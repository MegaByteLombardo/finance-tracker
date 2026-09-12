package settings_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
)

func TestTheSeededBaseCurrency(t *testing.T) {
	t.Parallel()

	service := settings.NewService(dbtest.New(t))

	current, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.BaseCurrencyCode != "USD" {
		t.Errorf("base currency = %q, want USD", current.BaseCurrencyCode)
	}
	if current.CreatedAt.IsZero() || current.UpdatedAt.IsZero() {
		t.Error("the settings row has no timestamps")
	}
}

func TestSetBaseCurrency(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	service := settings.NewService(db)

	updated, err := service.SetBaseCurrency(t.Context(), " ves ") // untrimmed, lower case
	if err != nil {
		t.Fatalf("SetBaseCurrency: %v", err)
	}
	if updated.BaseCurrencyCode != "VES" {
		t.Errorf("base currency = %q, want VES", updated.BaseCurrencyCode)
	}

	reread, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reread.BaseCurrencyCode != "VES" {
		t.Errorf("the change did not stick: %q", reread.BaseCurrencyCode)
	}

	// Still exactly one row: the update must never insert.
	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM app_settings").Scan(&rows); err != nil {
		t.Fatalf("counting settings rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d settings rows, want 1", rows)
	}
}

func TestSetBaseCurrencyRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    string
		wantErr error
	}{
		{"a currency that does not exist", "XXX", settings.ErrCurrencyNotFound},
		{"an empty code", "", settings.ErrInvalidInput},
		{"a two-letter code", "US", settings.ErrInvalidInput},
		{"a four-letter code", "USDD", settings.ErrInvalidInput},
		{"digits", "US1", settings.ErrInvalidInput},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := settings.NewService(dbtest.New(t))

			if _, err := service.SetBaseCurrency(t.Context(), tc.code); !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}

			// The refusal left the setting alone.
			current, err := service.Get(t.Context())
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if current.BaseCurrencyCode != "USD" {
				t.Errorf("the base currency changed to %q despite the refusal", current.BaseCurrencyCode)
			}
		})
	}
}

// TestAnArchivedCurrencyCannotBeChosenButAnAlreadyChosenOneKeepsWorking is the
// same rule accounts follow: archiving retires a currency from NEW use without
// invalidating what already refers to it.
func TestAnArchivedCurrencyCannotBeChosenButAnAlreadyChosenOneKeepsWorking(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	service := settings.NewService(db)
	currencies := currency.NewService(db)

	if _, err := currencies.SetActive(t.Context(), "VES", false); err != nil {
		t.Fatalf("archiving VES: %v", err)
	}

	if _, err := service.SetBaseCurrency(t.Context(), "VES"); !errors.Is(err, settings.ErrCurrencyInactive) {
		t.Errorf("error = %v, want ErrCurrencyInactive", err)
	}

	// But a base currency archived AFTER it was chosen keeps working: the rule
	// applies at the moment of the choice, not forever after.
	if _, err := currencies.SetActive(t.Context(), "VES", true); err != nil {
		t.Fatalf("restoring VES: %v", err)
	}
	if _, err := service.SetBaseCurrency(t.Context(), "VES"); err != nil {
		t.Fatalf("SetBaseCurrency: %v", err)
	}
	if _, err := currencies.SetActive(t.Context(), "VES", false); err != nil {
		t.Fatalf("archiving VES again: %v", err)
	}

	current, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.BaseCurrencyCode != "VES" {
		t.Errorf("base currency = %q; archiving it must not change the setting", current.BaseCurrencyCode)
	}
}

// TestChangingTheBaseCurrencyTouchesNothingElse is the invariant that matters
// most about this package, asserted at the level of the raw tables.
func TestChangingTheBaseCurrencyTouchesNothingElse(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	service := settings.NewService(db)

	seedLedger(t, db)
	before := snapshotEverything(t, db)

	if _, err := service.SetBaseCurrency(t.Context(), "EUR"); err != nil {
		t.Fatalf("SetBaseCurrency: %v", err)
	}

	after := snapshotEverything(t, db)
	if len(after) != len(before) {
		t.Fatalf("%d rows after the change, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("changing the base currency rewrote a ledger row:\n before %s\n  after %s",
				before[i], after[i])
		}
	}
}

func TestGetReportsAMissingSettingsRow(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	service := settings.NewService(db)

	// Only reachable by hand-editing the database: migration 00012 creates the
	// row and a CHECK makes a second impossible. It must be an error rather than
	// a silent default that the rest of the application would then treat as the
	// user's choice.
	if _, err := db.ExecContext(t.Context(), "DELETE FROM app_settings"); err != nil {
		t.Fatalf("removing the settings row: %v", err)
	}

	if _, err := service.Get(t.Context()); !errors.Is(err, settings.ErrMissing) {
		t.Errorf("Get error = %v, want ErrMissing", err)
	}
	if _, err := service.SetBaseCurrency(t.Context(), "EUR"); !errors.Is(err, settings.ErrMissing) {
		t.Errorf("SetBaseCurrency error = %v, want ErrMissing", err)
	}
}

// TestCurrencyCodeLengthAgreesWithTheCatalogue is the counterpart to this
// package duplicating the code length rather than importing it, the same
// arrangement money and currency use for the minor-unit bounds.
func TestCurrencyCodeLengthAgreesWithTheCatalogue(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	service := settings.NewService(db)

	// Every seeded code must be acceptable to this package's own validation.
	list, err := currency.NewService(db).List(t.Context(), true)
	if err != nil {
		t.Fatalf("listing currencies: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("no currencies are seeded")
	}
	for _, c := range list {
		if _, err := service.SetBaseCurrency(t.Context(), c.Code); err != nil {
			t.Errorf("the catalogue holds %q but settings refuses it: %v", c.Code, err)
		}
	}
}

func seedLedger(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,institution,is_active,created_at,updated_at)
		 VALUES (1,'Mercantil USD','BANK','CHECKING','USD','Mercantil',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z'),
		        (2,'Banesco VES','BANK','SAVINGS','VES','Banesco',1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z')`,
		`INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,description,effective_date,created_at,updated_at)
		 VALUES (10,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:00.000Z','2026-09-01T11:00:00.000Z'),
		        (11,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:01.000Z','2026-09-01T11:00:01.000Z')`,
		`INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		 VALUES (100,10,1,100000,'2026-09-01T11:00:00.000Z'),
		        (101,11,2,5000000,'2026-09-01T11:00:01.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding the ledger: %v", err)
		}
	}
}

// snapshotEverything reads every row of every table a valuation must never
// touch.
func snapshotEverything(t *testing.T, db *sql.DB) []string {
	t.Helper()

	var snapshot []string

	rows, err := db.QueryContext(t.Context(), `
		SELECT id, transaction_type, effective_date, updated_at FROM transactions ORDER BY id`)
	if err != nil {
		t.Fatalf("reading transactions: %v", err)
	}
	for rows.Next() {
		var id int64
		var txType, effectiveDate, updatedAt string
		if err := rows.Scan(&id, &txType, &effectiveDate, &updatedAt); err != nil {
			t.Fatalf("scanning a transaction: %v", err)
		}
		snapshot = append(snapshot, fmt.Sprintf("tx|%d|%s|%s|%s", id, txType, effectiveDate, updatedAt))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatalf("reading transactions: %v", err)
	}

	rows, err = db.QueryContext(t.Context(), `
		SELECT id, account_id, amount_minor FROM account_movements ORDER BY id`)
	if err != nil {
		t.Fatalf("reading movements: %v", err)
	}
	for rows.Next() {
		var id, accountID, amount int64
		if err := rows.Scan(&id, &accountID, &amount); err != nil {
			t.Fatalf("scanning a movement: %v", err)
		}
		snapshot = append(snapshot, fmt.Sprintf("mv|%d|%d|%d", id, accountID, amount))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatalf("reading movements: %v", err)
	}

	rows, err = db.QueryContext(t.Context(), `
		SELECT a.id, a.currency_code, COALESCE(SUM(m.amount_minor), 0)
		  FROM accounts a LEFT JOIN account_movements m ON m.account_id = a.id
		 GROUP BY a.id, a.currency_code ORDER BY a.id`)
	if err != nil {
		t.Fatalf("reading balances: %v", err)
	}
	for rows.Next() {
		var id, balance int64
		var code string
		if err := rows.Scan(&id, &code, &balance); err != nil {
			t.Fatalf("scanning a balance: %v", err)
		}
		snapshot = append(snapshot, fmt.Sprintf("bal|%d|%s|%d", id, code, balance))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatalf("reading balances: %v", err)
	}

	return snapshot
}
