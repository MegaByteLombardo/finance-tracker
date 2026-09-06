package adapter_test

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
	"github.com/MegaByteLombardo/finance-tracker/internal/web/adapter"
)

func newTestServices(t *testing.T, db *sql.DB) *web.Services {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	backups, err := backup.NewService(db, t.TempDir(), 5, logger)
	if err != nil {
		t.Fatalf("creating backup service: %v", err)
	}
	return adapter.New(db, backups)
}

// 1. Compile-time assertions for all consumer interfaces
func TestCompilationAssertions(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svcs := newTestServices(t, db)

	var _ web.CurrencyService = svcs.Currencies
	var _ web.AccountService = svcs.Accounts
	var _ web.CategoryService = svcs.Categories
	var _ web.PaymentMethodService = svcs.Methods
	var _ web.LedgerService = svcs.Ledger
	var _ web.BackupService = svcs.Backups
	var _ web.ApplicationService = svcs.Application
}

// 2. Error translation tests on real SQLite
func TestErrorTranslations(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := dbtest.New(t)
	svcs := newTestServices(t, db)

	assertError := func(t *testing.T, err error, targetSentinel error, expectedSubstring string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !errors.Is(err, targetSentinel) {
			t.Errorf("errors.Is(err, %v) = false, got: %v", targetSentinel, err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(expectedSubstring)) {
			t.Errorf("expected error message to contain %q, got: %q", expectedSubstring, err.Error())
		}
	}

	// account.ErrNotFound -> web.ErrNotFound
	_, err := svcs.Accounts.Get(ctx, 999999)
	assertError(t, err, web.ErrNotFound, "account not found")

	// account.ErrInvalidInput -> web.ErrInvalidInput (BANK without subtype)
	_, err = svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name:         "Bank No Subtype",
		Type:         web.AccountTypeBank,
		Subtype:      nil,
		CurrencyCode: "USD",
	})
	assertError(t, err, web.ErrInvalidInput, "subtype")

	// account.ErrCurrencyNotFound -> web.ErrCurrencyNotFound
	stChecking := web.AccountSubtypeChecking
	_, err = svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name:         "Bad Currency",
		Type:         web.AccountTypeBank,
		Subtype:      &stChecking,
		CurrencyCode: "XYZ",
	})
	assertError(t, err, web.ErrCurrencyNotFound, "currency")

	// account.ErrCurrencyInactive -> web.ErrCurrencyInactive
	// Archive EUR
	rawCurrSvc := currency.NewService(db)
	_, err = rawCurrSvc.SetActive(ctx, "EUR", false)
	if err != nil {
		t.Fatalf("archiving EUR: %v", err)
	}
	_, err = svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name:         "EUR Account",
		Type:         web.AccountTypeBank,
		Subtype:      &stChecking,
		CurrencyCode: "EUR",
	})
	assertError(t, err, web.ErrCurrencyInactive, "inactive")

	// category.ErrNotFound -> web.ErrNotFound
	_, err = svcs.Categories.Get(ctx, 999999)
	assertError(t, err, web.ErrNotFound, "category not found")

	// category.ErrInvalidInput -> web.ErrInvalidInput
	_, err = svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "",
		Type: web.CategoryTypeIncome,
	})
	assertError(t, err, web.ErrInvalidInput, "blank")

	// category.ErrConflict -> web.ErrConflict
	_, err = svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Food",
		Type: web.CategoryTypeExpense,
	})
	if err != nil {
		t.Fatalf("creating food category: %v", err)
	}
	_, err = svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "food", // case-insensitive match
		Type: web.CategoryTypeExpense,
	})
	assertError(t, err, web.ErrConflict, "already exists")

	// account.ErrConflict -> web.ErrConflict (reserved in M2 domain, test mapping)
	assertError(t, adapter.TranslateAccountError(account.ErrConflict), web.ErrConflict, "already exists")

	// currency.ErrNotFound -> web.ErrNotFound (3-letter unknown code)
	_, err = svcs.Currencies.Get(ctx, "ZZZ")
	assertError(t, err, web.ErrNotFound, "currency not found")

	// currency.ErrInvalidInput -> web.ErrInvalidInput (code too long)
	_, err = svcs.Currencies.Get(ctx, "TOOLONG")
	assertError(t, err, web.ErrInvalidInput, "must be exactly 3 letters")

	// currency.ErrConflict -> web.ErrConflict (test mapping)
	assertError(t, adapter.TranslateCurrencyError(currency.ErrConflict), web.ErrConflict, "already exists")

	// Setup valid account and category for ledger tests
	acctUSD, err := svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name:         "Ledger Test USD",
		Type:         web.AccountTypeBank,
		Subtype:      &stChecking,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating test account: %v", err)
	}

	catSalary, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Salary",
		Type: web.CategoryTypeIncome,
	})
	if err != nil {
		t.Fatalf("creating salary category: %v", err)
	}

	catGroceries, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Groceries",
		Type: web.CategoryTypeExpense,
	})
	if err != nil {
		t.Fatalf("creating groceries category: %v", err)
	}

	// ledger.ErrNotFound -> web.ErrNotFound
	_, err = svcs.Ledger.GetTransaction(ctx, 999999)
	assertError(t, err, web.ErrNotFound, "transaction not found")

	// ledger.ErrInvalidInput -> web.ErrInvalidInput (invalid date)
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      catSalary.ID,
		Amount:          "100.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-02-31", // impossible date
	})
	assertError(t, err, web.ErrInvalidInput, "date")

	// ledger.ErrInvalidAmount -> web.ErrInvalidAmount (negative on income)
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      catSalary.ID,
		Amount:          "-50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrInvalidAmount, "amount")

	// ledger.ErrAccountNotFound -> web.ErrAccountNotFound
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       999999,
		CategoryID:      catSalary.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrAccountNotFound, "account not found")

	// ledger.ErrAccountInactive -> web.ErrAccountInactive
	_, err = svcs.Accounts.SetActive(ctx, acctUSD.ID, false)
	if err != nil {
		t.Fatalf("archiving account: %v", err)
	}
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      catSalary.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrAccountInactive, "inactive")

	// Restore account
	_, _ = svcs.Accounts.SetActive(ctx, acctUSD.ID, true)

	// ledger.ErrCategoryNotFound -> web.ErrCategoryNotFound
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      999999,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrCategoryNotFound, "category not found")

	// ledger.ErrCategoryInactive -> web.ErrCategoryInactive
	_, err = svcs.Categories.SetActive(ctx, catSalary.ID, false)
	if err != nil {
		t.Fatalf("archiving category: %v", err)
	}
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      catSalary.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrCategoryInactive, "inactive")

	// Restore category
	_, _ = svcs.Categories.SetActive(ctx, catSalary.ID, true)

	// ledger.ErrCategoryTypeMismatch -> web.ErrCategoryTypeMismatch
	// Filing income with expense category (Groceries)
	_, err = svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctUSD.ID,
		CategoryID:      catGroceries.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-05",
	})
	assertError(t, err, web.ErrCategoryTypeMismatch, "match")
}

// 3. End-to-end flow over real SQLite
func TestEndToEndFlowOnRealDB(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := dbtest.New(t)
	svcs := newTestServices(t, db)

	// 1. Create categories
	catSalary, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Salary",
		Type: web.CategoryTypeIncome,
	})
	if err != nil {
		t.Fatalf("creating salary category: %v", err)
	}

	catDinner, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Dinner",
		Type: web.CategoryTypeExpense,
	})
	if err != nil {
		t.Fatalf("creating dinner category: %v", err)
	}

	// 2. Open Account with initial balance via ApplicationService.OpenAccount
	inst := "Mercantil"
	subtype := web.AccountSubtypeChecking
	initialBal := "1000.00"
	today := time.Now().UTC().Format("2006-01-02")

	openRes, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{
			Name:         "Mercantil USD",
			Type:         web.AccountTypeBank,
			Subtype:      &subtype,
			CurrencyCode: "USD",
			Institution:  &inst,
		},
		OpeningBalance:     &initialBal,
		OpeningBalanceDate: today,
	})
	if err != nil {
		t.Fatalf("opening account: %v", err)
	}

	if openRes.OpeningBalance == nil {
		t.Fatalf("expected non-nil opening balance for 1000.00")
	}
	if openRes.OpeningBalance.AmountFormatted != "1000.00" {
		t.Errorf("opening balance = %q, want 1000.00", openRes.OpeningBalance.AmountFormatted)
	}

	acctID := openRes.Account.ID

	// 3. Record income +$500
	descInc := "Biweekly paycheck"
	txInc, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID:       acctID,
		CategoryID:      catSalary.ID,
		Amount:          "500.00",
		PaymentMethodID: 1,
		Description:     &descInc,
		EffectiveDate:   today,
	})
	if err != nil {
		t.Fatalf("recording income: %v", err)
	}
	if txInc.AmountFormatted != "500.00" {
		t.Errorf("income amount = %q, want 500.00", txInc.AmountFormatted)
	}

	// 4. Record expense -$25
	descExp := "Restaurant"
	txExp, err := svcs.Ledger.RecordExpense(ctx, web.RecordExpenseInput{
		AccountID:       acctID,
		CategoryID:      catDinner.ID,
		Amount:          "25.00",
		PaymentMethodID: 2,
		Description:     &descExp,
		EffectiveDate:   today,
	})
	if err != nil {
		t.Fatalf("recording expense: %v", err)
	}
	if txExp.AmountFormatted != "-25.00" {
		t.Errorf("expense amount = %q, want -25.00", txExp.AmountFormatted)
	}

	// 5. Check account balance: 1000 + 500 - 25 = 1475.00
	bal, err := svcs.Ledger.GetAccountBalance(ctx, acctID)
	if err != nil {
		t.Fatalf("getting account balance: %v", err)
	}
	if bal.BalanceMinor != 147500 {
		t.Errorf("balance minor = %d, want 147500", bal.BalanceMinor)
	}
	if bal.BalanceFormatted != "1475.00" {
		t.Errorf("balance formatted = %q, want 1475.00", bal.BalanceFormatted)
	}

	// 6. Check currency totals
	totals, err := svcs.Ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		t.Fatalf("listing currency totals: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("expected 1 currency total, got %d", len(totals))
	}
	if totals[0].CurrencyCode != "USD" || totals[0].TotalFormatted != "1475.00" {
		t.Errorf("unexpected total: %+v", totals[0])
	}

	// 7. Check list transactions ordering
	txs, err := svcs.Ledger.ListTransactions(ctx, web.ListTransactionsInput{Limit: 10})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 3 {
		t.Fatalf("expected 3 transactions (opening + income + expense), got %d", len(txs))
	}
}

// 4. Zero and empty opening balance tests
func TestZeroAndEmptyOpeningBalance(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := dbtest.New(t)
	svcs := newTestServices(t, db)

	stChecking := web.AccountSubtypeChecking

	// Case A: Empty string
	emptyBal := ""
	resEmpty, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{
			Name:         "Empty Balance Account",
			Type:         web.AccountTypeBank,
			Subtype:      &stChecking,
			CurrencyCode: "USD",
		},
		OpeningBalance: &emptyBal,
	})
	if err != nil {
		t.Fatalf("OpenAccount with empty balance returned error: %v", err)
	}
	if resEmpty.OpeningBalance != nil {
		t.Errorf("expected OpeningBalance == nil for empty string, got: %+v", resEmpty.OpeningBalance)
	}

	// Case B: "0" amount
	zeroBal := "0"
	resZero, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{
			Name:         "Zero Balance Account",
			Type:         web.AccountTypeBank,
			Subtype:      &stChecking,
			CurrencyCode: "USD",
		},
		OpeningBalance:     &zeroBal,
		OpeningBalanceDate: "2026-09-05",
	})
	if err != nil {
		t.Fatalf("OpenAccount with '0' returned error: %v", err)
	}
	if resZero.OpeningBalance != nil {
		t.Errorf("expected OpeningBalance == nil for '0' balance, got: %+v", resZero.OpeningBalance)
	}

	// Case C: "0.00" amount
	zeroDecBal := "0.00"
	resZeroDec, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{
			Name:         "Zero Dec Balance Account",
			Type:         web.AccountTypeBank,
			Subtype:      &stChecking,
			CurrencyCode: "USD",
		},
		OpeningBalance:     &zeroDecBal,
		OpeningBalanceDate: "2026-09-05",
	})
	if err != nil {
		t.Fatalf("OpenAccount with '0.00' returned error: %v", err)
	}
	if resZeroDec.OpeningBalance != nil {
		t.Errorf("expected OpeningBalance == nil for '0.00' balance, got: %+v", resZeroDec.OpeningBalance)
	}

	// Verify balance is 0.00
	bal, err := svcs.Ledger.GetAccountBalance(ctx, resZero.Account.ID)
	if err != nil {
		t.Fatalf("getting balance: %v", err)
	}
	if bal.BalanceMinor != 0 || bal.BalanceFormatted != "0.00" {
		t.Errorf("expected balance 0.00, got %s (minor %d)", bal.BalanceFormatted, bal.BalanceMinor)
	}
}

// 5. M3 Adapter operations tests over real SQLite
func TestAdapterM3Operations(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := dbtest.New(t)
	svcs := newTestServices(t, db)

	// Test Backups adapter
	backups, err := svcs.Backups.List(ctx)
	if err != nil {
		t.Fatalf("listing backups: %v", err)
	}
	initialBackupCount := len(backups)

	createdBackup, err := svcs.Backups.CreateManual(ctx)
	if err != nil {
		t.Fatalf("creating manual backup: %v", err)
	}
	if createdBackup.Reason != "manual" {
		t.Errorf("expected reason 'manual', got %q", createdBackup.Reason)
	}

	backupsAfter, err := svcs.Backups.List(ctx)
	if err != nil {
		t.Fatalf("listing backups after creation: %v", err)
	}
	if len(backupsAfter) != initialBackupCount+1 {
		t.Errorf("expected %d backups, got %d", initialBackupCount+1, len(backupsAfter))
	}

	// Test UpdateTransaction & UpdateOpeningBalance
	sub := web.AccountSubtypeChecking
	bal := "200.00"
	openRes, err := svcs.Application.OpenAccount(ctx, web.OpenAccountInput{
		Account: web.CreateAccountInput{
			Name: "Adapter USD Acct", Type: web.AccountTypeBank,
			Subtype: &sub, CurrencyCode: "USD",
		},
		OpeningBalance:     &bal,
		OpeningBalanceDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("opening account: %v", err)
	}

	// Update opening balance
	updatedOpening, err := svcs.Ledger.UpdateOpeningBalance(ctx, openRes.OpeningBalance.ID, web.UpdateOpeningBalanceInput{
		Amount:        "300.00",
		EffectiveDate: "2026-09-02",
	})
	if err != nil {
		t.Fatalf("updating opening balance: %v", err)
	}
	if updatedOpening.AmountFormatted != "300.00" {
		t.Errorf("expected amount 300.00, got %q", updatedOpening.AmountFormatted)
	}

	// Create category & record expense
	catFood, _ := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Food", Type: web.CategoryTypeExpense})
	catSalary, _ := svcs.Categories.Create(ctx, web.CreateCategoryInput{Name: "Salary", Type: web.CategoryTypeIncome})

	txExp, err := svcs.Ledger.RecordExpense(ctx, web.RecordExpenseInput{
		AccountID:       openRes.Account.ID,
		CategoryID:      catFood.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		EffectiveDate:   "2026-09-03",
	})
	if err != nil {
		t.Fatalf("recording expense: %v", err)
	}

	// Update regular transaction
	desc := "Updated grocery expense"
	updatedTx, err := svcs.Ledger.UpdateTransaction(ctx, txExp.ID, web.UpdateTransactionInput{
		AccountID:           openRes.Account.ID,
		Type:                web.TransactionTypeExpense,
		CategoryID:          catFood.ID,
		PaymentMethodID:     2,
		Amount:              "75.00",
		Description:         &desc,
		EffectiveDate:       "2026-09-04",
		AllowCurrencyChange: false,
	})
	if err != nil {
		t.Fatalf("updating transaction: %v", err)
	}
	if updatedTx.AmountFormatted != "-75.00" {
		t.Errorf("expected amount -75.00, got %q", updatedTx.AmountFormatted)
	}
	if updatedTx.Description == nil || *updatedTx.Description != desc {
		t.Errorf("expected description %q, got %+v", desc, updatedTx.Description)
	}

	// Switch to INCOME
	updatedIncome, err := svcs.Ledger.UpdateTransaction(ctx, txExp.ID, web.UpdateTransactionInput{
		AccountID:           openRes.Account.ID,
		Type:                web.TransactionTypeIncome,
		CategoryID:          catSalary.ID,
		PaymentMethodID:     1,
		Amount:              "120.00",
		EffectiveDate:       "2026-09-05",
		AllowCurrencyChange: false,
	})
	if err != nil {
		t.Fatalf("switching transaction to income: %v", err)
	}
	if updatedIncome.Type != web.TransactionTypeIncome || updatedIncome.AmountFormatted != "120.00" {
		t.Errorf("expected +120.00 INCOME, got %s %s", updatedIncome.Type, updatedIncome.AmountFormatted)
	}
}
