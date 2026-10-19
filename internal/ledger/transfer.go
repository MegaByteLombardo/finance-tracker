package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// This file holds transfers between two of the user's own accounts: the first
// use the ledger makes of its own shape, one financial event with more than one
// impact.
//
// A transfer is not income and not an expense. The money was the user's before
// and is the user's after; only where it sits changed. That is why it carries no
// category — filing it under one would contaminate every future question about
// where money came from or went — and no payment method, because no channel with
// the outside world was used. It is also why the sum of an account's movements
// is still every balance there is: the two movements cancel out, so the total
// held in the currency is identical before and after. That invariant is the
// point of the whole type.
//
// It lives in internal/ledger rather than in a package of its own. A transfer
// writes the same two tables through the same repository, validates with the
// same helpers, and speaks the same errors as every other use case here; a
// separate package would have to import all of that back, or ledger would import
// it, and either way the boundary would be a line drawn through one idea.

// RecordTransfer moves money between two of the user's own accounts.
//
// It writes ONE transaction and TWO movements: the source account's is negative,
// the destination account's is positive, and they are equal in magnitude, so
// they sum to zero and no money is created or destroyed. Both accounts must
// exist, be active, be different from each other, and share a currency — M4 has
// no exchange rates, so a transfer between currencies cannot be represented.
//
// The amount is a positive decimal string. The direction is the pair of
// accounts, not the sign, which is what makes a transfer that takes money out of
// its destination impossible to express rather than merely invalid.
//
// A transfer may leave an account with a negative balance and this is not
// checked. The ledger records what happened; overdrafts happen, statements show
// them, and a cash account that goes negative is usually telling the user that
// something is missing rather than that they did something illegal. Refusing it
// would also be a rule that only sometimes applied: effective_date can be
// backdated, so a transfer entered today can make last week's balance negative
// no matter what is checked at the moment of writing.
func (s *Service) RecordTransfer(ctx context.Context, in RecordTransferInput) (_ TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TransactionView{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the transfer: %w", rerr))
		}
	}()

	view, err := recordTransferTx(ctx, tx, transferRequest{
		sourceAccountID:      in.SourceAccountID,
		destinationAccountID: in.DestinationAccountID,
		amount:               in.Amount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}

	if err := tx.Commit(); err != nil {
		return TransactionView{}, fmt.Errorf("committing the transfer: %w", err)
	}
	return view, nil
}

// UpdateTransfer corrects a transfer.
//
// The source, the destination, the amount, the date and the description can all
// change, and everything a creation checks is checked again — an edit that could
// produce a state a creation would have refused would be a second door into
// every invariant this package maintains.
//
// The transaction and BOTH movements are rewritten in one database transaction.
// There is no moment at which one side of a transfer has been corrected and the
// other has not, so no moment at which the two balances disagree about how much
// money exists.
//
// The two movements keep their ids. An edit corrects an event; it does not
// replace it with a different one, and the rows that record its two impacts are
// the same rows they always were. Recreating them would also mean deleting them,
// and nothing in this application deletes a financial row.
//
// The type cannot change: a transfer cannot become income, an expense or an
// opening balance, and none of those can become a transfer. UpdateTransferInput
// has no Type field, so there is no signature through which to try.
//
// M4 does not keep the previous version of an edited record. That is a scope
// decision, documented in docs/m3-contract.md; the way back is a backup.
func (s *Service) UpdateTransfer(
	ctx context.Context, id int64, in UpdateTransferInput,
) (_ TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TransactionView{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the edit of transfer %d: %w", id, rerr))
		}
	}()

	repo := NewRepository(tx)

	current, err := repo.GetTransaction(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}
	if current.Type != Transfer {
		return TransactionView{}, fmt.Errorf(
			"%w: transaction %d is %s, not a transfer", ErrTypeChangeNotAllowed, id, current.Type)
	}

	movements, err := repo.ListMovementsFor(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}
	if err := checkTransferMovementCount(id, len(movements)); err != nil {
		return TransactionView{}, err
	}
	sourceMovement, destinationMovement, err := splitTransfer(id,
		transferSide{movementID: movements[0].ID, accountID: movements[0].AccountID, amountMinor: movements[0].AmountMinor},
		transferSide{movementID: movements[1].ID, accountID: movements[1].AccountID, amountMinor: movements[1].AmountMinor})
	if err != nil {
		return TransactionView{}, err
	}

	// The currency the transfer is in today, read before the edit resolves the
	// new one. Taken from the source movement's account: both ends share a
	// currency, so either would do.
	previous, err := account.NewRepository(tx).Get(ctx, sourceMovement.accountID)
	if err != nil {
		return TransactionView{}, fmt.Errorf(
			"looking up the current source account of transfer %d: %w", id, err)
	}

	// The SAME validation a creation performs, not a copy of it.
	resolved, err := resolveTransfer(ctx, tx, transferRequest{
		sourceAccountID:      in.SourceAccountID,
		destinationAccountID: in.DestinationAccountID,
		amount:               in.Amount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}

	// Moving a transfer to a pair of accounts in another currency reinterprets
	// its amount rather than converting it — "250.00" read at another scale is a
	// different sum of money — so it has to be asked for. Same rule, same
	// reason, as UpdateTransaction.
	if !in.AllowCurrencyChange && previous.CurrencyCode != resolved.source.CurrencyCode {
		return TransactionView{}, fmt.Errorf(
			"%w: this transfer is in %s and %q is in %s, and Finance Tracker does not convert between them",
			ErrCurrencyChangeNotConfirmed, previous.CurrencyCode,
			resolved.source.Name, resolved.source.CurrencyCode)
	}

	if err := repo.UpdateTransaction(ctx, Transaction{
		ID:   id,
		Type: Transfer,
		// A transfer has neither, and rewriting them explicitly is what stops an
		// edit leaving behind a category the transaction acquired some other
		// way. The database's CHECK refuses the row if either survives.
		CategoryID:      nil,
		PaymentMethodID: nil,
		Description:     resolved.description,
		EffectiveDate:   resolved.effectiveDate,
		UpdatedAt:       database.Now(),
	}); err != nil {
		return TransactionView{}, err
	}

	// Each row keeps the ROLE its sign already gave it: the movement that was
	// negative stays negative and takes the new source, the one that was
	// positive stays positive and takes the new destination. Swapping the two
	// accounts therefore swaps what the accounts are, not which row is which.
	if err := repo.UpdateMovement(ctx, sourceMovement.movementID, resolved.source.ID, -resolved.amountMinor); err != nil {
		return TransactionView{}, err
	}
	if err := repo.UpdateMovement(ctx, destinationMovement.movementID, resolved.destination.ID, resolved.amountMinor); err != nil {
		return TransactionView{}, err
	}

	view, err := repo.GetView(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}

	if err := tx.Commit(); err != nil {
		return TransactionView{}, fmt.Errorf("committing the edit of transfer %d: %w", id, err)
	}
	return view, nil
}

// transferRequest is the shared shape behind recording and editing a transfer.
//
// It is unexported for the same reason recordRequest is: the public API is one
// method per use case, so an invalid combination has no signature to arrive
// through.
type transferRequest struct {
	sourceAccountID      int64
	destinationAccountID int64
	amount               string
	description          *string
	effectiveDate        string
}

// resolvedTransfer is what validation produced: two accounts that can take a
// movement, the exact positive amount in their shared currency, the calendar
// date, and the normalised description.
type resolvedTransfer struct {
	source        account.Account
	destination   account.Account
	amountMinor   int64
	effectiveDate time.Time
	description   *string
}

// RecordTransferTx is RecordTransfer without the transaction, so that a
// transfer and the savings whose backing moves with it commit together (M7.4,
// internal/application). The rules are RecordTransfer's.
func RecordTransferTx(ctx context.Context, exec database.DBTX, in RecordTransferInput) (TransactionView, error) {
	return recordTransferTx(ctx, exec, transferRequest{
		sourceAccountID:      in.SourceAccountID,
		destinationAccountID: in.DestinationAccountID,
		amount:               in.Amount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
}

// recordTransferTx validates a request and writes the transaction and its two
// movements through exec.
//
// exec must be an open transaction. That is what makes the invariants real
// rather than likely: on separate connections an account could be archived
// between the check and the insert, or the second movement could fail after the
// first succeeded, leaving a transfer with a debit and no credit — money
// destroyed, silently, in a ledger whose whole purpose is not to do that.
func recordTransferTx(ctx context.Context, exec database.DBTX, r transferRequest) (TransactionView, error) {
	resolved, err := resolveTransfer(ctx, exec, r)
	if err != nil {
		return TransactionView{}, err
	}

	repo := NewRepository(exec)
	now := database.Now()

	transaction, err := repo.InsertTransaction(ctx, Transaction{
		Type: Transfer,
		// Neither is given, and the database refuses the row if either is.
		CategoryID:      nil,
		PaymentMethodID: nil,
		Description:     resolved.description,
		EffectiveDate:   resolved.effectiveDate,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		return TransactionView{}, err
	}

	// Source first, so that the lower movement id is the one money left from.
	// Nothing depends on that — the read path decides which side is which by the
	// sign — but a ledger that reads in the order the money moved is easier to
	// follow in a raw query.
	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     resolved.source.ID,
		AmountMinor:   -resolved.amountMinor,
		CreatedAt:     now,
	}); err != nil {
		return TransactionView{}, err
	}
	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     resolved.destination.ID,
		AmountMinor:   resolved.amountMinor,
		CreatedAt:     now,
	}); err != nil {
		return TransactionView{}, err
	}

	return repo.GetView(ctx, transaction.ID)
}

// resolveTransfer validates a request and resolves everything it refers to.
//
// Recording and editing both go through here, which is what stops them from
// drifting apart, exactly as resolveRequest does for the single-account use
// cases.
//
// The order of the checks is part of the contract, so that a form shows the same
// error for the same mistake every time: description, source, destination, the
// two accounts against each other, the amount, the date.
func resolveTransfer(ctx context.Context, exec database.DBTX, r transferRequest) (resolvedTransfer, error) {
	description := normalizeDescription(r.description)
	if err := validateDescription(description); err != nil {
		return resolvedTransfer{}, err
	}

	source, err := resolveAccount(ctx, exec, r.sourceAccountID)
	if err != nil {
		return resolvedTransfer{}, accountSideError(SourceSide, r.sourceAccountID, err)
	}
	destination, err := resolveAccount(ctx, exec, r.destinationAccountID)
	if err != nil {
		return resolvedTransfer{}, accountSideError(DestinationSide, r.destinationAccountID, err)
	}

	if source.ID == destination.ID {
		return resolvedTransfer{}, fmt.Errorf(
			"%w: %q was given as both ends of it", ErrSameAccount, source.Name)
	}

	// The two accounts must share a currency. There is no conversion here and
	// there is no field through which to supply a rate: a cross-currency
	// transfer needs a source amount, a destination amount and the rate between
	// them, and all three belong to the FX milestone.
	if source.CurrencyCode != destination.CurrencyCode {
		return resolvedTransfer{}, fmt.Errorf(
			"%w: %q is in %s and %q is in %s",
			ErrCurrencyMismatch, source.Name, source.CurrencyCode,
			destination.Name, destination.CurrencyCode)
	}

	// The currency is the accounts', always. It supplies the scale the amount is
	// read at, which is why it is loaded before the amount is parsed.
	//
	// An archived currency does NOT block a transfer, for the same reason it does
	// not block any other movement: retiring a currency stops it denominating new
	// accounts, it does not freeze the accounts that already use it.
	cur, err := currency.NewRepository(exec).Get(ctx, source.CurrencyCode)
	if err != nil {
		return resolvedTransfer{}, fmt.Errorf(
			"looking up currency %q of account %d: %w", source.CurrencyCode, source.ID, err)
	}

	amountMinor, err := money.Parse(r.amount, cur.MinorUnit)
	if err != nil {
		// Translated so that a handler never has to import money to interpret
		// what the ledger returned.
		return resolvedTransfer{}, fmt.Errorf("%w: %s", ErrInvalidAmount, unwrapAmountMessage(err))
	}
	if amountMinor <= 0 {
		return resolvedTransfer{}, fmt.Errorf(
			"%w: a transfer amount must be greater than zero; the direction is which accounts were named, not the sign",
			ErrInvalidAmount)
	}

	effectiveDate, err := validateEffectiveDate(r.effectiveDate)
	if err != nil {
		return resolvedTransfer{}, err
	}

	return resolvedTransfer{
		source:        source,
		destination:   destination,
		amountMinor:   amountMinor,
		effectiveDate: effectiveDate,
		description:   description,
	}, nil
}

// accountSideError says which end of a two-sided transaction an account error
// came from, so that a form can mark the right field. Transfers and exchanges
// both go through it.
//
// Only the two conditions a form can act on are wrapped. A failure to read the
// database is passed through untouched: it is not about that account.
func accountSideError(side MovementSide, accountID int64, err error) error {
	if errors.Is(err, ErrAccountNotFound) || errors.Is(err, ErrAccountInactive) {
		return &AccountSideError{Side: side, AccountID: accountID, Err: err}
	}
	return err
}

// transferSide is one half of a transfer: a movement, the account it lands in,
// and the signed amount. accountName is filled in only by the read path, which
// has joined it.
type transferSide struct {
	movementID  int64
	accountID   int64
	accountName string
	amountMinor int64
}

// checkTransferMovementCount refuses a transfer that does not have exactly two
// movements.
func checkTransferMovementCount(transactionID int64, count int) error {
	if count != 2 {
		return fmt.Errorf("transaction %d: %w: it has %d movements, and a transfer has exactly 2",
			transactionID, ErrInconsistentTransfer, count)
	}
	return nil
}

// splitTransfer decides which of a transfer's two movements is the source and
// which is the destination, and refuses any pair that is not a transfer.
//
// This is the one implementation of what a stored transfer must look like, used
// by the read path when it assembles a TransferDetail and by UpdateTransfer
// before it rewrites anything. Two copies of these rules would eventually
// disagree, and the disagreement would be invisible until a balance was wrong.
//
// They are checked in Go rather than in SQLite because every one of them spans
// two rows. A CHECK constraint sees one row and cannot look at its sibling;
// expressing "exactly two movements, opposite in sign and equal in magnitude,
// in two different accounts" would take triggers on every write to
// account_movements, which is a lot of machinery guarding a shape that one
// function and one database transaction already guarantee. What the schema does
// enforce is what it can see from a single row — a transfer has no category and
// no payment method, and no movement is ever zero.
//
// Reaching an error here means the stored data is already wrong, which no input
// can cause. It is reported rather than repaired: rendering half a transfer as
// though it were whole would be a ledger lying quietly.
func splitTransfer(transactionID int64, a, b transferSide) (source, destination transferSide, err error) {
	// amount_minor can never be zero — the column refuses it — so exactly one of
	// these three branches is right.
	switch {
	case a.amountMinor < 0 && b.amountMinor > 0:
		source, destination = a, b
	case a.amountMinor > 0 && b.amountMinor < 0:
		source, destination = b, a
	default:
		return transferSide{}, transferSide{}, fmt.Errorf(
			"transaction %d: %w: its movements are %d and %d, and a transfer has one negative and one positive",
			transactionID, ErrInconsistentTransfer, a.amountMinor, b.amountMinor)
	}

	// The invariant that makes a transfer neither create nor destroy money.
	// Written as a sum rather than as abs(source) == destination because
	// negating math.MinInt64 overflows, and adding one negative int64 to one
	// positive int64 cannot.
	if source.amountMinor+destination.amountMinor != 0 {
		return transferSide{}, transferSide{}, fmt.Errorf(
			"transaction %d: %w: its movements are %d and %d, which do not cancel out",
			transactionID, ErrInconsistentTransfer, source.amountMinor, destination.amountMinor)
	}

	if source.accountID == destination.accountID {
		return transferSide{}, transferSide{}, fmt.Errorf(
			"transaction %d: %w: both of its movements land in account %d",
			transactionID, ErrInconsistentTransfer, source.accountID)
	}

	return source, destination, nil
}
