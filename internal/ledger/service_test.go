package ledger_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

const today = "2026-09-05"

func ptr(s string) *string { return &s }

func checking() *account.Subtype { s := account.Checking; return &s }

// world is a database with the pieces every ledger test needs: services, one
// USD bank account, and one category of each type.
type world struct {
	db         *sql.DB
	ledger     *ledger.Service
	accounts   *account.Service
	categories *category.Service
	currencies *currency.Service

	usdAccount int64
	salary     int64 // INCOME
	food       int64 // EXPENSE
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db := dbtest.New(t)
	w := &world{
		db:         db,
		ledger:     ledger.NewService(db),
		accounts:   account.NewService(db),
		categories: category.NewService(db),
		currencies: currency.NewService(db),
	}

	w.usdAccount = w.newAccount(t, "Mercantil USD", "USD")
	w.salary = w.newCategory(t, "Salario", category.Income)
	w.food = w.newCategory(t, "Comida", category.Expense)
	return w
}

func (w *world) newAccount(t *testing.T, name, currencyCode string) int64 {
	t.Helper()

	created, err := w.accounts.Create(t.Context(), account.CreateInput{
		Name: name, Type: account.Bank, Subtype: checking(),
		CurrencyCode: currencyCode, Institution: ptr("Mercantil"),
	})
	if err != nil {
		t.Fatalf("creating account %q: %v", name, err)
	}
	return created.ID
}

func (w *world) newCategory(t *testing.T, name string, categoryType category.Type) int64 {
	t.Helper()

	created, err := w.categories.Create(t.Context(), category.CreateInput{Name: name, Type: categoryType})
	if err != nil {
		t.Fatalf("creating category %q: %v", name, err)
	}
	return created.ID
}

// counts returns the number of rows in each ledger table, which is how these
// tests tell "nothing was written" from "something was".
func (w *world) counts(t *testing.T) (transactions, movements int) {
	t.Helper()

	if err := w.db.QueryRowContext(t.Context(), "SELECT count(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatalf("counting transactions: %v", err)
	}
	if err := w.db.QueryRowContext(t.Context(), "SELECT count(*) FROM account_movements").Scan(&movements); err != nil {
		t.Fatalf("counting movements: %v", err)
	}
	return transactions, movements
}

// assertNoOrphanTransactions checks the invariant that makes the two-table
// design safe: a financial event with no impact on any account must not exist.
//
// A mid-write failure is not simulated with a fault injector — there is no
// reachable path between the two inserts that can fail, because everything that
// can be rejected is rejected before either of them. What is asserted instead
// is the outcome that would betray a broken transaction: an event with nothing
// attached to it.
func (w *world) assertNoOrphanTransactions(t *testing.T) {
	t.Helper()

	var orphans int
	err := w.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM transactions t
		 WHERE NOT EXISTS (SELECT 1 FROM account_movements m WHERE m.transaction_id = t.id)`).Scan(&orphans)
	if err != nil {
		t.Fatalf("looking for orphan transactions: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d transaction(s) exist with no account movement", orphans)
	}
}

// --- recording --------------------------------------------------------------

func TestRecordIncomeStoresAPositiveMovement(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	view, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID:     w.usdAccount,
		CategoryID:    w.salary,
		Amount:        "1500.50",
		PaymentMethod: ledger.BankTransfer,
		Description:   ptr("Sueldo de septiembre"),
		EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}

	if view.AmountMinor != 150050 {
		t.Errorf("AmountMinor = %d, want 150050", view.AmountMinor)
	}
	if view.AmountFormatted != "1500.50" {
		t.Errorf("AmountFormatted = %q, want %q", view.AmountFormatted, "1500.50")
	}
	if view.Type != ledger.Income {
		t.Errorf("Type = %q, want %q", view.Type, ledger.Income)
	}
	if view.PaymentMethod == nil || *view.PaymentMethod != ledger.BankTransfer {
		t.Errorf("PaymentMethod = %v, want %q", view.PaymentMethod, ledger.BankTransfer)
	}
	if view.CategoryID == nil || *view.CategoryID != w.salary {
		t.Errorf("CategoryID = %v, want %d", view.CategoryID, w.salary)
	}
	if view.CategoryName == nil || *view.CategoryName != "Salario" {
		t.Errorf("CategoryName = %v, want %q", view.CategoryName, "Salario")
	}
	if view.Description == nil || *view.Description != "Sueldo de septiembre" {
		t.Errorf("Description = %v", view.Description)
	}
	if got := view.EffectiveDate.Format("2006-01-02"); got != today {
		t.Errorf("EffectiveDate = %q, want %q", got, today)
	}

	transactions, movements := w.counts(t)
	if transactions != 1 || movements != 1 {
		t.Errorf("wrote %d transactions and %d movements, want 1 and 1", transactions, movements)
	}
	w.assertNoOrphanTransactions(t)
}

func TestRecordExpenseStoresANegativeMovement(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	// The caller sends a positive amount. The sign belongs to the operation.
	view, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID:     w.usdAccount,
		CategoryID:    w.food,
		Amount:        "25.99",
		PaymentMethod: ledger.POS,
		EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}

	if view.AmountMinor != -2599 {
		t.Errorf("AmountMinor = %d, want -2599", view.AmountMinor)
	}
	if view.AmountFormatted != "-25.99" {
		t.Errorf("AmountFormatted = %q, want %q", view.AmountFormatted, "-25.99")
	}
	if view.Description != nil {
		t.Errorf("Description = %q, want nil", *view.Description)
	}
	w.assertNoOrphanTransactions(t)
}

// An expense that increases a balance must be unrepresentable, not merely
// discouraged.
func TestSignedAmountsAreRejectedForIncomeAndExpenses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "-25.99",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Errorf("RecordExpense with a negative amount = %v, want ErrInvalidAmount", err)
	}

	_, err = w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "-1500",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Errorf("RecordIncome with a negative amount = %v, want ErrInvalidAmount", err)
	}

	// Zero is meaningless for both: nothing moved.
	for _, amount := range []string{"0", "0.00"} {
		_, err = w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
			AccountID: w.usdAccount, CategoryID: w.salary, Amount: amount,
			PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
		})
		if !errors.Is(err, ledger.ErrInvalidAmount) {
			t.Errorf("RecordIncome(%q) = %v, want ErrInvalidAmount", amount, err)
		}
	}

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("rejected operations wrote %d transactions and %d movements, want none", transactions, movements)
	}
}

func TestRecordRejectsInvalidAmounts(t *testing.T) {
	t.Parallel()

	amounts := map[string]string{
		"empty":               "",
		"blank":               "   ",
		"too many decimals":   "12.345",
		"thousands separator": "1,000.00",
		"currency symbol":     "$100",
		"letters":             "mucho",
		"exponent":            "1e3",
		"overflow":            "92233720368547758.08",
	}

	for name, amount := range amounts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			_, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
				AccountID: w.usdAccount, CategoryID: w.salary, Amount: amount,
				PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
			})
			if !errors.Is(err, ledger.ErrInvalidAmount) {
				t.Fatalf("RecordIncome(%q) = %v, want ErrInvalidAmount", amount, err)
			}
			// The money package's sentinel must not leak: a handler should not
			// have to import money to interpret what the ledger returned.
			if transactions, _ := w.counts(t); transactions != 0 {
				t.Errorf("a rejected amount wrote %d transactions", transactions)
			}
		})
	}
}

// USD has two decimals, so a third is money the user typed that would have to
// be discarded. Rounding it silently is not an option.
func TestOverPreciseAmountsAreRejectedRatherThanRounded(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "10.005",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Fatalf("RecordExpense with three decimals = %v, want ErrInvalidAmount", err)
	}

	balance, err := w.ledger.GetAccountBalance(t.Context(), w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 0 {
		t.Errorf("balance = %d after a rejected amount, want 0", balance.BalanceMinor)
	}
}

func TestRecordRejectsInvalidDates(t *testing.T) {
	t.Parallel()

	dates := map[string]string{
		"empty":          "",
		"blank":          "   ",
		"unpadded":       "2026-9-5",
		"impossible day": "2026-02-31",
		"month 13":       "2026-13-01",
		"a timestamp":    "2026-09-05T00:00:00.000Z",
		"slashes":        "2026/09/05",
		"words":          "yesterday",
	}

	for name, date := range dates {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			_, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
				AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
				PaymentMethod: ledger.BankTransfer, EffectiveDate: date,
			})
			if !errors.Is(err, ledger.ErrInvalidInput) {
				t.Fatalf("RecordIncome with date %q = %v, want ErrInvalidInput", date, err)
			}
		})
	}
}

// The effective date is when it happened financially, which is not when it was
// recorded.
func TestEffectiveDateIsIndependentOfCreatedAt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	view, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "40",
		PaymentMethod: ledger.POS, EffectiveDate: "2026-09-03",
	})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}

	if got := view.EffectiveDate.Format("2006-01-02"); got != "2026-09-03" {
		t.Errorf("EffectiveDate = %q, want %q", got, "2026-09-03")
	}
	if view.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
	if view.EffectiveDate.Equal(view.CreatedAt) {
		t.Error("EffectiveDate and CreatedAt are the same value; they are different facts")
	}
}

func TestRecordRejectsInvalidPaymentMethods(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, method := range []ledger.PaymentMethod{"", "ZELLE", "pos ", "TRANSFER"} {
		_, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
			AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
			PaymentMethod: method, EffectiveDate: today,
		})
		// " pos " normalises to POS and is accepted; the rest are not.
		if method == "pos " {
			if err != nil {
				t.Errorf("payment method %q should normalise: %v", method, err)
			}
			continue
		}
		if !errors.Is(err, ledger.ErrInvalidInput) {
			t.Errorf("payment method %q = %v, want ErrInvalidInput", method, err)
		}
	}
}

func TestEveryPaymentMethodWorksForBothDirections(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	// Money can arrive by transfer or in cash just as it can leave that way.
	for _, method := range ledger.PaymentMethods() {
		if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
			AccountID: w.usdAccount, CategoryID: w.salary, Amount: "10",
			PaymentMethod: method, EffectiveDate: today,
		}); err != nil {
			t.Errorf("income by %s was rejected: %v", method, err)
		}
		if _, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
			AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
			PaymentMethod: method, EffectiveDate: today,
		}); err != nil {
			t.Errorf("expense by %s was rejected: %v", method, err)
		}
	}
}

func TestDescriptionMustBeAbsentRatherThanEmpty(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, description := range []string{"", "   "} {
		_, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
			AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
			PaymentMethod: ledger.POS, Description: ptr(description), EffectiveDate: today,
		})
		if !errors.Is(err, ledger.ErrInvalidInput) {
			t.Errorf("description %q = %v, want ErrInvalidInput", description, err)
		}
	}

	_, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
		PaymentMethod: ledger.POS, Description: ptr(strings.Repeat("x", 256)), EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("an overlong description = %v, want ErrInvalidInput", err)
	}

	// Absent is nil, and it survives the round trip as NULL.
	view, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	if view.Description != nil {
		t.Errorf("Description = %q, want nil", *view.Description)
	}

	// A description that needs trimming is trimmed, not rejected.
	view, err = w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
		PaymentMethod: ledger.POS, Description: ptr("  Almuerzo  "), EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	if view.Description == nil || *view.Description != "Almuerzo" {
		t.Errorf("Description = %v, want %q", view.Description, "Almuerzo")
	}
}

// --- accounts and categories ------------------------------------------------

func TestRecordRejectsUnknownAndInactiveAccounts(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: 4242, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("an unknown account = %v, want ErrAccountNotFound", err)
	}
	// The account package's error must not leak through.
	if errors.Is(err, account.ErrNotFound) {
		t.Error("account.ErrNotFound leaked out of the ledger")
	}

	if _, err := w.accounts.SetActive(ctx, w.usdAccount, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	_, err = w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrAccountInactive) {
		t.Errorf("an archived account = %v, want ErrAccountInactive", err)
	}

	if transactions, _ := w.counts(t); transactions != 0 {
		t.Errorf("rejected operations wrote %d transactions, want 0", transactions)
	}
}

func TestRecordRejectsUnknownAndInactiveCategories(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: 4242, Amount: "100",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrCategoryNotFound) {
		t.Errorf("an unknown category = %v, want ErrCategoryNotFound", err)
	}
	if errors.Is(err, category.ErrNotFound) {
		t.Error("category.ErrNotFound leaked out of the ledger")
	}

	if _, err := w.categories.SetActive(ctx, w.food, false); err != nil {
		t.Fatalf("archiving the category: %v", err)
	}

	_, err = w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "100",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrCategoryInactive) {
		t.Errorf("an archived category = %v, want ErrCategoryInactive", err)
	}

	if transactions, _ := w.counts(t); transactions != 0 {
		t.Errorf("rejected operations wrote %d transactions, want 0", transactions)
	}
}

// An income category cannot classify an expense, and the reverse.
func TestCategoryTypeMustMatchTheTransactionType(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrCategoryTypeMismatch) {
		t.Errorf("an expense under an income category = %v, want ErrCategoryTypeMismatch", err)
	}

	_, err = w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrCategoryTypeMismatch) {
		t.Errorf("income under an expense category = %v, want ErrCategoryTypeMismatch", err)
	}

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("mismatched categories wrote %d transactions and %d movements, want none",
			transactions, movements)
	}
}

// Archiving a currency stops it denominating new accounts. It must not freeze
// the accounts already using it: money keeps moving in them.
func TestAnArchivedCurrencyDoesNotBlockMovements(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	if _, err := w.currencies.SetActive(ctx, "USD", false); err != nil {
		t.Fatalf("archiving the currency: %v", err)
	}

	if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	}); err != nil {
		t.Errorf("archiving a currency froze an existing account: %v", err)
	}
}

// --- opening balances -------------------------------------------------------

func TestRecordOpeningBalance(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	view, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}
	if view == nil {
		t.Fatal("a non-zero opening balance recorded nothing")
	}

	if view.AmountMinor != 100000 {
		t.Errorf("AmountMinor = %d, want 100000", view.AmountMinor)
	}
	if view.Type != ledger.OpeningBalance {
		t.Errorf("Type = %q, want %q", view.Type, ledger.OpeningBalance)
	}
	// An opening balance is not income and is not categorised.
	if view.CategoryID != nil {
		t.Errorf("CategoryID = %v, want nil", *view.CategoryID)
	}
	if view.PaymentMethod != nil {
		t.Errorf("PaymentMethod = %v, want nil", *view.PaymentMethod)
	}
	w.assertNoOrphanTransactions(t)
}

// Zero is accepted and records nothing: the balance of an account with no
// movements is already zero, so a zero-amount row would assert nothing while
// appearing in every listing.
func TestAZeroOpeningBalanceRecordsNothing(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, amount := range []string{"0", "0.00", "-0", "  0  "} {
		view, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
			AccountID: w.usdAccount, Amount: amount, EffectiveDate: today,
		})
		if err != nil {
			t.Errorf("RecordOpeningBalance(%q) = %v, want no error", amount, err)
		}
		if view != nil {
			t.Errorf("RecordOpeningBalance(%q) recorded a transaction", amount)
		}
	}

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("zero opening balances wrote %d transactions and %d movements, want none",
			transactions, movements)
	}

	balance, err := w.ledger.GetAccountBalance(ctx, w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 0 {
		t.Errorf("balance = %d, want 0", balance.BalanceMinor)
	}
}

// An account can start overdrawn.
func TestANegativeOpeningBalanceIsAllowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	view, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "-250.75", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}
	if view == nil {
		t.Fatal("a negative opening balance recorded nothing")
	}
	if view.AmountMinor != -25075 {
		t.Errorf("AmountMinor = %d, want -25075", view.AmountMinor)
	}
	if view.AmountFormatted != "-250.75" {
		t.Errorf("AmountFormatted = %q, want %q", view.AmountFormatted, "-250.75")
	}
}

// --- balances ---------------------------------------------------------------

// The rule the whole design rests on: a balance is the sum of the movements,
// and there is nowhere else it could come from.
func TestBalanceIsTheSumOfMovements(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	if _, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	}); err != nil {
		t.Fatalf("opening balance: %v", err)
	}
	if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "1500.50",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	}); err != nil {
		t.Fatalf("income: %v", err)
	}
	if _, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.99",
		PaymentMethod: ledger.POS, EffectiveDate: today,
	}); err != nil {
		t.Fatalf("expense: %v", err)
	}

	// 100000 + 150050 - 2599
	const want = 247451

	balance, err := w.ledger.GetAccountBalance(ctx, w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != want {
		t.Errorf("BalanceMinor = %d, want %d", balance.BalanceMinor, want)
	}
	if balance.BalanceFormatted != "2474.51" {
		t.Errorf("BalanceFormatted = %q, want %q", balance.BalanceFormatted, "2474.51")
	}

	// And it agrees with the raw sum, so nothing above the SQL invented it.
	var raw int64
	if err := w.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(amount_minor), 0) FROM account_movements WHERE account_id = ?",
		w.usdAccount).Scan(&raw); err != nil {
		t.Fatalf("summing movements: %v", err)
	}
	if raw != want {
		t.Errorf("SUM(amount_minor) = %d, want %d", raw, want)
	}
}

func TestAnAccountWithNoMovementsHasABalanceOfZero(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	balance, err := w.ledger.GetAccountBalance(t.Context(), w.usdAccount)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 0 {
		t.Errorf("BalanceMinor = %d, want 0", balance.BalanceMinor)
	}
	// Formatted at the currency's scale, not as a bare "0".
	if balance.BalanceFormatted != "0.00" {
		t.Errorf("BalanceFormatted = %q, want %q", balance.BalanceFormatted, "0.00")
	}
	if balance.CurrencyCode != "USD" {
		t.Errorf("CurrencyCode = %q, want USD", balance.CurrencyCode)
	}

	// It must appear in the listing too: a newly opened account belongs on the
	// dashboard.
	balances, err := w.ledger.ListAccountBalances(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAccountBalances: %v", err)
	}
	if len(balances) != 1 {
		t.Errorf("ListAccountBalances returned %d accounts, want 1", len(balances))
	}
}

func TestGetAccountBalanceOfUnknownAccount(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	if _, err := w.ledger.GetAccountBalance(t.Context(), 4242); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("GetAccountBalance of an unknown account = %v, want ErrAccountNotFound", err)
	}
}

// Balances are per account and per currency, and nothing is ever converted.
func TestBalancesAreGroupedByCurrencyAndNeverConverted(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	ves := w.newAccount(t, "Mercantil VES", "VES")
	eur := w.newAccount(t, "Cuenta EUR", "EUR")

	amounts := map[int64]string{w.usdAccount: "100", ves: "5000", eur: "250"}
	for accountID, amount := range amounts {
		if _, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
			AccountID: accountID, Amount: amount, EffectiveDate: today,
		}); err != nil {
			t.Fatalf("opening balance for account %d: %v", accountID, err)
		}
	}

	balances, err := w.ledger.ListAccountBalances(ctx, false)
	if err != nil {
		t.Fatalf("ListAccountBalances: %v", err)
	}
	if len(balances) != 3 {
		t.Fatalf("ListAccountBalances returned %d accounts, want 3", len(balances))
	}

	// Ordered by currency code, then name: the dashboard groups in one pass.
	wantOrder := []string{"EUR", "USD", "VES"}
	for i, code := range wantOrder {
		if balances[i].CurrencyCode != code {
			t.Errorf("balance %d is %s, want %s", i, balances[i].CurrencyCode, code)
		}
	}

	totals, err := w.ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		t.Fatalf("ListCurrencyTotals: %v", err)
	}
	if len(totals) != 3 {
		t.Fatalf("ListCurrencyTotals returned %d currencies, want 3", len(totals))
	}

	want := map[string]struct {
		minor     int64
		formatted string
		accounts  int
	}{
		"EUR": {25000, "250.00", 1},
		"USD": {10000, "100.00", 1},
		"VES": {500000, "5000.00", 1},
	}
	for _, total := range totals {
		expected, ok := want[total.CurrencyCode]
		if !ok {
			t.Errorf("unexpected currency %s in the totals", total.CurrencyCode)
			continue
		}
		if total.TotalMinor != expected.minor {
			t.Errorf("%s TotalMinor = %d, want %d", total.CurrencyCode, total.TotalMinor, expected.minor)
		}
		if total.TotalFormatted != expected.formatted {
			t.Errorf("%s TotalFormatted = %q, want %q",
				total.CurrencyCode, total.TotalFormatted, expected.formatted)
		}
		if total.AccountCount != expected.accounts {
			t.Errorf("%s AccountCount = %d, want %d",
				total.CurrencyCode, total.AccountCount, expected.accounts)
		}
	}
}

// The account count must count accounts, not the rows a join produced.
func TestCurrencyTotalsSumSeveralAccountsAndMovements(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	second := w.newAccount(t, "Banesco USD", "USD")

	for _, accountID := range []int64{w.usdAccount, w.usdAccount, second} {
		if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
			AccountID: accountID, CategoryID: w.salary, Amount: "100",
			PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
		}); err != nil {
			t.Fatalf("income: %v", err)
		}
	}

	totals, err := w.ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		t.Fatalf("ListCurrencyTotals: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("ListCurrencyTotals returned %d currencies, want 1", len(totals))
	}
	if totals[0].AccountCount != 2 {
		t.Errorf("AccountCount = %d, want 2 (the join produced three rows)", totals[0].AccountCount)
	}
	if totals[0].TotalMinor != 30000 {
		t.Errorf("TotalMinor = %d, want 30000", totals[0].TotalMinor)
	}
}

// An archived account keeps its history and its balance; it is simply left out
// of the default listing.
func TestArchivedAccountsAreExcludedUnlessAskedFor(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	if _, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	}); err != nil {
		t.Fatalf("opening balance: %v", err)
	}
	if _, err := w.accounts.SetActive(ctx, w.usdAccount, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	active, err := w.ledger.ListAccountBalances(ctx, false)
	if err != nil {
		t.Fatalf("ListAccountBalances(false): %v", err)
	}
	if len(active) != 0 {
		t.Errorf("an archived account appeared in the default listing")
	}

	all, err := w.ledger.ListAccountBalances(ctx, true)
	if err != nil {
		t.Fatalf("ListAccountBalances(true): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListAccountBalances(true) returned %d accounts, want 1", len(all))
	}
	if all[0].BalanceMinor != 100000 {
		t.Errorf("an archived account lost its balance: %d", all[0].BalanceMinor)
	}
	if all[0].IsActive {
		t.Error("IsActive is true for an archived account")
	}
}

// --- listing ----------------------------------------------------------------

// Newest first by effective date, with the id as a tie-break so the order is
// total: several transactions routinely share a date.
func TestListTransactionsOrdering(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	dates := []string{"2026-09-01", "2026-09-05", "2026-09-03", "2026-09-05"}
	ids := make([]int64, 0, len(dates))
	for _, date := range dates {
		view, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
			AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
			PaymentMethod: ledger.POS, EffectiveDate: date,
		})
		if err != nil {
			t.Fatalf("expense on %s: %v", date, err)
		}
		ids = append(ids, view.ID)
	}

	got, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListTransactions returned %d rows, want 4", len(got))
	}

	// 09-05 (the later id first), 09-05, 09-03, 09-01.
	want := []int64{ids[3], ids[1], ids[2], ids[0]}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("row %d is transaction %d, want %d (dates: %v)", i, got[i].ID, want[i], dates)
		}
	}

	// Repeating the call must give the same order, which is what the id
	// tie-break is for.
	again, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	for i := range again {
		if again[i].ID != got[i].ID {
			t.Errorf("the order changed between calls at row %d", i)
		}
	}
}

func TestListTransactionsLimit(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for range 5 {
		if _, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
			AccountID: w.usdAccount, CategoryID: w.food, Amount: "10",
			PaymentMethod: ledger.POS, EffectiveDate: today,
		}); err != nil {
			t.Fatalf("expense: %v", err)
		}
	}

	tests := map[string]struct {
		limit int
		want  int
	}{
		"explicit":                {2, 2},
		"zero is the default":     {0, 5},
		"negative is the default": {-1, 5},
		"above the total":         {100, 5},
		"above the cap":           {10000, 5},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{Limit: tc.limit})
			if err != nil {
				t.Fatalf("ListTransactions: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("Limit %d returned %d rows, want %d", tc.limit, len(got), tc.want)
			}
		})
	}
}

// The listing carries everything a table row needs, resolved in one query.
func TestListTransactionsCarriesTheWholeRow(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	if _, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.99",
		PaymentMethod: ledger.POS, Description: ptr("Almuerzo"), EffectiveDate: today,
	}); err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}

	got, err := w.ledger.ListTransactions(t.Context(), ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListTransactions returned %d rows, want 1", len(got))
	}
	row := got[0]

	checks := []struct {
		field string
		ok    bool
	}{
		{"AccountName", row.AccountName == "Mercantil USD"},
		{"AccountType", row.AccountType == account.Bank},
		{"AccountSubtype", row.AccountSubtype != nil && *row.AccountSubtype == account.Checking},
		{"Institution", row.Institution != nil && *row.Institution == "Mercantil"},
		{"CurrencyCode", row.CurrencyCode == "USD"},
		{"CurrencySymbol", row.CurrencySymbol != nil && *row.CurrencySymbol == "$"},
		{"MinorUnit", row.MinorUnit == 2},
		{"CategoryName", row.CategoryName != nil && *row.CategoryName == "Comida"},
		{"CategoryType", row.CategoryType != nil && *row.CategoryType == category.Expense},
		{"PaymentMethod", row.PaymentMethod != nil && *row.PaymentMethod == ledger.POS},
		{"Description", row.Description != nil && *row.Description == "Almuerzo"},
		{"AmountMinor", row.AmountMinor == -2599},
		{"AmountFormatted", row.AmountFormatted == "-25.99"},
		{"MovementID", row.MovementID != 0},
	}
	for _, check := range checks {
		if !check.ok {
			t.Errorf("%s is wrong in the listing row: %+v", check.field, row)
		}
	}
}

// The currency is the account's, always. There is nowhere in the input to say
// otherwise, and this asserts the value that comes back proves it.
func TestCurrencyIsDerivedFromTheAccount(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	ves := w.newAccount(t, "Mercantil VES", "VES")

	usdView, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("income into USD: %v", err)
	}
	vesView, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: ves, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("income into VES: %v", err)
	}

	if usdView.CurrencyCode != "USD" {
		t.Errorf("USD account produced a %s movement", usdView.CurrencyCode)
	}
	if vesView.CurrencyCode != "VES" {
		t.Errorf("VES account produced a %s movement", vesView.CurrencyCode)
	}

	// Neither table has a currency column that could disagree with the account.
	for _, table := range []string{"transactions", "account_movements"} {
		rows, err := w.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatalf("reading columns of %s: %v", table, err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scanning column name: %v", err)
			}
			if strings.Contains(name, "currency") {
				t.Errorf("%s has a %q column; it could disagree with the account", table, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterating columns: %v", err)
		}
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}
}

func TestGetTransaction(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	created, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}

	fetched, err := w.ledger.GetTransaction(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if fetched.ID != created.ID || fetched.AmountMinor != created.AmountMinor {
		t.Errorf("GetTransaction returned %+v, want %+v", fetched, created)
	}

	if _, err := w.ledger.GetTransaction(t.Context(), 4242); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("GetTransaction of an unknown id = %v, want ErrNotFound", err)
	}
}

// A cancelled context must stop the operation rather than let it run.
func TestOperationsRespectContextCancellation(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{}); err == nil {
		t.Error("ListTransactions succeeded with a cancelled context")
	}
	if _, err := w.ledger.ListAccountBalances(ctx, false); err == nil {
		t.Error("ListAccountBalances succeeded with a cancelled context")
	}
	if _, err := w.ledger.ListCurrencyTotals(ctx, false); err == nil {
		t.Error("ListCurrencyTotals succeeded with a cancelled context")
	}
	if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "100",
		PaymentMethod: ledger.BankTransfer, EffectiveDate: today,
	}); err == nil {
		t.Error("RecordIncome succeeded with a cancelled context")
	}
}

// A zero opening balance records nothing, so it must not demand the date of an
// event that never happens. The caller had no reason to fill that field in.
func TestAZeroOpeningBalanceNeedsNoDate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	view, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "0", EffectiveDate: "",
	})
	if err != nil {
		t.Fatalf("a zero opening balance with no date = %v, want no error", err)
	}
	if view != nil {
		t.Error("a zero opening balance recorded a transaction")
	}

	// A non-zero one still demands it.
	if _, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: "",
	}); !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("a real opening balance with no date = %v, want ErrInvalidInput", err)
	}

	// The account is still checked first: a zero amount is not a way to skip
	// validation of everything else.
	if _, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: 4242, Amount: "0", EffectiveDate: "",
	}); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("a zero opening balance on an unknown account = %v, want ErrAccountNotFound", err)
	}
}
