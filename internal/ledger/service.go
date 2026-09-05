package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
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
	method, err := validatePaymentMethod(in.PaymentMethod)
	if err != nil {
		return TransactionView{}, err
	}

	view, err := s.record(ctx, recordRequest{
		txType:        Income,
		accountID:     in.AccountID,
		amount:        in.Amount,
		categoryID:    &in.CategoryID,
		paymentMethod: &method,
		description:   in.Description,
		effectiveDate: in.EffectiveDate,
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
	method, err := validatePaymentMethod(in.PaymentMethod)
	if err != nil {
		return TransactionView{}, err
	}

	view, err := s.record(ctx, recordRequest{
		txType:        Expense,
		accountID:     in.AccountID,
		amount:        in.Amount,
		categoryID:    &in.CategoryID,
		paymentMethod: &method,
		description:   in.Description,
		effectiveDate: in.EffectiveDate,
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
	txType        TransactionType
	accountID     int64
	amount        string
	categoryID    *int64
	paymentMethod *PaymentMethod
	description   *string
	effectiveDate string
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
	r.description = normalizeDescription(r.description)
	if err := validateDescription(r.description); err != nil {
		return nil, err
	}

	accounts := account.NewRepository(exec)
	currencies := currency.NewRepository(exec)
	repo := NewRepository(exec)

	acct, err := accounts.Get(ctx, r.accountID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return nil, fmt.Errorf("account %d: %w", r.accountID, ErrAccountNotFound)
	case err != nil:
		return nil, fmt.Errorf("looking up account %d: %w", r.accountID, err)
	}
	if !acct.IsActive {
		// Decided here, on the field. Being archived is a fact about the
		// account, not an error the account package raises.
		return nil, fmt.Errorf("account %q: %w", acct.Name, ErrAccountInactive)
	}

	// The currency is the account's, always. It supplies the scale the amount is
	// read at, which is why it is loaded before the amount is parsed.
	//
	// An archived currency does NOT block this. Retiring a currency stops it
	// denominating new accounts; it does not freeze the accounts that already
	// use it, and money keeps moving in them.
	cur, err := currencies.Get(ctx, acct.CurrencyCode)
	if err != nil {
		return nil, fmt.Errorf("looking up currency %q of account %d: %w", acct.CurrencyCode, acct.ID, err)
	}

	amountMinor, err := amountFor(r.txType, r.amount, cur.MinorUnit)
	if err != nil {
		return nil, err
	}
	if amountMinor == 0 {
		// Only an opening balance reaches here; amountFor rejects zero for the
		// other two. Nothing is written and nothing is an error: a balance of
		// zero is what an account with no movements already has.
		//
		// This returns BEFORE the effective date is examined, deliberately. A
		// date is a property of a transaction, and there is no transaction: an
		// account opened with a zero balance should not have to supply the date
		// of an event that never gets recorded.
		return nil, nil
	}

	// Validated here rather than at the top so that the decision above does not
	// depend on a field the caller had no reason to fill in.
	effectiveDate, err := validateEffectiveDate(r.effectiveDate)
	if err != nil {
		return nil, err
	}

	if err := checkCategory(ctx, exec, r); err != nil {
		return nil, err
	}

	now := database.Now()

	transaction, err := repo.InsertTransaction(ctx, Transaction{
		Type:          r.txType,
		CategoryID:    r.categoryID,
		PaymentMethod: r.paymentMethod,
		Description:   r.description,
		EffectiveDate: effectiveDate,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return nil, err
	}

	if _, err := repo.InsertMovement(ctx, Movement{
		TransactionID: transaction.ID,
		AccountID:     acct.ID,
		AmountMinor:   amountMinor,
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
