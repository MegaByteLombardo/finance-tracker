package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// This file holds currency exchanges: the second use the ledger makes of its own
// shape, and the first transaction whose two movements are in two different
// currencies.
//
//	Banesco VES   -85,000.00 VES      →      Mercantil USD   +100.00 USD
//
// It is NOT a transfer. A transfer moves one sum of money, so its two movements
// are equal and opposite and the total held in its currency is identical before
// and after — that invariant is the point of the type. An exchange has two
// amounts, in two currencies, that are not related by any arithmetic this
// application performs: 8,500,000 céntimos is not 10,000 cents, and asserting
// anything about the two magnitudes together would be asserting something about
// two different kinds of quantity. TRANSFER was therefore not widened; the two
// are separate events with separate use cases.
//
// It is not income and not an expense either. Nothing was earned and nothing was
// consumed; the same wealth is denominated differently afterwards. So it carries
// no category — filing it under one would contaminate every later question about
// where money came from or went — and no payment method.
//
// A commission is out of scope and stays out: if the bank charged one
// separately, it is an EXPENSE under a category such as "Bank fees". If the user
// only knows the two final amounts, those are exactly what to enter, and the
// derived execution rate then already reflects what the commission cost.
//
// It lives in internal/ledger, beside transfers, for the same reason they do: it
// writes the same two tables through the same repository, validates with the
// same helpers, and speaks the same errors.

// RecordExchange records money actually exchanged between two of the user's own
// accounts, in two different currencies.
//
// It writes ONE transaction and TWO movements: the source account's is negative
// and holds the amount delivered, the destination account's is positive and
// holds the amount received. Both accounts must exist, be active, be different
// from each other, and be denominated in DIFFERENT currencies.
//
// Both amounts are positive decimal strings, each read at its own account's
// currency scale. The direction is the pair of accounts, not a sign, which is
// what makes an exchange that takes money out of its destination impossible to
// express rather than merely invalid. Whether this was a purchase or a sale of
// dollars is stated by which account was the destination; there is no field for
// it, and none is needed.
//
// NO EXCHANGE RATE IS READ AND NONE IS WRITTEN. The rates in exchange_rates say
// what a source published on a day and are used to value balances; they have no
// part in recording what the user actually traded at, and this method does not
// consult them, does not create one, and does not update one. The rate this
// operation happened at is derived from the two amounts whenever it is read.
//
// An exchange may leave an account with a negative balance and this is not
// checked, for exactly the reasons set out for transfers in TD-043.
func (s *Service) RecordExchange(ctx context.Context, in RecordExchangeInput) (_ TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TransactionView{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		// After a successful Commit, Rollback reports sql.ErrTxDone, which is
		// the expected outcome and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the exchange: %w", rerr))
		}
	}()

	view, err := recordExchangeTx(ctx, tx, exchangeRequest{
		sourceAccountID:      in.SourceAccountID,
		sourceAmount:         in.SourceAmount,
		destinationAccountID: in.DestinationAccountID,
		destinationAmount:    in.DestinationAmount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}

	if err := tx.Commit(); err != nil {
		return TransactionView{}, fmt.Errorf("committing the exchange: %w", err)
	}
	return view, nil
}

// UpdateExchange corrects an exchange.
//
// Both accounts, both amounts, the date and the description can all change, and
// everything a creation checks is checked again — an edit that could produce a
// state a creation would have refused would be a second door into every
// invariant this package maintains.
//
// The transaction and BOTH movements are rewritten in one database transaction.
// There is no moment at which one side of an exchange has been corrected and the
// other has not, so no moment at which one currency's balance reflects the
// correction and the other does not.
//
// The two movements keep their ids, for the reason recorded in TD-042: an edit
// corrects an event, it does not replace it with a different one, and nothing in
// this application deletes a financial row.
//
// The execution rate is not corrected, because it is not stored. Changing either
// amount changes what the derived rate is, on the next read, with nothing to
// keep in step.
//
// The type cannot change: an exchange cannot become a transfer, income, an
// expense or an opening balance, and none of those can become an exchange.
// UpdateExchangeInput has no Type field, so there is no signature through which
// to try, and this method refuses any transaction that is not already an
// FX_EXCHANGE.
func (s *Service) UpdateExchange(
	ctx context.Context, id int64, in UpdateExchangeInput,
) (_ TransactionView, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TransactionView{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back the edit of exchange %d: %w", id, rerr))
		}
	}()

	repo := NewRepository(tx)

	current, err := repo.GetTransaction(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}
	// The guard names what is ACCEPTED, per TD-045. Every type this method does
	// not name is refused, including any added later.
	if current.Type != FXExchange {
		return TransactionView{}, fmt.Errorf(
			"%w: transaction %d is %s, not an exchange", ErrTypeChangeNotAllowed, id, current.Type)
	}

	movements, err := repo.ListMovementsFor(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}
	if err := checkExchangeMovementCount(id, len(movements)); err != nil {
		return TransactionView{}, err
	}
	sourceMovement, destinationMovement, err := splitExchangeBySign(id,
		exchangeSide{movementID: movements[0].ID, accountID: movements[0].AccountID, amountMinor: movements[0].AmountMinor},
		exchangeSide{movementID: movements[1].ID, accountID: movements[1].AccountID, amountMinor: movements[1].AmountMinor})
	if err != nil {
		return TransactionView{}, err
	}

	// The SAME validation a creation performs, not a copy of it.
	resolved, err := resolveExchange(ctx, tx, exchangeRequest{
		sourceAccountID:      in.SourceAccountID,
		sourceAmount:         in.SourceAmount,
		destinationAccountID: in.DestinationAccountID,
		destinationAmount:    in.DestinationAmount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
	if err != nil {
		return TransactionView{}, err
	}

	if err := repo.UpdateTransaction(ctx, Transaction{
		ID:   id,
		Type: FXExchange,
		// An exchange has neither, and rewriting them explicitly is what stops
		// an edit leaving behind a category the transaction acquired some other
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
	// negative stays negative and takes the new source account and the new
	// delivered amount, the one that was positive stays positive and takes the
	// new destination. Swapping the two accounts therefore turns a purchase into
	// a sale without renumbering anything.
	if err := repo.UpdateMovement(ctx,
		sourceMovement.movementID, resolved.source.ID, -resolved.sourceAmountMinor); err != nil {
		return TransactionView{}, err
	}
	if err := repo.UpdateMovement(ctx,
		destinationMovement.movementID, resolved.destination.ID, resolved.destinationAmountMinor); err != nil {
		return TransactionView{}, err
	}

	view, err := repo.GetView(ctx, id)
	if err != nil {
		return TransactionView{}, err
	}

	if err := tx.Commit(); err != nil {
		return TransactionView{}, fmt.Errorf("committing the edit of exchange %d: %w", id, err)
	}
	return view, nil
}

// exchangeRequest is the shared shape behind recording and editing an exchange.
//
// It is unexported for the same reason transferRequest is: the public API is one
// method per use case, so an invalid combination has no signature to arrive
// through.
type exchangeRequest struct {
	sourceAccountID      int64
	sourceAmount         string
	destinationAccountID int64
	destinationAmount    string
	description          *string
	effectiveDate        string
}

// resolvedExchange is what validation produced: two accounts that can take a
// movement, in two different currencies, the two exact positive amounts in their
// own currencies' minor units, the calendar date, and the normalised
// description.
type resolvedExchange struct {
	source                 account.Account
	sourceCurrency         currency.Currency
	sourceAmountMinor      int64
	destination            account.Account
	destinationCurrency    currency.Currency
	destinationAmountMinor int64
	effectiveDate          time.Time
	description            *string
}

// RecordExchangeTx is RecordExchange without the transaction, so that a card
// payment from another currency commits with its request key and its savings
// plan (M8.2, internal/application). The rules are RecordExchange's.
func RecordExchangeTx(ctx context.Context, exec database.DBTX, in RecordExchangeInput) (TransactionView, error) {
	return recordExchangeTx(ctx, exec, exchangeRequest{
		sourceAccountID:      in.SourceAccountID,
		sourceAmount:         in.SourceAmount,
		destinationAccountID: in.DestinationAccountID,
		destinationAmount:    in.DestinationAmount,
		description:          in.Description,
		effectiveDate:        in.EffectiveDate,
	})
}

// recordExchangeTx validates a request and writes the transaction and its two
// movements through exec.
//
// exec must be an open transaction. That is what makes the invariants real
// rather than likely: on separate connections an account could be archived
// between the check and the insert, or the second movement could fail after the
// first succeeded — leaving an exchange that removed bolívares and produced no
// dollars, which is money destroyed in a ledger whose whole purpose is not to do
// that.
func recordExchangeTx(ctx context.Context, exec database.DBTX, r exchangeRequest) (TransactionView, error) {
	resolved, err := resolveExchange(ctx, exec, r)
	if err != nil {
		return TransactionView{}, err
	}

	repo := NewRepository(exec)
	now := database.Now()

	transaction, err := repo.InsertTransaction(ctx, Transaction{
		Type: FXExchange,
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

	// Source first, so that the lower movement id is the one money left from,
	// exactly as a transfer does. Nothing depends on it — the read path decides
	// which side is which by the sign — but a ledger that reads in the order the
	// money moved is easier to follow in a raw query.
	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     resolved.source.ID,
		AmountMinor:   -resolved.sourceAmountMinor,
		CreatedAt:     now,
	}); err != nil {
		return TransactionView{}, err
	}
	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     resolved.destination.ID,
		AmountMinor:   resolved.destinationAmountMinor,
		CreatedAt:     now,
	}); err != nil {
		return TransactionView{}, err
	}

	return repo.GetView(ctx, transaction.ID)
}

// resolveExchange validates a request and resolves everything it refers to.
//
// Recording and editing both go through here, which is what stops them from
// drifting apart, exactly as resolveTransfer does for same-currency movement.
//
// The order of the checks is part of the contract, so that a form shows the same
// error for the same mistake every time: description, source account,
// destination account, the two accounts against each other, the two currencies
// against each other, the source amount, the destination amount, the date.
func resolveExchange(ctx context.Context, exec database.DBTX, r exchangeRequest) (resolvedExchange, error) {
	description := normalizeDescription(r.description)
	if err := validateDescription(description); err != nil {
		return resolvedExchange{}, err
	}

	source, err := resolveAccount(ctx, exec, r.sourceAccountID)
	if err != nil {
		return resolvedExchange{}, accountSideError(SourceSide, r.sourceAccountID, err)
	}
	destination, err := resolveAccount(ctx, exec, r.destinationAccountID)
	if err != nil {
		return resolvedExchange{}, accountSideError(DestinationSide, r.destinationAccountID, err)
	}

	if source.ID == destination.ID {
		return resolvedExchange{}, fmt.Errorf(
			"%w: %q was given as both ends of it", ErrSameAccount, source.Name)
	}

	// The two accounts must NOT share a currency. This is the mirror image of
	// the rule a transfer enforces, and the pair of them is what keeps the two
	// operations from collapsing into one: money moving between two accounts in
	// one currency is a transfer and its movements cancel out; money moving
	// between two currencies is an exchange and they do not.
	if source.CurrencyCode == destination.CurrencyCode {
		return resolvedExchange{}, fmt.Errorf(
			"%w: %q and %q are both in %s, so this is a transfer rather than an exchange",
			ErrSameCurrency, source.Name, destination.Name, source.CurrencyCode)
	}

	// Each currency supplies the scale ITS OWN amount is read at, which is why
	// both are loaded before either amount is parsed. There is no shared scale
	// here and there must not be one: reading a bolívar amount at a dollar's
	// scale would be the quietest possible way to be wrong.
	//
	// An archived currency does NOT block an exchange, for the same reason it
	// does not block any other movement: retiring a currency stops it
	// denominating new accounts, it does not freeze the accounts that already
	// use it.
	currencies := currency.NewRepository(exec)

	sourceCurrency, err := currencies.Get(ctx, source.CurrencyCode)
	if err != nil {
		return resolvedExchange{}, fmt.Errorf(
			"looking up currency %q of account %d: %w", source.CurrencyCode, source.ID, err)
	}
	destinationCurrency, err := currencies.Get(ctx, destination.CurrencyCode)
	if err != nil {
		return resolvedExchange{}, fmt.Errorf(
			"looking up currency %q of account %d: %w", destination.CurrencyCode, destination.ID, err)
	}

	sourceAmountMinor, err := exchangeAmount(SourceSide, r.sourceAmount, sourceCurrency.MinorUnit)
	if err != nil {
		return resolvedExchange{}, err
	}
	destinationAmountMinor, err := exchangeAmount(DestinationSide, r.destinationAmount, destinationCurrency.MinorUnit)
	if err != nil {
		return resolvedExchange{}, err
	}

	// The two magnitudes are deliberately NOT compared with each other. They are
	// quantities of two different currencies, and the only relation between them
	// is the rate, which is whatever the user actually traded at.

	effectiveDate, err := validateEffectiveDate(r.effectiveDate)
	if err != nil {
		return resolvedExchange{}, err
	}

	return resolvedExchange{
		source:                 source,
		sourceCurrency:         sourceCurrency,
		sourceAmountMinor:      sourceAmountMinor,
		destination:            destination,
		destinationCurrency:    destinationCurrency,
		destinationAmountMinor: destinationAmountMinor,
		effectiveDate:          effectiveDate,
		description:            description,
	}, nil
}

// exchangeAmount reads one side's amount at its own currency's scale and refuses
// anything that is not strictly positive.
//
// Zero is refused on both sides: an exchange of nothing for something, or of
// something for nothing, is not an exchange, and account_movements refuses a
// zero amount anyway. A negative is refused rather than reinterpreted, because
// the direction is which account was named.
func exchangeAmount(side MovementSide, amount string, minorUnit int) (int64, error) {
	minor, err := money.Parse(amount, minorUnit)
	if err != nil {
		// Translated so that a handler never has to import money to interpret
		// what the ledger returned.
		return 0, fmt.Errorf("%w: the %s amount %s", ErrInvalidAmount, side, unwrapAmountMessage(err))
	}
	if minor <= 0 {
		return 0, fmt.Errorf(
			"%w: the %s amount must be greater than zero; the direction is which accounts were named, not the sign",
			ErrInvalidAmount, side)
	}
	return minor, nil
}

// exchangeSide is one half of an exchange: a movement, the account it lands in,
// the signed amount, and the currency that amount is in. The name, symbol and
// scale are filled in only by the read path, which has joined them.
type exchangeSide struct {
	movementID     int64
	accountID      int64
	accountName    string
	amountMinor    int64
	currencyCode   string
	currencySymbol *string
	minorUnit      int
}

// checkExchangeMovementCount refuses an exchange that does not have exactly two
// movements.
func checkExchangeMovementCount(transactionID int64, count int) error {
	if count != 2 {
		return fmt.Errorf("transaction %d: %w: it has %d movements, and an exchange has exactly 2",
			transactionID, ErrInconsistentExchange, count)
	}
	return nil
}

// splitExchangeBySign decides which of an exchange's two movements is the source
// and which is the destination, and refuses any pair that is not an exchange.
//
// This is the one implementation of what a stored exchange must look like, used
// by the read path when it assembles an ExchangeDetail and by UpdateExchange
// before it rewrites anything. Two copies of these rules would eventually
// disagree, and the disagreement would be invisible until a balance was wrong.
//
// The rules it applies are the two that depend only on the movements: exactly
// one negative and one positive, in two different accounts. What is NOT a rule
// is any relation between the two magnitudes — that is the single difference
// from splitTransfer, and it is the whole difference between the two operations.
//
// The third rule, that the two currencies differ, is checked by
// exchangeDetailFrom rather than here. Only the read path has joined the
// currencies; UpdateExchange calls this with the movement rows alone, because
// all it needs from them is which one is the source.
//
// They are checked in Go rather than in SQLite for the reason set out in TD-044:
// every one of them spans two rows, and a CHECK constraint sees one.
//
// Reaching an error here means the stored data is already wrong, which no input
// can cause. It is reported rather than repaired: rendering half an exchange as
// though it were whole would be a ledger lying quietly.
func splitExchangeBySign(transactionID int64, a, b exchangeSide) (source, destination exchangeSide, err error) {
	// amount_minor can never be zero — the column refuses it — so exactly one of
	// these three branches is right.
	switch {
	case a.amountMinor < 0 && b.amountMinor > 0:
		source, destination = a, b
	case a.amountMinor > 0 && b.amountMinor < 0:
		source, destination = b, a
	default:
		return exchangeSide{}, exchangeSide{}, fmt.Errorf(
			"transaction %d: %w: its movements are %d and %d, and an exchange has one negative and one positive",
			transactionID, ErrInconsistentExchange, a.amountMinor, b.amountMinor)
	}

	if source.accountID == destination.accountID {
		return exchangeSide{}, exchangeSide{}, fmt.Errorf(
			"transaction %d: %w: both of its movements land in account %d",
			transactionID, ErrInconsistentExchange, source.accountID)
	}

	return source, destination, nil
}

// exchangeDetailFrom assembles the read model from the two sides, deriving the
// execution rate.
//
// It is the read path's half of splitExchangeBySign, and it carries the one
// stored-shape rule that needs the currencies: they must differ. Two movements
// of unequal magnitude in ONE currency would be money created or destroyed
// inside that currency, and it is the condition most worth refusing to render.
func exchangeDetailFrom(transactionID int64, source, destination exchangeSide) (*ExchangeDetail, error) {
	if source.currencyCode == destination.currencyCode {
		return nil, fmt.Errorf(
			"transaction %d: %w: both of its movements are in %s, which is the shape of a transfer",
			transactionID, ErrInconsistentExchange, source.currencyCode)
	}

	delivered := -source.amountMinor
	if delivered <= 0 {
		// Unreachable: splitExchangeBySign has established the sign. Handled
		// rather than assumed, because the alternative is a negative "amount
		// delivered" travelling into a template.
		return nil, fmt.Errorf(
			"transaction %d: %w: its source movement is %d",
			transactionID, ErrInconsistentExchange, source.amountMinor)
	}

	rate, err := executionRate(
		source.currencyCode, source.minorUnit, delivered,
		destination.currencyCode, destination.minorUnit, destination.amountMinor)
	if err != nil {
		return nil, fmt.Errorf("deriving the execution rate of transaction %d: %w", transactionID, err)
	}

	return &ExchangeDetail{
		SourceMovementID:      source.movementID,
		SourceAccountID:       source.accountID,
		SourceAccountName:     source.accountName,
		SourceCurrencyCode:    source.currencyCode,
		SourceCurrencySymbol:  source.currencySymbol,
		SourceMinorUnit:       source.minorUnit,
		SourceAmountMinor:     delivered,
		SourceAmountFormatted: money.Format(delivered, source.minorUnit),

		DestinationMovementID:      destination.movementID,
		DestinationAccountID:       destination.accountID,
		DestinationAccountName:     destination.accountName,
		DestinationCurrencyCode:    destination.currencyCode,
		DestinationCurrencySymbol:  destination.currencySymbol,
		DestinationMinorUnit:       destination.minorUnit,
		DestinationAmountMinor:     destination.amountMinor,
		DestinationAmountFormatted: money.Format(destination.amountMinor, destination.minorUnit),

		ExecutionRate: rate,
	}, nil
}

// executionRate derives what one unit of one currency cost in the other.
//
// Both amounts are positive minor units in their own currencies. The value of
// each side is amount / 10^minorUnit, and the rate is one divided by the other —
// an exact rational, computed with math/big and never with a float:
//
//	84,220.67 VES  for  100.00 USD   ->   1 USD = 842.2067 VES
//	85,000.00 VES  for  100.00 USD   ->   1 USD = 850 VES
//
// Which side is the base follows the rule documented on ExecutionRate: USD if
// exactly one side is USD, otherwise whichever orientation makes the rate at
// least 1, with the destination breaking an exact tie. That choice is made here
// rather than in a template because it requires comparing two exact fractions,
// and nothing above this package is allowed to do arithmetic on money.
func executionRate(
	sourceCode string, sourceMinorUnit int, sourceAmountMinor int64,
	destinationCode string, destinationMinorUnit int, destinationAmountMinor int64,
) (ExecutionRate, error) {
	if sourceAmountMinor <= 0 || destinationAmountMinor <= 0 {
		return ExecutionRate{}, fmt.Errorf(
			"%w: both amounts must be greater than zero, got %d and %d",
			ErrInvalidAmount, sourceAmountMinor, destinationAmountMinor)
	}
	if sourceCode == destinationCode {
		return ExecutionRate{}, fmt.Errorf(
			"%w: both sides are in %s", ErrSameCurrency, sourceCode)
	}

	sourceValue, err := exactValue(sourceAmountMinor, sourceMinorUnit)
	if err != nil {
		return ExecutionRate{}, err
	}
	destinationValue, err := exactValue(destinationAmountMinor, destinationMinorUnit)
	if err != nil {
		return ExecutionRate{}, err
	}

	// perDestination is "1 destination = X source"; perSource is its inverse.
	// Both are exact fractions; neither is materialised as a decimal until the
	// orientation has been chosen.
	perDestination := new(big.Rat).Quo(sourceValue, destinationValue)
	perSource := new(big.Rat).Quo(destinationValue, sourceValue)

	base, quote := destinationCode, sourceCode
	chosen := perDestination

	switch {
	case sourceCode == usdCode:
		base, quote, chosen = sourceCode, destinationCode, perSource
	case destinationCode == usdCode:
		// Already the default orientation; spelled out so the rule reads as one
		// list rather than as a default plus exceptions.
		base, quote, chosen = destinationCode, sourceCode, perDestination
	case perDestination.Cmp(oneRat) < 0:
		base, quote, chosen = sourceCode, destinationCode, perSource
	}

	value, exact, err := money.NewDecimalFromRat(chosen, money.MaxScale)
	if err != nil {
		return ExecutionRate{}, err
	}

	return ExecutionRate{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		Rate:              value,
		RateFormatted:     value.String(),
		Exact:             exact,
	}, nil
}

// usdCode is the currency an exchange is quoted in whenever it is one of the two
// sides.
//
// It is a presentation rule with a financial reason: in Venezuela every rate is
// spoken as bolívares per dollar, in both directions of trade, so quoting a sale
// as "1 VES = 0.00118 USD" would be technically correct and unreadable. It is
// the only currency code named anywhere in this package, and it names no
// behaviour — an exchange between two currencies neither of which is USD is
// recorded, valued and edited identically.
const usdCode = "USD"

// oneRat is the threshold the orientation rule compares against. It is a
// package-level value rather than a local one so that no caller is tempted to
// mutate it: math/big's API is in-place, and every use here is read-only.
var oneRat = big.NewRat(1, 1)

// exactValue turns minor units into the exact decimal value they represent.
func exactValue(amountMinor int64, minorUnit int) (*big.Rat, error) {
	if minorUnit < money.MinMinorUnit || minorUnit > money.MaxMinorUnit {
		return nil, fmt.Errorf("%w: minor unit %d is outside the supported range %d-%d",
			ErrInvalidAmount, minorUnit, money.MinMinorUnit, money.MaxMinorUnit)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(minorUnit)), nil)
	return new(big.Rat).SetFrac(big.NewInt(amountMinor), scale), nil
}
