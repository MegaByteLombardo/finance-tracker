package migrations_test

import (
	"database/sql"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

// These tests assert what the database itself refuses, not what Go validates
// before it. Application-level validation can be bypassed by a bug or by a
// hand-written statement; a CHECK constraint cannot.

// --- column sets ------------------------------------------------------------

// Asserting the exact column set, rather than searching for forbidden names
// like "balance", catches every kind of schema drift: a monetary column, a
// column added by accident, a column renamed, a column removed.
func TestTablesHaveExactlyTheExpectedColumns(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	tests := map[string][]string{
		"currencies": {
			"code", "name", "symbol", "minor_unit", "is_active", "created_at", "updated_at",
		},
		"accounts": {
			"id", "name", "account_type", "currency_code", "institution",
			"is_active", "created_at", "updated_at",
		},
	}

	for table, want := range tests {
		t.Run(table, func(t *testing.T) {
			t.Parallel()

			got := columnNames(t, db, table)
			if len(got) != len(want) {
				t.Fatalf("%s has %d columns %v, want %d %v", table, len(got), got, len(want), want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s column %d = %q, want %q\n got: %v\nwant: %v",
						table, i, got[i], want[i], got, want)
				}
			}
		})
	}
}

func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	// PRAGMA does not accept a bound parameter for the table name; the value
	// here is a test constant, never user input.
	rows, err := db.QueryContext(t.Context(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("reading columns of %s: %v", table, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning column name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns: %v", err)
	}
	return names
}

// --- currencies -------------------------------------------------------------

func TestCurrencySeed(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM currencies").Scan(&count); err != nil {
		t.Fatalf("counting currencies: %v", err)
	}
	if count != 3 {
		t.Errorf("seed inserted %d currencies, want exactly 3", count)
	}

	want := map[string]struct {
		name      string
		symbol    string
		minorUnit int
	}{
		"USD": {"US Dollar", "$", 2},
		"EUR": {"Euro", "€", 2},
		"VES": {"Bolívar Soberano", "Bs.", 2},
	}

	for code, expected := range want {
		var (
			name      string
			symbol    sql.NullString
			minorUnit int
			isActive  bool
		)
		err := db.QueryRowContext(t.Context(),
			"SELECT name, symbol, minor_unit, is_active FROM currencies WHERE code = ?", code,
		).Scan(&name, &symbol, &minorUnit, &isActive)
		if err != nil {
			t.Errorf("currency %s is missing from the seed: %v", code, err)
			continue
		}
		if name != expected.name {
			t.Errorf("%s name = %q, want %q", code, name, expected.name)
		}
		if !symbol.Valid || symbol.String != expected.symbol {
			t.Errorf("%s symbol = %v, want %q", code, symbol, expected.symbol)
		}
		if minorUnit != expected.minorUnit {
			t.Errorf("%s minor_unit = %d, want %d", code, minorUnit, expected.minorUnit)
		}
		if !isActive {
			t.Errorf("%s is not active", code)
		}
	}
}

func TestCurrencyConstraints(t *testing.T) {
	t.Parallel()

	const insert = `INSERT INTO currencies (code, name, symbol, minor_unit, is_active)
	                VALUES (?, ?, ?, ?, ?)`

	rejected := map[string]struct {
		code      string
		name      string
		symbol    any
		minorUnit any
		isActive  any
	}{
		"lowercase code":       {"usd", "Dollar", "$", 2, 1},
		"two-letter code":      {"US", "Dollar", "$", 2, 1},
		"four-letter code":     {"USDX", "Dollar", "$", 2, 1},
		"digits in code":       {"US1", "Dollar", "$", 2, 1},
		"empty code":           {"", "Dollar", "$", 2, 1},
		"blank name":           {"AAA", "   ", "$", 2, 1},
		"empty name":           {"AAA", "", "$", 2, 1},
		"empty symbol":         {"AAA", "Test", "", 2, 1},
		"blank symbol":         {"AAA", "Test", "   ", 2, 1},
		"negative minor unit":  {"AAA", "Test", "$", -1, 1},
		"minor unit above ISO": {"AAA", "Test", "$", 5, 1},
		"non-integer active":   {"AAA", "Test", "$", 2, 2},
		"negative active":      {"AAA", "Test", "$", 2, -1},
		// STRICT typing: the column is INTEGER, so text must be refused
		// outright rather than stored under SQLite's usual type affinity.
		"text in minor unit": {"AAA", "Test", "$", "two", 1},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insert,
				tc.code, tc.name, tc.symbol, tc.minorUnit, tc.isActive)
			if err == nil {
				t.Fatalf("the database accepted an invalid currency (%s)", name)
			}
		})
	}
}

func TestCurrencyAcceptsAbsentSymbol(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	_, err := db.ExecContext(t.Context(),
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES ('JPY', 'Japanese Yen', NULL, 0)")
	if err != nil {
		t.Fatalf("a currency with no symbol was rejected: %v", err)
	}

	var symbol sql.NullString
	if err := db.QueryRowContext(t.Context(),
		"SELECT symbol FROM currencies WHERE code = 'JPY'").Scan(&symbol); err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if symbol.Valid {
		t.Errorf("symbol = %q, want NULL", symbol.String)
	}
}

func TestCurrencyCodeIsUnique(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	_, err := db.ExecContext(t.Context(),
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES ('USD', 'Duplicate', '$', 2)")
	if err == nil {
		t.Error("the database accepted a duplicate currency code")
	}
}

func TestTimestampDefaultsMatchTheRequiredFormat(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	// The defaults must satisfy the same GLOB the columns enforce, otherwise a
	// plain INSERT would be impossible.
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES ('AAA', 'Test', '$', 2)"); err != nil {
		t.Fatalf("insert relying on timestamp defaults: %v", err)
	}

	var createdAt, updatedAt string
	if err := db.QueryRowContext(t.Context(),
		"SELECT created_at, updated_at FROM currencies WHERE code = 'AAA'").Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("reading timestamps: %v", err)
	}

	// Fixed width is what makes lexicographic order chronological.
	const wantLen = len("2026-08-29T22:37:55.123Z")
	if len(createdAt) != wantLen {
		t.Errorf("created_at = %q (%d chars), want %d", createdAt, len(createdAt), wantLen)
	}
	if len(updatedAt) != wantLen {
		t.Errorf("updated_at = %q (%d chars), want %d", updatedAt, len(updatedAt), wantLen)
	}
}

// --- accounts ---------------------------------------------------------------

const insertAccount = `INSERT INTO accounts (name, account_type, currency_code, institution, is_active)
                       VALUES (?, ?, ?, ?, ?)`

func TestAccountAcceptsEveryValidType(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	for _, accountType := range []string{"BANK", "CASH", "WALLET", "OTHER"} {
		_, err := db.ExecContext(t.Context(), insertAccount,
			"Account "+accountType, accountType, "USD", nil, 1)
		if err != nil {
			t.Errorf("account_type %q was rejected: %v", accountType, err)
		}
	}
}

func TestAccountConstraints(t *testing.T) {
	t.Parallel()

	rejected := map[string]struct {
		name        string
		accountType string
		currency    any
		institution any
		isActive    any
	}{
		"unknown currency": {"Test", "BANK", "XYZ", "Bank", 1},
		"null currency":    {"Test", "BANK", nil, "Bank", 1},
		"empty currency":   {"Test", "BANK", "", "Bank", 1},
		// CHECKING and SAVINGS were considered and rejected: savings is a
		// purpose, not a container, and it can live in a bank, in cash or in a
		// wallet.
		"CHECKING is not a type":  {"Test", "CHECKING", "USD", "Bank", 1},
		"SAVINGS is not a type":   {"Test", "SAVINGS", "USD", "Bank", 1},
		"lowercase type":          {"Test", "bank", "USD", "Bank", 1},
		"unknown type":            {"Test", "CRYPTO", "USD", "Bank", 1},
		"empty type":              {"Test", "", "USD", "Bank", 1},
		"blank name":              {"   ", "BANK", "USD", "Bank", 1},
		"empty name":              {"", "BANK", "USD", "Bank", 1},
		"empty institution":       {"Test", "BANK", "USD", "", 1},
		"blank institution":       {"Test", "BANK", "USD", "   ", 1},
		"non-boolean active":      {"Test", "BANK", "USD", "Bank", 2},
		"negative active":         {"Test", "BANK", "USD", "Bank", -1},
		"text in active (STRICT)": {"Test", "BANK", "USD", "Bank", "yes"},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertAccount,
				tc.name, tc.accountType, tc.currency, tc.institution, tc.isActive)
			if err == nil {
				t.Fatalf("the database accepted an invalid account (%s)", name)
			}
		})
	}
}

// Cash held outside any institution genuinely has none.
func TestAccountAcceptsAbsentInstitution(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := db.ExecContext(t.Context(), insertAccount,
		"Efectivo USD", "CASH", "USD", nil, 1); err != nil {
		t.Fatalf("an account with no institution was rejected: %v", err)
	}

	var institution sql.NullString
	if err := db.QueryRowContext(t.Context(),
		"SELECT institution FROM accounts WHERE name = 'Efectivo USD'").Scan(&institution); err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if institution.Valid {
		t.Errorf("institution = %q, want NULL", institution.String)
	}
}

// Currencies are retired with is_active, never deleted. RESTRICT is what makes
// that a rule rather than a convention.
func TestCurrencyInUseCannotBeDeleted(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := db.ExecContext(t.Context(), insertAccount,
		"Mercantil USD", "BANK", "USD", "Mercantil", 1); err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	if _, err := db.ExecContext(t.Context(), "DELETE FROM currencies WHERE code = 'USD'"); err == nil {
		t.Error("a currency referenced by an account was deleted")
	}

	// An unreferenced currency can still be deleted, so the constraint is about
	// references rather than a blanket prohibition.
	if _, err := db.ExecContext(t.Context(), "DELETE FROM currencies WHERE code = 'EUR'"); err != nil {
		t.Errorf("an unreferenced currency could not be deleted: %v", err)
	}
}

// Renaming a currency code would rewrite the financial identity of every
// account denominated in it.
func TestCurrencyInUseCannotBeRenamed(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := db.ExecContext(t.Context(), insertAccount,
		"Mercantil USD", "BANK", "USD", "Mercantil", 1); err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	if _, err := db.ExecContext(t.Context(),
		"UPDATE currencies SET code = 'USX' WHERE code = 'USD'"); err == nil {
		t.Error("a currency code referenced by an account was renamed")
	}
}

func TestAccountsCurrencyIndexExists(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	var name string
	err := db.QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_schema WHERE type = 'index' AND name = 'idx_accounts_currency_code'").
		Scan(&name)
	if err != nil {
		t.Errorf("idx_accounts_currency_code is missing; foreign key checks would scan the table: %v", err)
	}
}
