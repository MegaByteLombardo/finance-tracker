package application_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

const today = "2026-09-05"

func ptr(s string) *string { return &s }

func checking() *account.Subtype { s := account.Checking; return &s }

func fixture(t *testing.T) (*application.Service, *ledger.Service, *currency.Service, *sql.DB) {
	t.Helper()

	db := dbtest.New(t)
	return application.NewService(db, savings.NewService(db)), ledger.NewService(db), currency.NewService(db), db
}

// counts returns how much of the workflow landed, which is how these tests tell
// "both" from "neither".
func counts(t *testing.T, db *sql.DB) (accounts, transactions, movements int) {
	t.Helper()

	for _, q := range []struct {
		query string
		into  *int
	}{
		{"SELECT count(*) FROM accounts", &accounts},
		{"SELECT count(*) FROM transactions", &transactions},
		{"SELECT count(*) FROM account_movements", &movements},
	} {
		if err := db.QueryRowContext(t.Context(), q.query).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	return accounts, transactions, movements
}

func validAccount() account.CreateInput {
	return account.CreateInput{
		Name:         "Mercantil USD",
		Type:         account.Bank,
		Subtype:      checking(),
		CurrencyCode: "USD",
		Institution:  ptr("Mercantil"),
	}
}

func TestOpenAccountWithAnOpeningBalance(t *testing.T) {
	t.Parallel()

	app, ledgerSvc, _, db := fixture(t)
	ctx := t.Context()

	result, err := app.OpenAccount(ctx, application.OpenAccountInput{
		Account:            validAccount(),
		OpeningBalance:     ptr("1000"),
		OpeningBalanceDate: today,
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if result.Account.ID == 0 {
		t.Error("the account has no id")
	}
	if result.Account.Subtype == nil || *result.Account.Subtype != account.Checking {
		t.Errorf("Subtype = %v, want CHECKING", result.Account.Subtype)
	}
	if result.OpeningBalance == nil {
		t.Fatal("no opening balance was recorded")
	}
	if result.OpeningBalance.AmountMinor != 100000 {
		t.Errorf("AmountMinor = %d, want 100000", result.OpeningBalance.AmountMinor)
	}
	if result.OpeningBalance.Type != ledger.OpeningBalance {
		t.Errorf("Type = %q, want %q", result.OpeningBalance.Type, ledger.OpeningBalance)
	}
	if result.OpeningBalance.AccountID != result.Account.ID {
		t.Errorf("the opening balance landed in account %d, not %d",
			result.OpeningBalance.AccountID, result.Account.ID)
	}

	if accounts, transactions, movements := counts(t, db); accounts != 1 || transactions != 1 || movements != 1 {
		t.Errorf("wrote %d accounts, %d transactions, %d movements; want 1, 1, 1",
			accounts, transactions, movements)
	}

	balance, err := ledgerSvc.GetAccountBalance(ctx, result.Account.ID)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 100000 {
		t.Errorf("balance = %d, want 100000", balance.BalanceMinor)
	}
}

// Leaving the balance field empty is how a user says the account starts at
// zero, and it must take the same code path as any other new account.
func TestOpenAccountWithoutAnOpeningBalance(t *testing.T) {
	t.Parallel()

	app, ledgerSvc, _, db := fixture(t)
	ctx := t.Context()

	for name, balance := range map[string]*string{
		"absent":             nil,
		"empty":              ptr(""),
		"blank":              ptr("   "),
		"zero":               ptr("0"),
		"zero with decimals": ptr("0.00"),
	} {
		t.Run(name, func(t *testing.T) {
			in := application.OpenAccountInput{Account: validAccount(), OpeningBalance: balance}
			in.Account.Name = "Cuenta " + name

			result, err := app.OpenAccount(ctx, in)
			if err != nil {
				t.Fatalf("OpenAccount: %v", err)
			}
			if result.OpeningBalance != nil {
				t.Errorf("an opening balance was recorded for %q", name)
			}

			balance, err := ledgerSvc.GetAccountBalance(ctx, result.Account.ID)
			if err != nil {
				t.Fatalf("GetAccountBalance: %v", err)
			}
			if balance.BalanceMinor != 0 {
				t.Errorf("balance = %d, want 0", balance.BalanceMinor)
			}
		})
	}

	// Five accounts, and not one transaction between them.
	if accounts, transactions, movements := counts(t, db); accounts != 5 || transactions != 0 || movements != 0 {
		t.Errorf("wrote %d accounts, %d transactions, %d movements; want 5, 0, 0",
			accounts, transactions, movements)
	}
}

// The reason this package exists. A handler doing the two steps separately
// would leave an account the user believes has a balance.
func TestOpenAccountIsAtomic(t *testing.T) {
	t.Parallel()

	failures := map[string]struct {
		in   application.OpenAccountInput
		want error
	}{
		"unparseable amount": {
			application.OpenAccountInput{
				Account: validAccount(), OpeningBalance: ptr("mil dolares"), OpeningBalanceDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"too many decimals": {
			application.OpenAccountInput{
				Account: validAccount(), OpeningBalance: ptr("1000.005"), OpeningBalanceDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"overflowing amount": {
			application.OpenAccountInput{
				Account: validAccount(), OpeningBalance: ptr("99999999999999999999"), OpeningBalanceDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"missing date": {
			application.OpenAccountInput{
				Account: validAccount(), OpeningBalance: ptr("1000"),
			},
			ledger.ErrInvalidInput,
		},
		"impossible date": {
			application.OpenAccountInput{
				Account: validAccount(), OpeningBalance: ptr("1000"), OpeningBalanceDate: "2026-02-31",
			},
			ledger.ErrInvalidInput,
		},
	}

	for name, tc := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, _, db := fixture(t)

			_, err := app.OpenAccount(t.Context(), tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenAccount = %v, want %v", err, tc.want)
			}

			// The account must NOT exist. This is the whole point: the opening
			// balance failed after the account insert, inside the same
			// transaction, so both are gone.
			accounts, transactions, movements := counts(t, db)
			if accounts != 0 {
				t.Errorf("%d account(s) survived a failed opening balance; the workflow is not atomic", accounts)
			}
			if transactions != 0 || movements != 0 {
				t.Errorf("%d transactions and %d movements survived, want none", transactions, movements)
			}
		})
	}
}

// A failure in the account half must leave nothing either, and must surface as
// the account package's error.
func TestOpenAccountRejectsInvalidAccounts(t *testing.T) {
	t.Parallel()

	bankWithoutSubtype := validAccount()
	bankWithoutSubtype.Subtype = nil

	cashWithSubtype := validAccount()
	cashWithSubtype.Type = account.Cash

	unknownCurrency := validAccount()
	unknownCurrency.CurrencyCode = "ZZZ"

	blankName := validAccount()
	blankName.Name = "   "

	failures := map[string]struct {
		in   account.CreateInput
		want error
	}{
		"bank without subtype": {bankWithoutSubtype, account.ErrInvalidInput},
		"cash with subtype":    {cashWithSubtype, account.ErrInvalidInput},
		"blank name":           {blankName, account.ErrInvalidInput},
		"unknown currency":     {unknownCurrency, account.ErrCurrencyNotFound},
	}

	for name, tc := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, _, db := fixture(t)

			_, err := app.OpenAccount(t.Context(), application.OpenAccountInput{
				Account: tc.in, OpeningBalance: ptr("1000"), OpeningBalanceDate: today,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenAccount = %v, want %v", err, tc.want)
			}

			if accounts, transactions, movements := counts(t, db); accounts != 0 || transactions != 0 || movements != 0 {
				t.Errorf("wrote %d accounts, %d transactions, %d movements after a rejected create; want none",
					accounts, transactions, movements)
			}
		})
	}
}

// No account is ever opened denominated in a retired currency, and the
// opening-balance half must not soften that.
func TestOpenAccountRejectsAnInactiveCurrency(t *testing.T) {
	t.Parallel()

	app, _, currencies, db := fixture(t)
	ctx := t.Context()

	if _, err := currencies.SetActive(ctx, "EUR", false); err != nil {
		t.Fatalf("archiving the currency: %v", err)
	}

	in := validAccount()
	in.Name = "Cuenta EUR"
	in.CurrencyCode = "EUR"

	_, err := app.OpenAccount(ctx, application.OpenAccountInput{
		Account: in, OpeningBalance: ptr("1000"), OpeningBalanceDate: today,
	})
	if !errors.Is(err, account.ErrCurrencyInactive) {
		t.Fatalf("OpenAccount with an inactive currency = %v, want ErrCurrencyInactive", err)
	}

	if accounts, transactions, _ := counts(t, db); accounts != 0 || transactions != 0 {
		t.Errorf("wrote %d accounts and %d transactions, want none", accounts, transactions)
	}
}

// A negative opening balance is an account that starts overdrawn.
func TestOpenAccountWithANegativeOpeningBalance(t *testing.T) {
	t.Parallel()

	app, ledgerSvc, _, _ := fixture(t)
	ctx := t.Context()

	result, err := app.OpenAccount(ctx, application.OpenAccountInput{
		Account: validAccount(), OpeningBalance: ptr("-250.75"), OpeningBalanceDate: today,
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if result.OpeningBalance == nil {
		t.Fatal("a negative opening balance recorded nothing")
	}

	balance, err := ledgerSvc.GetAccountBalance(ctx, result.Account.ID)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != -25075 {
		t.Errorf("balance = %d, want -25075", balance.BalanceMinor)
	}
	if balance.BalanceFormatted != "-250.75" {
		t.Errorf("BalanceFormatted = %q, want %q", balance.BalanceFormatted, "-250.75")
	}
}

// The opening balance is dated when the account actually stood at that amount,
// which need not be today.
func TestOpeningBalanceKeepsItsEffectiveDate(t *testing.T) {
	t.Parallel()

	app, _, _, _ := fixture(t)

	result, err := app.OpenAccount(t.Context(), application.OpenAccountInput{
		Account: validAccount(), OpeningBalance: ptr("1000"), OpeningBalanceDate: "2026-01-01",
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if got := result.OpeningBalance.EffectiveDate.Format("2006-01-02"); got != "2026-01-01" {
		t.Errorf("EffectiveDate = %q, want %q", got, "2026-01-01")
	}
}

func TestOpenAccountRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	app, _, _, db := fixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := app.OpenAccount(ctx, application.OpenAccountInput{
		Account: validAccount(), OpeningBalance: ptr("1000"), OpeningBalanceDate: today,
	}); err == nil {
		t.Error("OpenAccount succeeded with a cancelled context")
	}

	if accounts, _, _ := counts(t, db); accounts != 0 {
		t.Errorf("%d account(s) were written despite a cancelled context", accounts)
	}
}
