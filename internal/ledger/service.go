package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
)

// Service holds the ledger use cases. It is the only surface a handler needs;
// nothing above it should see SQL or perform arithmetic on an amount.
//
// There is deliberately no CreateTransaction(type, …). One method per use case
// is what makes an expense filed under an income category, or an opening
// balance with a payment method, impossible to express rather than merely
// invalid — the difference between a state that is rejected and a state that
// cannot be written down.
//
// M2 has no operation that edits or deletes a transaction.
type Service struct {
	db   *sql.DB
	repo *Repository
}

// NewService builds the service over a pool. It keeps the *sql.DB so that every
// write can open its own transaction.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

// RecordIncome records money arriving in an account.
//
// The amount is a positive decimal string; the movement is stored positive. The
// category must be an active INCOME category, and the currency is the account's
// — there is nowhere in the input to say otherwise.
func (s *Service) RecordIncome(ctx context.Context, in RecordIncomeInput) (TransactionView, error) {
	view, err := s.record(ctx, recordRequest{
		txType:          Income,
		accountID:       in.AccountID,
		amount:          in.Amount,
		categoryID:      &in.CategoryID,
		paymentMethodID: &in.PaymentMethodID,
		description:     in.Description,
		effectiveDate:   in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}
	// Only an opening balance can decline to write anything.
	return *view, nil
}

// RecordExpense records money leaving an account.
//
// The amount is positive here too: "25.99", never "-25.99". The sign belongs to
// the operation, so the movement is stored negative and an expense that
// increases a balance cannot be expressed.
func (s *Service) RecordExpense(ctx context.Context, in RecordExpenseInput) (TransactionView, error) {
	view, err := s.record(ctx, recordRequest{
		txType:          Expense,
		accountID:       in.AccountID,
		amount:          in.Amount,
		categoryID:      &in.CategoryID,
		paymentMethodID: &in.PaymentMethodID,
		description:     in.Description,
		effectiveDate:   in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}
	return *view, nil
}

// RecordOpeningBalance records where an account started.
//
// A starting balance is a fact about the account, not income: recording it as
// income would put it in every "what did I earn" figure forever. It therefore
// has no category and no payment method, and the schema refuses one.
//
// An amount of zero records NOTHING and returns (nil, nil). The balance of an
// account with no movements is already zero, so a zero-amount row would assert
// nothing while appearing in every listing. Callers must check the pointer;
// this is the only method here that returns a nil value with a nil error.
//
// A negative opening balance is meaningful — an account that starts overdrawn —
// and is recorded as a negative movement.
func (s *Service) RecordOpeningBalance(ctx context.Context, in RecordOpeningBalanceInput) (*TransactionView, error) {
	return s.record(ctx, recordRequest{
		txType:        OpeningBalance,
		accountID:     in.AccountID,
		amount:        in.Amount,
		effectiveDate: in.EffectiveDate,
	})
}

// RecordOpeningBalanceTx is RecordOpeningBalance without the transaction, so
// that a workflow spanning more than one domain can run it inside a transaction
// of its own.
//
// It exists for exactly one caller: opening an account and recording its
// opening balance must be atomic, and that workflow cannot live in this package
// or in account without an import cycle. Everything else calls the method.
func RecordOpeningBalanceTx(
	ctx context.Context, exec database.DBTX, in RecordOpeningBalanceInput,
) (*TransactionView, error) {
	return recordTx(ctx, exec, recordRequest{
		txType:        OpeningBalance,
		accountID:     in.AccountID,
		amount:        in.Amount,
		effectiveDate: in.EffectiveDate,
	})
}

// GetTransaction returns one transaction as a listing row, or ErrNotFound.
func (s *Service) GetTransaction(ctx context.Context, id int64) (TransactionView, error) {
	return s.repo.GetView(ctx, id)
}

// ListTransactions returns recent activity, newest first by effective date.
func (s *Service) ListTransactions(ctx context.Context, in ListTransactionsInput) ([]TransactionView, error) {
	return s.repo.ListViews(ctx, in.limit())
}

// GetAccountBalance returns one account's balance, derived from its movements.
func (s *Service) GetAccountBalance(ctx context.Context, accountID int64) (AccountBalance, error) {
	return s.repo.GetBalance(ctx, accountID)
}

// ListAccountBalances returns every account with its balance, ordered by
// currency code, then name.
func (s *Service) ListAccountBalances(ctx context.Context, includeInactive bool) ([]AccountBalance, error) {
	return s.repo.ListBalances(ctx, includeInactive)
}

// ListCurrencyTotals returns the total held in each currency.
//
// One row per currency that has at least one account. Currencies are never
// converted into one another; a single consolidated figure needs exchange rates
// and belongs to the FX milestone.
func (s *Service) ListCurrencyTotals(ctx context.Context, includeInactive bool) ([]CurrencyTotal, error) {
	return s.repo.ListCurrencyTotals(ctx, includeInactive)
}

// recordRequest is the shared shape behind the three use cases. It is
// unexported on purpose: the public API is one method per use case, so that
// invalid combinations have no signature to arrive through.
type recordRequest struct {
	txType          TransactionType
	accountID       int64
	amount          string
	categoryID      *int64
	paymentMethodID *int64
	description     *string
	effectiveDate   string
}

// record runs a request in its own transaction.
func (s *Service) record(ctx context.Context, r recordRequest) (_ *TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back %s: %w", r.txType, rerr))
		}
	}()

	view, err := recordTx(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if view == nil {
		// A zero opening balance wrote nothing, so there is nothing to commit;
		// the deferred rollback closes the transaction.
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing %s: %w", r.txType, err)
	}
	return view, nil
}

// recordTx validates a request and writes the transaction and its movement
// through exec.
//
// exec must be an open transaction. That is what makes the invariants real
// rather than likely: on separate connections the account could be archived, or
// the category retired, between the check and the insert, and a transaction
// could be written whose movement then failed — leaving a financial event with
// no effect on any account.
func recordTx(ctx context.Context, exec database.DBTX, r recordRequest) (*TransactionView, error) {
	resolved, err := resolveRequest(ctx, exec, &r)
	if err != nil {
		return nil, err
	}

	if resolved.amountMinor == 0 {
		// Only an opening balance reaches here; amountFor rejects zero for the
		// other two. Nothing is written and nothing is an error: a balance of
		// zero is what an account with no movements already has.
		return nil, nil
	}

	repo := NewRepository(exec)
	now := database.Now()

	transaction, err := repo.InsertTransaction(ctx, Transaction{
		Type:            r.txType,
		CategoryID:      r.categoryID,
		PaymentMethodID: r.paymentMethodID,
		Description:     r.description,
		EffectiveDate:   resolved.effectiveDate,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		return nil, err
	}

	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     resolved.account.ID,
		AmountMinor:   resolved.amountMinor,
		CreatedAt:     now,
	}); err != nil {
		return nil, err
	}

	view, err := repo.GetView(ctx, transaction.ID)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// resolvedRequest is what validation produced: the account the movement lands
// in, the exact signed amount, and the calendar date.
type resolvedRequest struct {
	account       account.Account
	amountMinor   int64
	effectiveDate time.Time
}

// resolveRequest validates a request and resolves everything it refers to.
//
// Creating and editing both go through here, which is what stops them from
// drifting apart. An edit that could produce a state a creation would refuse
// would be a hole in every invariant this package maintains, and the only
// reliable way to prevent that is to run the same code.
//
// r is a pointer because normalisation -- trimming a description -- has to be
// visible to the caller that goes on to store it.
func resolveRequest(ctx context.Context, exec database.DBTX, r *recordRequest) (resolvedRequest, error) {
	r.description = normalizeDescription(r.description)
	if err := validateDescription(r.description); err != nil {
		return resolvedRequest{}, err
	}

	acct, err := resolveAccount(ctx, exec, r.accountID)
	if err != nil {
		return resolvedRequest{}, err
	}

	// The currency is the account's, always. It supplies the scale the amount is
	// read at, which is why it is loaded before the amount is parsed -- and why
	// moving a transaction to an account in another currency reinterprets the
	// amount rather than converting it.
	//
	// An archived currency does NOT block this. Retiring a currency stops it
	// denominating new accounts; it does not freeze the accounts that already
	// use it, and money keeps moving in them.
	cur, err := currency.NewRepository(exec).Get(ctx, acct.CurrencyCode)
	if err != nil {
		return resolvedRequest{}, fmt.Errorf(
			"looking up currency %q of account %d: %w", acct.CurrencyCode, acct.ID, err)
	}

	amountMinor, err := amountFor(r.txType, r.amount, cur.MinorUnit)
	if err != nil {
		return resolvedRequest{}, err
	}
	if amountMinor == 0 {
		// The caller decides what a zero means. Creating an opening balance
		// treats it as "record nothing"; editing one refuses it, because M3 has
		// no delete and the transaction would be left with no effect.
		//
		// The date is deliberately not examined here: an account opened with a
		// zero balance should not have to supply the date of an event that never
		// gets recorded.
		return resolvedRequest{account: acct}, nil
	}

	effectiveDate, err := validateEffectiveDate(r.effectiveDate)
	if err != nil {
		return resolvedRequest{}, err
	}

	if err := checkCategory(ctx, exec, *r); err != nil {
		return resolvedRequest{}, err
	}
	if err := checkPaymentMethod(ctx, exec, r.paymentMethodID); err != nil {
		return resolvedRequest{}, err
	}

	return resolvedRequest{account: acct, amountMinor: amountMinor, effectiveDate: effectiveDate}, nil
}

// resolveAccount loads an account a movement is about to land in and refuses it
// if it cannot take one.
//
// Every use case that writes a movement goes through here — income, expenses,
// opening balances, and both ends of a transfer — so "the account must exist and
// be active" is decided in exactly one place. Two copies of that rule would
// eventually disagree, and the one that disagreed would be the one letting a
// movement into an archived account.
//
// Being archived is decided here, on the field, rather than being an error the
// account package raises: it is a fact about the account, not a failure to load
// it.
func resolveAccount(ctx context.Context, exec database.DBTX, id int64) (account.Account, error) {
	acct, err := account.NewRepository(exec).Get(ctx, id)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return account.Account{}, fmt.Errorf("account %d: %w", id, ErrAccountNotFound)
	case err != nil:
		return account.Account{}, fmt.Errorf("looking up account %d: %w", id, err)
	}
	if !acct.IsActive {
		return account.Account{}, fmt.Errorf("account %q: %w", acct.Name, ErrAccountInactive)
	}
	return acct, nil
}

// amountFor reads the amount and applies the use case's sign rule.
//
// The sign is derived from the operation rather than taken from input. That is
// the whole point: an expense is negative because it is an expense, so there is
// no way to record one that increases a balance.
func amountFor(txType TransactionType, amount string, minorUnit int) (int64, error) {
	minor, err := money.Parse(amount, minorUnit)
	if err != nil {
		// Translated so that a handler never has to import money to interpret
		// what the ledger returned.
		return 0, fmt.Errorf("%w: %s", ErrInvalidAmount, unwrapAmountMessage(err))
	}

	switch txType {
	case Income:
		if minor <= 0 {
			return 0, fmt.Errorf("%w: an income amount must be greater than zero; the sign is not part of the input",
				ErrInvalidAmount)
		}
		return minor, nil

	case Expense:
		if minor <= 0 {
			return 0, fmt.Errorf("%w: an expense amount must be greater than zero; it is stored as a negative movement",
				ErrInvalidAmount)
		}
		return -minor, nil

	case OpeningBalance:
		// Zero is legitimate and means "record nothing"; negative is an account
		// that starts overdrawn.
		return minor, nil

	case Transfer:
		// Unreachable: a transfer has two accounts and two signed amounts, so it
		// never travels through the single-account request this function serves.
		// Spelled out rather than left to the default so that the boundary is a
		// statement instead of an accident.
		return 0, fmt.Errorf(
			"%w: a transfer is recorded through RecordTransfer, not as a single movement", ErrInvalidInput)

	default:
		return 0, fmt.Errorf("%w: unknown transaction type %q", ErrInvalidInput, txType)
	}
}

// checkCategory verifies that the category exists, is usable, and matches the
// transaction type.
//
// The type match cannot be a database constraint: it compares a column of
// transactions with a column of categories, and CHECK cannot reach across
// tables. It is enforced here instead, inside the same transaction as the
// insert, which is the same treatment "an account's currency must be active"
// gets in the account package.
func checkCategory(ctx context.Context, exec database.DBTX, r recordRequest) error {
	if r.categoryID == nil {
		return nil
	}

	cat, err := category.NewRepository(exec).Get(ctx, *r.categoryID)
	switch {
	case errors.Is(err, category.ErrNotFound):
		return fmt.Errorf("category %d: %w", *r.categoryID, ErrCategoryNotFound)
	case err != nil:
		return fmt.Errorf("looking up category %d: %w", *r.categoryID, err)
	}
	if !cat.IsActive {
		return fmt.Errorf("category %q: %w", cat.Name, ErrCategoryInactive)
	}

	want, err := categoryTypeFor(r.txType)
	if err != nil {
		return err
	}
	if cat.Type != want {
		return fmt.Errorf("%w: category %q is %s, but this is %s",
			ErrCategoryTypeMismatch, cat.Name, cat.Type, r.txType)
	}
	return nil
}

// checkPaymentMethod verifies that the payment method exists and is usable.
//
// Like the category check, this compares rows in two tables and so cannot be a
// database constraint. The foreign key guarantees the method exists; only this
// guarantees it was still active.
func checkPaymentMethod(ctx context.Context, exec database.DBTX, id *int64) error {
	if id == nil {
		return nil
	}

	method, err := paymentmethod.NewRepository(exec).Get(ctx, *id)
	switch {
	case errors.Is(err, paymentmethod.ErrNotFound):
		return fmt.Errorf("payment method %d: %w", *id, ErrPaymentMethodNotFound)
	case err != nil:
		return fmt.Errorf("looking up payment method %d: %w", *id, err)
	}
	if !method.IsActive {
		return fmt.Errorf("payment method %q: %w", method.Name, ErrPaymentMethodInactive)
	}
	return nil
}

// categoryTypeFor maps a transaction type to the category type it demands.
func categoryTypeFor(txType TransactionType) (category.Type, error) {
	switch txType {
	case Income:
		return category.Income, nil
	case Expense:
		return category.Expense, nil
	default:
		return "", fmt.Errorf("%w: a %s transaction has no category", ErrInvalidInput, txType)
	}
}

// unwrapAmountMessage takes the explanation out of a money error without
// carrying its sentinel along, so that callers match ErrInvalidAmount and see a
// message that still says what was wrong.
func unwrapAmountMessage(err error) string {
	message := err.Error()
	if after, found := strings.CutPrefix(message, money.ErrInvalidAmount.Error()+": "); found {
		return after
	}
	return message
}

// UpdateTransaction corrects an income or an expense.
//
// Everything a creation checks is checked again: the account exists and is
// active, the category exists, is active and matches the type, the payment
// method exists and is active, the amount is positive and within the currency's
// precision, and the date is a real calendar date. An edit that produced a state
// a creation would have refused would be a hole in every invariant this package
// maintains.
//
// The transaction and its movement are rewritten in ONE transaction. There is no
// moment at which a corrected event sits beside an uncorrected movement, so the
// balance is right the instant the commit returns.
//
// M3 does not keep the previous version of an edited record. That is a scope
// decision, documented in docs/m3-contract.md; the way back is a backup.
func (s *Service) UpdateTransaction(ctx context.Context, id int64, in UpdateTransactionInput) (TransactionView, error) {
	view, err := s.update(ctx, id, func(ctx context.Context, exec database.DBTX, current Transaction) (recordRequest, error) {
		// Income and expenses convert freely into each other. Nothing else
		// converts at all.
		//
		// The guard names what IS allowed rather than listing what is not. The
		// difference matters: written as "if current.Type == OpeningBalance", a
		// transfer would have walked straight through the moment TRANSFER
		// joined the enumeration, and this method would have rewritten one of
		// its two movements and left the other — a transfer with a debit and no
		// credit, which is money destroyed. Every type this method does not
		// name is refused, including any added later.
		if current.Type != Income && current.Type != Expense {
			return recordRequest{}, fmt.Errorf(
				"%w: transaction %d is %s and must be edited through the use case for that type",
				ErrTypeChangeNotAllowed, id, current.Type)
		}
		if in.Type != Income && in.Type != Expense {
			return recordRequest{}, fmt.Errorf(
				"%w: a transaction can only be corrected to %s or %s", ErrTypeChangeNotAllowed, Income, Expense)
		}

		return recordRequest{
			txType:          in.Type,
			accountID:       in.AccountID,
			amount:          in.Amount,
			categoryID:      &in.CategoryID,
			paymentMethodID: &in.PaymentMethodID,
			description:     in.Description,
			effectiveDate:   in.EffectiveDate,
		}, nil
	}, in.AllowCurrencyChange)
	if err != nil {
		return TransactionView{}, err
	}
	return *view, nil
}

// UpdateOpeningBalance corrects where an account started.
//
// Only the amount and the date can change. The account cannot: an opening
// balance belongs to the account it opened, and moving it would mean that
// account never had a starting point while another gained one it never had.
func (s *Service) UpdateOpeningBalance(ctx context.Context, id int64, in UpdateOpeningBalanceInput) (TransactionView, error) {
	view, err := s.update(ctx, id, func(ctx context.Context, exec database.DBTX, current Transaction) (recordRequest, error) {
		if current.Type != OpeningBalance {
			return recordRequest{}, fmt.Errorf(
				"%w: transaction %d is a %s, not an opening balance", ErrTypeChangeNotAllowed, id, current.Type)
		}

		movement, err := NewRepository(exec).GetMovementFor(ctx, id)
		if err != nil {
			return recordRequest{}, err
		}

		return recordRequest{
			txType: OpeningBalance,
			// The account is taken from the existing movement, not from input:
			// there is no field through which it could be changed.
			accountID:     movement.AccountID,
			amount:        in.Amount,
			effectiveDate: in.EffectiveDate,
		}, nil
	}, false)
	if err != nil {
		return TransactionView{}, err
	}
	return *view, nil
}

// buildRequest turns an edit into the same shape a creation uses, after checking
// whatever is specific to the transaction being edited.
type buildRequest func(context.Context, database.DBTX, Transaction) (recordRequest, error)

// update applies an edit inside one transaction.
//
// It deliberately reuses the validation a creation performs rather than
// duplicating it: the two must not be able to disagree about what a valid
// transaction is.
func (s *Service) update(
	ctx context.Context, id int64, build buildRequest, allowCurrencyChange bool,
) (_ *TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the edit of transaction %d: %w", id, rerr))
		}
	}()

	repo := NewRepository(tx)

	current, err := repo.GetTransaction(ctx, id)
	if err != nil {
		return nil, err
	}
	movement, err := repo.GetMovementFor(ctx, id)
	if err != nil {
		return nil, err
	}

	request, err := build(ctx, tx, current)
	if err != nil {
		return nil, err
	}

	resolved, err := resolveRequest(ctx, tx, &request)
	if err != nil {
		return nil, err
	}

	// Moving a transaction between currencies reinterprets its amount rather
	// than converting it, so it has to be asked for.
	if !allowCurrencyChange && resolved.account.ID != movement.AccountID {
		previous, err := account.NewRepository(tx).Get(ctx, movement.AccountID)
		if err != nil {
			return nil, fmt.Errorf("looking up the current account of transaction %d: %w", id, err)
		}
		if previous.CurrencyCode != resolved.account.CurrencyCode {
			return nil, fmt.Errorf(
				"%w: %q is in %s and %q is in %s, and %s does not convert between them",
				ErrCurrencyChangeNotConfirmed, previous.Name, previous.CurrencyCode,
				resolved.account.Name, resolved.account.CurrencyCode, "Finance Tracker")
		}
	}

	// An edit cannot delete, so it cannot reduce a transaction to nothing.
	if resolved.amountMinor == 0 {
		return nil, fmt.Errorf(
			"%w: an amount of zero would leave a transaction with no effect, and transactions cannot be deleted",
			ErrInvalidAmount)
	}

	if err := repo.UpdateTransaction(ctx, Transaction{
		ID:              id,
		Type:            request.txType,
		CategoryID:      request.categoryID,
		PaymentMethodID: request.paymentMethodID,
		Description:     request.description,
		EffectiveDate:   resolved.effectiveDate,
		UpdatedAt:       database.Now(),
	}); err != nil {
		return nil, err
	}

	// The movement's sign comes from the new type, never from the old row.
	if err := repo.UpdateMovement(ctx, movement.ID, resolved.account.ID, resolved.amountMinor); err != nil {
		return nil, err
	}

	view, err := repo.GetView(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing the edit of transaction %d: %w", id, err)
	}
	return &view, nil
}
