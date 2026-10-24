package migrations_test

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
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
			"id", "name", "account_type", "account_subtype", "currency_code", "institution",
			"is_active", "created_at", "updated_at",
		},
		"categories": {
			"id", "name", "category_type", "is_active", "created_at", "updated_at",
		},
		"transactions": {
			"id", "transaction_type", "category_id", "payment_method_id", "description",
			"effective_date", "created_at", "updated_at",
		},
		"payment_methods": {
			"id", "name", "is_active", "created_at", "updated_at",
		},
		"account_movements": {
			"id", "transaction_id", "account_id", "amount_minor", "created_at",
		},
		"exchange_rates": {
			"id", "base_currency_code", "quote_currency_code", "effective_date", "rate",
			"source", "is_active", "fetched_at", "created_at", "updated_at",
		},
		"app_settings": {
			"id", "base_currency_code", "created_at", "updated_at",
		},
		"savings_policy_versions": {
			"id", "rate_bp", "effective_from", "created_at", "updated_at",
		},
		"savings_category_rules": {
			"category_id", "is_eligible", "created_at", "updated_at",
		},
		"savings_income_overrides": {
			"transaction_id", "is_eligible", "reason", "created_at", "updated_at",
		},
		"savings_suggestions": {
			"id", "income_transaction_id", "revision", "policy_version_id",
			"income_account_id", "income_category_id", "income_amount_minor",
			"income_currency_code", "income_effective_date",
			"income_usd_minor", "suggested_usd_minor", "fx_method", "valued_at",
			"status", "status_reason", "created_at", "updated_at",
		},
		"savings_suggestion_rate_legs": {
			"suggestion_id", "leg", "exchange_rate_id", "base_currency_code",
			"quote_currency_code", "rate", "effective_date", "source", "inverted", "created_at",
		},
		"savings_operations": {
			"id", "kind", "request_key", "suggestion_id", "transaction_id", "reason",
			"is_exception", "effective_date", "created_at",
		},
		"savings_entries": {
			"id", "operation_id", "account_id", "amount_minor", "created_at", "goal_id",
		},
		"savings_goals": {
			"id", "name", "goal_type", "target_usd_minor", "target_date", "distribution_bp",
			"status", "closed_at", "closing_note", "created_at", "updated_at",
		},
		"savings_goal_target_revisions": {
			"id", "goal_id", "target_usd_minor", "created_at",
		},
		"savings_issue_acknowledgements": {
			"issue_key", "note", "created_at",
		},
		"credit_cards": {
			"account_id", "product_name", "credit_limit_minor", "statement_day",
			"due_days_after_statement", "payment_processing_days", "denomination",
			"denomination_confirmed", "default_payment_method_id", "created_at", "updated_at",
		},
		"credit_charges": {
			"transaction_id", "kind", "created_at",
		},
		"transaction_refunds": {
			"refund_transaction_id", "original_transaction_id", "created_at",
		},
		"operation_requests": {
			"request_key", "transaction_id", "account_id", "created_at",
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

const insertAccount = `INSERT INTO accounts (name, account_type, account_subtype, currency_code, institution, is_active)
                       VALUES (?, ?, ?, ?, ?, ?)`

func TestAccountAcceptsEveryValidType(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	valid := []struct {
		accountType string
		subtype     any
	}{
		{"BANK", "CHECKING"},
		{"BANK", "SAVINGS"},
		{"CASH", nil},
		{"WALLET", nil},
		{"OTHER", nil},
		{"CREDIT_CARD", nil},
	}

	for _, tc := range valid {
		_, err := db.ExecContext(t.Context(), insertAccount,
			"Account "+tc.accountType, tc.accountType, tc.subtype, "USD", nil, 1)
		if err != nil {
			t.Errorf("account_type %q with subtype %v was rejected: %v", tc.accountType, tc.subtype, err)
		}
	}
}

func TestAccountConstraints(t *testing.T) {
	t.Parallel()

	rejected := map[string]struct {
		name        string
		accountType string
		subtype     any
		currency    any
		institution any
		isActive    any
	}{
		"unknown currency": {"Test", "BANK", "CHECKING", "XYZ", "Bank", 1},
		"null currency":    {"Test", "BANK", "CHECKING", nil, "Bank", 1},
		"empty currency":   {"Test", "BANK", "CHECKING", "", "Bank", 1},
		// CHECKING and SAVINGS were considered and rejected as account types:
		// savings is not a container, and the distinction only exists for BANK.
		// They live in account_subtype instead.
		"CHECKING is not a type":  {"Test", "CHECKING", nil, "USD", "Bank", 1},
		"SAVINGS is not a type":   {"Test", "SAVINGS", nil, "USD", "Bank", 1},
		"lowercase type":          {"Test", "bank", "CHECKING", "USD", "Bank", 1},
		"unknown type":            {"Test", "CRYPTO", nil, "USD", "Bank", 1},
		"empty type":              {"Test", "", nil, "USD", "Bank", 1},
		"blank name":              {"   ", "BANK", "CHECKING", "USD", "Bank", 1},
		"empty name":              {"", "BANK", "CHECKING", "USD", "Bank", 1},
		"empty institution":       {"Test", "BANK", "CHECKING", "USD", "", 1},
		"blank institution":       {"Test", "BANK", "CHECKING", "USD", "   ", 1},
		"non-boolean active":      {"Test", "BANK", "CHECKING", "USD", "Bank", 2},
		"negative active":         {"Test", "BANK", "CHECKING", "USD", "Bank", -1},
		"text in active (STRICT)": {"Test", "BANK", "CHECKING", "USD", "Bank", "yes"},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertAccount,
				tc.name, tc.accountType, tc.subtype, tc.currency, tc.institution, tc.isActive)
			if err == nil {
				t.Fatalf("the database accepted an invalid account (%s)", name)
			}
		})
	}
}

// The subtype rule is a dependency between two columns, so it is worth
// asserting exhaustively rather than by example. The NULL cases matter most: a
// CHECK constraint fails only when its expression is FALSE, and an expression
// that evaluates to NULL passes, which is how a BANK account with no subtype
// would slip through a naively written constraint.
func TestAccountSubtypeDependsOnType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		accountType string
		subtype     any
		accepted    bool
	}{
		{"BANK", "CHECKING", true},
		{"BANK", "SAVINGS", true},
		{"BANK", nil, false},
		{"BANK", "", false},
		{"BANK", "OTHER", false},
		{"BANK", "checking", false},
		{"CASH", nil, true},
		{"CASH", "CHECKING", false},
		{"CASH", "SAVINGS", false},
		{"WALLET", nil, true},
		{"WALLET", "CHECKING", false},
		{"OTHER", nil, true},
		{"OTHER", "SAVINGS", false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%v", tc.accountType, tc.subtype), func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertAccount,
				"Test", tc.accountType, tc.subtype, "USD", nil, 1)

			switch {
			case tc.accepted && err != nil:
				t.Errorf("%s with subtype %v was rejected: %v", tc.accountType, tc.subtype, err)
			case !tc.accepted && err == nil:
				t.Errorf("%s with subtype %v was accepted", tc.accountType, tc.subtype)
			}
		})
	}
}

// The rule must survive an UPDATE too, not only an INSERT.
func TestAccountSubtypeRuleAppliesToUpdates(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, insertAccount,
		"Mercantil USD", "BANK", "CHECKING", "USD", "Mercantil", 1); err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		"UPDATE accounts SET account_subtype = NULL WHERE name = 'Mercantil USD'"); err == nil {
		t.Error("a BANK account had its subtype cleared")
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE accounts SET account_type = 'CASH' WHERE name = 'Mercantil USD'"); err == nil {
		t.Error("a BANK account became CASH while keeping its subtype")
	}
	// Changing both at once is the legitimate reclassification.
	if _, err := db.ExecContext(ctx,
		"UPDATE accounts SET account_type = 'CASH', account_subtype = NULL WHERE name = 'Mercantil USD'"); err != nil {
		t.Errorf("a legitimate reclassification was rejected: %v", err)
	}
}

// Cash held outside any institution genuinely has none.
func TestAccountAcceptsAbsentInstitution(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := db.ExecContext(t.Context(), insertAccount,
		"Efectivo USD", "CASH", nil, "USD", nil, 1); err != nil {
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
		"Mercantil USD", "BANK", "CHECKING", "USD", "Mercantil", 1); err != nil {
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
		"Mercantil USD", "BANK", "CHECKING", "USD", "Mercantil", 1); err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	if _, err := db.ExecContext(t.Context(),
		"UPDATE currencies SET code = 'USX' WHERE code = 'USD'"); err == nil {
		t.Error("a currency code referenced by an account was renamed")
	}
}

// --- categories -------------------------------------------------------------

const insertCategory = `INSERT INTO categories (name, category_type, is_active) VALUES (?, ?, ?)`

func TestCategoryConstraints(t *testing.T) {
	t.Parallel()

	rejected := map[string]struct {
		name         string
		categoryType any
		isActive     any
	}{
		"blank name":              {"   ", "EXPENSE", 1},
		"empty name":              {"", "EXPENSE", 1},
		"unknown type":            {"Comida", "TRANSFER", 1},
		"lowercase type":          {"Comida", "expense", 1},
		"empty type":              {"Comida", "", 1},
		"null type":               {"Comida", nil, 1},
		"non-boolean active":      {"Comida", "EXPENSE", 2},
		"text in active (STRICT)": {"Comida", "EXPENSE", "yes"},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertCategory, tc.name, tc.categoryType, tc.isActive)
			if err == nil {
				t.Fatalf("the database accepted an invalid category (%s)", name)
			}
		})
	}
}

func TestCategoryNameIsUniquePerTypeCaseInsensitively(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, insertCategory, "Comida", "EXPENSE", 1); err != nil {
		t.Fatalf("creating the category: %v", err)
	}

	for _, duplicate := range []string{"Comida", "comida", "COMIDA"} {
		if _, err := db.ExecContext(ctx, insertCategory, duplicate, "EXPENSE", 1); err == nil {
			t.Errorf("a duplicate EXPENSE category %q was accepted", duplicate)
		}
	}

	// The same name under the other type is legitimate: "Intereses" is both
	// earned and paid.
	if _, err := db.ExecContext(ctx, insertCategory, "Comida", "INCOME", 1); err != nil {
		t.Errorf("the same name under the other type was rejected: %v", err)
	}
}

// --- transactions -----------------------------------------------------------

const insertTransaction = `INSERT INTO transactions
                           (transaction_type, category_id, payment_method_id, description, effective_date)
                           VALUES (?, ?, ?, ?, ?)`

// The payment methods migrated from M2's fixed enumeration, by id.
const (
	methodBankTransfer = 1
	methodPOS          = 2
	methodCash         = 3
	methodOther        = 4
)

// seedCategory returns the id of a category of the given type.
func seedCategory(t *testing.T, db *sql.DB, name, categoryType string) int64 {
	t.Helper()

	result, err := db.ExecContext(t.Context(), insertCategory, name, categoryType, 1)
	if err != nil {
		t.Fatalf("seeding category %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the category id: %v", err)
	}
	return id
}

// seedAccount returns the id of a USD bank account.
func seedAccount(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()

	result, err := db.ExecContext(t.Context(), insertAccount, name, "BANK", "CHECKING", "USD", "Mercantil", 1)
	if err != nil {
		t.Fatalf("seeding account %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the account id: %v", err)
	}
	return id
}

// The shape of each transaction type is a dependency between three columns.
// Every combination is asserted, because the NULL cases are exactly the ones a
// carelessly written CHECK would let through.
func TestTransactionShapePerType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		txType      string
		hasCategory bool
		method      any
		accepted    bool
	}{
		{"INCOME", true, methodBankTransfer, true},
		{"INCOME", true, methodPOS, true},
		{"INCOME", true, methodCash, true},
		{"INCOME", true, methodOther, true},
		{"INCOME", false, methodPOS, false},
		{"INCOME", true, nil, false},
		{"INCOME", false, nil, false},
		{"EXPENSE", true, methodPOS, true},
		{"EXPENSE", false, methodPOS, false},
		{"EXPENSE", true, nil, false},
		{"OPENING_BALANCE", false, nil, true},
		{"OPENING_BALANCE", true, nil, false},
		{"OPENING_BALANCE", false, methodCash, false},
		{"OPENING_BALANCE", true, methodCash, false},
		// A transfer between two of the user's own accounts is neither income
		// nor an expense, so it has no category, and it uses no channel with the
		// outside world, so it has no payment method. Both are refused by the
		// database rather than only by the service.
		{"TRANSFER", false, nil, true},
		{"TRANSFER", true, nil, false},
		{"TRANSFER", false, methodBankTransfer, false},
		{"TRANSFER", true, methodBankTransfer, false},
		// An exchange is neither earned nor spent — the same wealth is simply
		// denominated differently afterwards — so it has no category, and it is
		// not an operation whose channel is interesting, so it has no payment
		// method. A commission charged on top is a separate EXPENSE.
		{"FX_EXCHANGE", false, nil, true},
		{"FX_EXCHANGE", true, nil, false},
		{"FX_EXCHANGE", false, methodBankTransfer, false},
		{"FX_EXCHANGE", true, methodBankTransfer, false},
		// A refund is shaped like an expense: categorised -- under an EXPENSE
		// category, which Go checks -- and with a channel. Only its sign differs,
		// and the sign lives on the movement.
		{"REFUND", true, methodPOS, true},
		{"REFUND", false, methodPOS, false},
		{"REFUND", true, nil, false},
		{"REFUND", false, nil, false},
		// The column is a foreign key now, so an id nobody defined is refused.
		{"INCOME", true, 9999, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/category=%v/method=%v", tc.txType, tc.hasCategory, tc.method), func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)

			var categoryID any
			if tc.hasCategory {
				categoryType := "INCOME"
				if tc.txType == "EXPENSE" || tc.txType == "REFUND" {
					categoryType = "EXPENSE"
				}
				categoryID = seedCategory(t, db, "Test", categoryType)
			}

			_, err := db.ExecContext(t.Context(), insertTransaction,
				tc.txType, categoryID, tc.method, nil, "2026-09-05")

			switch {
			case tc.accepted && err != nil:
				t.Errorf("a valid %s was rejected: %v", tc.txType, err)
			case !tc.accepted && err == nil:
				t.Errorf("an invalid %s was accepted", tc.txType)
			}
		})
	}
}

func TestTransactionConstraints(t *testing.T) {
	t.Parallel()

	rejected := map[string]struct {
		txType        any
		method        any
		description   any
		effectiveDate any
	}{
		"unknown type":            {"REFUND", nil, nil, "2026-09-05"},
		"unknown payment method":  {"OPENING_BALANCE", 9999, nil, "2026-09-05"},
		"lowercase type":          {"opening_balance", nil, nil, "2026-09-05"},
		"lowercase transfer":      {"transfer", nil, nil, "2026-09-05"},
		"pluralised transfer":     {"TRANSFERS", nil, nil, "2026-09-05"},
		"lowercase exchange":      {"fx_exchange", nil, nil, "2026-09-05"},
		"hyphenated exchange":     {"FX-EXCHANGE", nil, nil, "2026-09-05"},
		"exchange without prefix": {"EXCHANGE", nil, nil, "2026-09-05"},
		"invented buy type":       {"BUY_USD", nil, nil, "2026-09-05"},
		"invented sell type":      {"SELL_USD", nil, nil, "2026-09-05"},
		"null type":               {nil, nil, nil, "2026-09-05"},
		"empty description":       {"OPENING_BALANCE", nil, "", "2026-09-05"},
		"blank description":       {"OPENING_BALANCE", nil, "   ", "2026-09-05"},
		"null effective date":     {"OPENING_BALANCE", nil, nil, nil},
		"empty effective date":    {"OPENING_BALANCE", nil, nil, ""},
		"unpadded effective date": {"OPENING_BALANCE", nil, nil, "2026-9-5"},
		"timestamp not a date":    {"OPENING_BALANCE", nil, nil, "2026-09-05T00:00:00.000Z"},
		"impossible day":          {"OPENING_BALANCE", nil, nil, "2026-02-31"},
		"impossible month":        {"OPENING_BALANCE", nil, nil, "2026-13-01"},
		"day zero":                {"OPENING_BALANCE", nil, nil, "2026-09-00"},
		"letters in date":         {"OPENING_BALANCE", nil, nil, "abcd-ef-gh"},
		"slashes in date":         {"OPENING_BALANCE", nil, nil, "2026/09/05"},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			_, err := db.ExecContext(t.Context(), insertTransaction,
				tc.txType, nil, tc.method, tc.description, tc.effectiveDate)
			if err == nil {
				t.Fatalf("the database accepted an invalid transaction (%s)", name)
			}
		})
	}
}

// A leap day is a real date and must not be caught by the calendar check.
func TestTransactionAcceptsLeapDay(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := db.ExecContext(t.Context(), insertTransaction,
		"OPENING_BALANCE", nil, nil, nil, "2028-02-29"); err != nil {
		t.Errorf("2028-02-29 is a real date and was rejected: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), insertTransaction,
		"OPENING_BALANCE", nil, nil, nil, "2027-02-29"); err == nil {
		t.Error("2027-02-29 does not exist and was accepted")
	}
}

func TestCategoryInUseCannotBeDeleted(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	id := seedCategory(t, db, "Comida", "EXPENSE")
	if _, err := db.ExecContext(ctx, insertTransaction,
		"EXPENSE", id, methodPOS, nil, "2026-09-05"); err != nil {
		t.Fatalf("creating the transaction: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM categories WHERE id = ?", id); err == nil {
		t.Error("a category referenced by a transaction was deleted")
	}
}

// --- account movements ------------------------------------------------------

const insertMovement = `INSERT INTO account_movements (transaction_id, account_id, amount_minor)
                        VALUES (?, ?, ?)`

// seedTransaction returns the id of an opening-balance transaction.
func seedTransaction(t *testing.T, db *sql.DB) int64 {
	t.Helper()

	result, err := db.ExecContext(t.Context(), insertTransaction,
		"OPENING_BALANCE", nil, nil, nil, "2026-09-05")
	if err != nil {
		t.Fatalf("seeding transaction: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the transaction id: %v", err)
	}
	return id
}

func TestMovementAcceptsSignedAmounts(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	accountID := seedAccount(t, db, "Mercantil USD")

	for _, amount := range []int64{1, -1, 100000, -100000, math.MaxInt64, math.MinInt64} {
		txID := seedTransaction(t, db)
		if _, err := db.ExecContext(t.Context(), insertMovement, txID, accountID, amount); err != nil {
			t.Errorf("amount_minor %d was rejected: %v", amount, err)
		}
	}
}

func TestMovementConstraints(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	accountID := seedAccount(t, db, "Mercantil USD")
	txID := seedTransaction(t, db)

	// A zero movement asserts nothing: the sum over no rows is already zero.
	if _, err := db.ExecContext(ctx, insertMovement, txID, accountID, 0); err == nil {
		t.Error("a zero amount_minor was accepted")
	}
	// STRICT is what stops a monetary amount ever being stored as a float.
	if _, err := db.ExecContext(ctx, insertMovement, txID, accountID, 12.34); err == nil {
		t.Error("a REAL amount_minor was accepted")
	}
	if _, err := db.ExecContext(ctx, insertMovement, txID, accountID, nil); err == nil {
		t.Error("a NULL amount_minor was accepted")
	}
	if _, err := db.ExecContext(ctx, insertMovement, 99999, accountID, 100); err == nil {
		t.Error("a movement against a non-existent transaction was accepted")
	}
	if _, err := db.ExecContext(ctx, insertMovement, txID, 99999, 100); err == nil {
		t.Error("a movement against a non-existent account was accepted")
	}
	if _, err := db.ExecContext(ctx, insertMovement, nil, accountID, 100); err == nil {
		t.Error("a movement with no transaction was accepted")
	}
	if _, err := db.ExecContext(ctx, insertMovement, txID, nil, 100); err == nil {
		t.Error("a movement with no account was accepted")
	}
}

// An account with history cannot be deleted; it is archived instead.
func TestAccountWithMovementsCannotBeDeleted(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	accountID := seedAccount(t, db, "Mercantil USD")
	txID := seedTransaction(t, db)
	if _, err := db.ExecContext(ctx, insertMovement, txID, accountID, 100000); err != nil {
		t.Fatalf("creating the movement: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", accountID); err == nil {
		t.Error("an account with movements was deleted")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM transactions WHERE id = ?", txID); err == nil {
		t.Error("a transaction with movements was deleted")
	}
}

// One transaction may impact several accounts. Nothing today creates such a
// row, but a UNIQUE(transaction_id) added "because there is only ever one"
// would have to be migrated away from when transfers arrive.
func TestTransactionMayHaveSeveralMovements(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	from := seedAccount(t, db, "Mercantil USD")
	to := seedAccount(t, db, "Banesco USD")
	txID := seedTransaction(t, db)

	if _, err := db.ExecContext(ctx, insertMovement, txID, from, -100000); err != nil {
		t.Fatalf("first movement: %v", err)
	}
	if _, err := db.ExecContext(ctx, insertMovement, txID, to, 100000); err != nil {
		t.Errorf("a second movement on the same transaction was rejected: %v", err)
	}
}

// --- schema-wide invariants -------------------------------------------------

// The money rule, asserted against the schema itself rather than trusted to
// review: no business column anywhere may be REAL.
func TestNoBusinessColumnIsREAL(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	for _, table := range businessTables {
		rows, err := db.QueryContext(t.Context(),
			"SELECT name, type FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatalf("reading columns of %s: %v", table, err)
		}
		for rows.Next() {
			var name, columnType string
			if err := rows.Scan(&name, &columnType); err != nil {
				t.Fatalf("scanning column: %v", err)
			}
			if strings.EqualFold(columnType, "REAL") {
				t.Errorf("%s.%s is REAL; monetary precision cannot survive that", table, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterating columns of %s: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}
}

// Every business table is STRICT, which is what makes the column types above
// mean anything at all.
func TestEveryBusinessTableIsStrict(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	for _, table := range businessTables {
		var ddl string
		if err := db.QueryRowContext(t.Context(),
			"SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&ddl); err != nil {
			t.Fatalf("reading the definition of %s: %v", table, err)
		}
		if !strings.Contains(strings.ToUpper(ddl), "STRICT") {
			t.Errorf("%s is not STRICT", table)
		}
	}
}

// A stored balance would be a second source of truth that can drift from the
// movements it claims to summarise. The exact column sets above already forbid
// one; this states the rule by name, so a failure reads as what it is.
func TestNoStoredBalanceAnywhere(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	forbidden := []string{"balance", "current_balance", "opening_balance", "initial_balance", "total"}

	for _, table := range businessTables {
		for _, column := range columnNames(t, db, table) {
			for _, bad := range forbidden {
				if strings.EqualFold(column, bad) {
					t.Errorf("%s.%s stores a balance; balances are derived from account_movements", table, column)
				}
			}
		}
	}
}

var businessTables = []string{
	"currencies", "accounts", "categories", "payment_methods", "transactions", "account_movements",
	"exchange_rates", "app_settings",
	"savings_policy_versions", "savings_category_rules", "savings_income_overrides",
	"savings_suggestions", "savings_suggestion_rate_legs",
	"savings_operations", "savings_entries",
	"savings_goals", "savings_goal_target_revisions", "savings_issue_acknowledgements",
}

func TestExpectedIndexesExist(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	// Each of these answers a real query; see the comment above it in its
	// migration. An index without a query behind it is not added.
	want := []string{
		"idx_accounts_currency_code",
		"idx_categories_type_name",
		"idx_payment_methods_name",
		"idx_transactions_effective_date",
		"idx_transactions_category_id",
		"idx_transactions_payment_method_id",
		"idx_account_movements_account_id",
		"idx_account_movements_transaction_id",
		"idx_exchange_rates_observation",
		"idx_exchange_rates_lookup",
		"idx_exchange_rates_quote_currency_code",
		"idx_savings_policy_versions_effective_from",
		"idx_savings_suggestions_revision",
		"idx_savings_suggestions_live",
		"idx_savings_suggestions_policy_version_id",
		"idx_savings_suggestions_income_account_id",
		"idx_savings_suggestions_income_category_id",
		"idx_savings_suggestions_income_currency_code",
		"idx_savings_suggestion_rate_legs_exchange_rate_id",
		"idx_savings_suggestion_rate_legs_base_currency_code",
		"idx_savings_suggestion_rate_legs_quote_currency_code",
		"idx_savings_operations_request_key",
		"idx_savings_operations_suggestion_id",
		"idx_savings_operations_transaction_id",
		"idx_savings_operations_effective_date",
		"idx_savings_entries_operation_id",
		"idx_savings_entries_account_id",
		"idx_savings_goal_target_revisions_goal_id",
		"idx_savings_entries_goal_id",
	}

	for _, index := range want {
		var name string
		err := db.QueryRowContext(t.Context(),
			"SELECT name FROM sqlite_schema WHERE type = 'index' AND name = ?", index).Scan(&name)
		if err != nil {
			t.Errorf("index %s is missing: %v", index, err)
		}
	}
}

// The movement table must not gain a unique constraint on transaction_id: that
// is where transfers will live.
func TestTransactionIdIsNotUnique(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	var unique int
	err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM pragma_index_list('account_movements') WHERE "unique" = 1`).Scan(&unique)
	if err != nil {
		t.Fatalf("reading indexes of account_movements: %v", err)
	}
	if unique != 0 {
		t.Errorf("account_movements has %d unique index(es); one transaction must be able to impact several accounts", unique)
	}
}
