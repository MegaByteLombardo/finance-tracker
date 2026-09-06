package migrations_test

import (
	"os"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

// Down never runs automatically, and there is no CLI to invoke it — so nothing
// else in this suite would notice if a Down statement were simply wrong.
//
// That is tolerable while every Down is a DROP. It stopped being tolerable at
// migration 00004, whose Down is a full table rebuild: it recreates accounts
// without the subtype column, copies the rows through, drops, renames and
// recreates the index. A mistake anywhere in that sequence would only be
// discovered by someone rolling back a database they cared about.
//
// This test therefore applies every M2 Down, newest first, exactly as goose
// would, and checks the schema and the data that come out the other side.
//
// It splits statements on ";" rather than reusing goose's parser, which is
// naive but sufficient: no Down here contains a semicolon inside a string
// literal. If one ever does, this fails loudly rather than silently skipping.
//
// 00008 and 00009 are deliberately absent. They are forward-only: after M3 a
// transaction can reference a payment method M2's four-value enumeration cannot
// spell, so no Down could put the schema back without discarding information.
// Their refusal to roll back is covered by TestM3MigrationsAreForwardOnly, and
// the supported way back is restoring the pre-migration snapshot.
func TestDownStatementsRollBackCleanly(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	// Two accounts, one of each subtype shape, so the rebuild has real rows to
	// carry across.
	if _, err := db.ExecContext(ctx, insertAccount,
		"Mercantil USD", "BANK", "CHECKING", "USD", "Mercantil", 1); err != nil {
		t.Fatalf("seeding the bank account: %v", err)
	}
	if _, err := db.ExecContext(ctx, insertAccount,
		"Efectivo USD", "CASH", nil, "USD", nil, 1); err != nil {
		t.Fatalf("seeding the cash account: %v", err)
	}

	// Newest first, which is the order goose applies them in.
	for _, file := range []string{
		"00007_create_account_movements.sql",
		"00006_create_transactions.sql",
		"00005_create_categories.sql",
		"00004_add_account_subtype.sql",
	} {
		for _, statement := range downStatements(t, file) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s Down failed on %q: %v", file, statement, err)
			}
		}
	}

	// The schema is back to what M1 left behind.
	want := []string{
		"id", "name", "account_type", "currency_code", "institution",
		"is_active", "created_at", "updated_at",
	}
	got := columnNames(t, db, "accounts")
	if len(got) != len(want) {
		t.Fatalf("accounts has %v after the rollback, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("accounts column %d = %q, want %q", i, got[i], want[i])
		}
	}

	for _, table := range []string{"categories", "transactions", "account_movements"} {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err == nil {
			t.Errorf("%s survived the rollback", table)
		}
	}

	// The rebuild carried the rows across rather than dropping them.
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		t.Fatalf("counting accounts: %v", err)
	}
	if count != 2 {
		t.Errorf("%d accounts survived the rollback, want 2", count)
	}

	// And the index the rebuild dropped along with the old table was recreated.
	// Without it every foreign key check would silently start scanning.
	var index string
	if err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_schema WHERE type = 'index' AND name = 'idx_accounts_currency_code'").
		Scan(&index); err != nil {
		t.Errorf("idx_accounts_currency_code is missing after the rollback: %v", err)
	}
}

// downStatements returns the statements in a migration's Down section.
func downStatements(t *testing.T, file string) []string {
	t.Helper()

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	_, down, found := strings.Cut(string(content), "-- +goose Down")
	if !found {
		t.Fatalf("%s has no Down section", file)
	}

	var statements []string
	for _, statement := range strings.Split(down, ";") {
		if strings.TrimSpace(stripSQLComments(statement)) == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

func stripSQLComments(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
