package migrations

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// The file 00019 is, and the five things it is checked for (TD-088, R6 of
// docs/m8-plan.md): an upgrade that carries every row of M7 across untouched; a
// schema in which only the two rebuilt tables changed; a body that can run twice;
// a guard that, when it fires, leaves the database exactly as it was; and a
// Down that refuses.
const m8RebuildFile = "00019_accounts_credit_card_transactions_refund.sql"

// seedM7Savings writes savings rows that reference accounts and transactions,
// which is exactly what the rebuild must not disturb: a policy, an override, a
// suggestion with its leg, a reservation with its entry, a goal.
func seedM7Savings(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO savings_policy_versions (id, rate_bp, effective_from) VALUES (1, 2000, '2026-09-01')`,
		`INSERT INTO savings_income_overrides (transaction_id, is_eligible, reason) VALUES (15, 0, 'Reembolso')`,
		`INSERT INTO savings_suggestions
		     (id, income_transaction_id, revision, policy_version_id, income_account_id, income_category_id,
		      income_amount_minor, income_currency_code, income_effective_date,
		      income_usd_minor, suggested_usd_minor, fx_method, valued_at, status, status_reason)
		 VALUES (1, 12, 1, 1, 1, 1, 150050, 'USD', '2026-09-03',
		         150050, 30010, 'IDENTITY', '2026-09-03T12:00:00.000Z', 'OPEN', NULL)`,
		`INSERT INTO savings_goals (id, name, goal_type, target_usd_minor, target_date, distribution_bp, status)
		 VALUES (1, 'Emergencias', 'PERMANENT', 100000, NULL, 10000, 'ACTIVE')`,
		`INSERT INTO savings_operations (id, kind, request_key, suggestion_id, transaction_id, reason, is_exception, effective_date)
		 VALUES (1, 'RESERVE', 'k-0000000000000001', 1, NULL, NULL, 0, '2026-09-03')`,
		`INSERT INTO savings_entries (id, operation_id, account_id, amount_minor, goal_id) VALUES (1, 1, 1, 30010, 1)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding M7 savings: %v", err)
		}
	}
}

// snapshotSavings reads every savings row as text, so that a comparison covers
// every column without naming them.
func snapshotSavings(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	tables := []string{
		"savings_policy_versions", "savings_income_overrides", "savings_suggestions",
		"savings_operations", "savings_entries", "savings_goals",
	}
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		var dump sql.NullString
		// quote() renders every value with its type, so 1 and '1' differ.
		var quoted []string
		for _, column := range strings.Split(savingsColumns(t, db, table), ",") {
			quoted = append(quoted, "quote("+column+")")
		}
		query := `SELECT group_concat(` + strings.Join(quoted, "||','||") + `, '|') FROM (SELECT * FROM ` + table + ` ORDER BY rowid)`
		if err := db.QueryRowContext(t.Context(), query).Scan(&dump); err != nil {
			t.Fatalf("snapshotting %s: %v", table, err)
		}
		out[table] = dump.String
	}
	return out
}

func savingsColumns(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing column rows: %v", err)
		}
	}()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning a column name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns: %v", err)
	}
	return strings.Join(names, ",")
}

type schemaObject struct {
	kind, table, sql string
}

// snapshotSchema reads sqlite_schema, keyed by object name.
func snapshotSchema(t *testing.T, db *sql.DB) map[string]schemaObject {
	t.Helper()
	rows, err := db.QueryContext(t.Context(),
		`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("reading sqlite_schema: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing schema rows: %v", err)
		}
	}()
	out := map[string]schemaObject{}
	for rows.Next() {
		var name string
		var o schemaObject
		if err := rows.Scan(&o.kind, &name, &o.table, &o.sql); err != nil {
			t.Fatalf("scanning sqlite_schema: %v", err)
		}
		out[name] = o
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating sqlite_schema: %v", err)
	}
	return out
}

func buildSchema18WithData(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := up(t.Context(), db, sourceUpTo(t, 18), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("building a schema 18 database: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)
	seedM5Reference(t, db)
	seedM7Savings(t, db)
}

// Upgrading a database that holds everything M7 can hold changes no row, leaves
// foreign keys enforced and the file sound, and changes the schema in exactly
// two places: the CHECK lists of accounts and transactions.
func TestUpgradeFromSchema18WithM7Data(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	buildSchema18WithData(t, db)

	beforeLedger := snapshotLedger(t, db)
	beforeSavings := snapshotSavings(t, db)
	beforeSchema := snapshotSchema(t, db)

	if err := Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("upgrading to M8.1: %v", err)
	}
	if got := appliedVersion(t, db); got != latestSchemaVersion {
		t.Errorf("the database is at version %d, want %d", got, latestSchemaVersion)
	}

	assertLedgerUnchanged(t, beforeLedger, snapshotLedger(t, db), "the M8.1 upgrade")
	afterSavings := snapshotSavings(t, db)
	for table, was := range beforeSavings {
		if is := afterSavings[table]; is != was {
			t.Errorf("%s is %q after the upgrade, was %q", table, is, was)
		}
	}
	assertSchemaIsSound(t, db)
	if err := database.CheckForeignKeys(ctx, db); err != nil {
		t.Errorf("after the upgrade: %v", err)
	}

	// Only the SQL of the two rebuilt tables may differ. Every index keeps its
	// name and its definition, every child table its REFERENCES clause.
	afterSchema := snapshotSchema(t, db)
	for name, was := range beforeSchema {
		is, ok := afterSchema[name]
		if !ok {
			t.Errorf("%s %q disappeared", was.kind, name)
			continue
		}
		if name == "accounts" || name == "transactions" {
			if is.sql == was.sql {
				t.Errorf("%s was not rebuilt", name)
			}
			continue
		}
		if is != was {
			t.Errorf("%s %q changed:\n was %q\n  is %q", was.kind, name, was.sql, is.sql)
		}
	}
	for name, is := range afterSchema {
		if _, existed := beforeSchema[name]; existed {
			continue
		}
		if !strings.HasPrefix(name, "credit_") && !strings.HasPrefix(name, "idx_credit_") &&
			!strings.Contains(name, "transaction_refunds") && !strings.Contains(name, "operation_requests") {
			t.Errorf("unexpected new %s %q", is.kind, name)
		}
	}
	for _, o := range afterSchema {
		if o.kind == "trigger" || o.kind == "view" {
			t.Errorf("the schema has a %s, which 00019 does not account for", o.kind)
		}
	}

	// The two new values are accepted and an old shape is still refused.
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (name, account_type, currency_code) VALUES ('Plaza Black', 'CREDIT_CARD', 'VES')`); err != nil {
		t.Errorf("CREDIT_CARD is refused after the upgrade: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO transactions (transaction_type, category_id, payment_method_id, effective_date) VALUES ('REFUND', 2, 1, '2026-10-02')`); err != nil {
		t.Errorf("REFUND is refused after the upgrade: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO transactions (transaction_type, category_id, payment_method_id, effective_date) VALUES ('REFUND', NULL, NULL, '2026-10-02')`); err == nil {
		t.Error("a REFUND without a category and a channel was accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (name, account_type, account_subtype, currency_code) VALUES ('x', 'CREDIT_CARD', 'CHECKING', 'VES')`); err == nil {
		t.Error("a CREDIT_CARD with a subtype was accepted")
	}
	// And the children still enforce their foreign keys against the new parents.
	if _, err := db.ExecContext(ctx, `INSERT INTO account_movements (transaction_id, account_id, amount_minor) VALUES (999, 1, 5)`); err == nil {
		t.Error("an orphan movement was accepted after the rebuild")
	}
}

// If the process dies between 00019's COMMIT and goose recording the version,
// the next start runs it again. Run twice, it produces the same schema and
// keeps every row.
func TestM8RebuildCanRunTwice(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	buildSchema18WithData(t, db)
	before := snapshotLedger(t, db)

	if err := up(ctx, db, sourceUpTo(t, 19), logger); err != nil {
		t.Fatalf("first run: %v", err)
	}
	once := snapshotSchema(t, db)

	if _, err := db.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id = 19`); err != nil {
		t.Fatalf("forgetting version 19: %v", err)
	}
	if err := up(ctx, db, sourceUpTo(t, 19), logger); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if twice := snapshotSchema(t, db); len(twice) != len(once) {
		t.Errorf("the schema has %d objects after the second run, %d after the first", len(twice), len(once))
	} else {
		for name, o := range once {
			if twice[name] != o {
				t.Errorf("%q differs between the first and the second run", name)
			}
		}
	}
	assertLedgerUnchanged(t, before, snapshotLedger(t, db), "running 00019 twice")
	assertSchemaIsSound(t, db)
	if err := database.CheckForeignKeys(ctx, db); err != nil {
		t.Error(err)
	}
}

// Each guard, made to fail: the migration stops with an error and, once the
// connection it left behind is closed, the database is exactly what it was --
// schema, rows, version -- and foreign keys are on again.
func TestM8RebuildGuardFailureLeavesTheDatabaseIntact(t *testing.T) {
	t.Parallel()

	for _, guard := range []string{
		"legacy_alter_table", "foreign_keys_off", "accounts_copied", "transactions_copied",
		"foreign_key_check", "integrity_check",
	} {
		t.Run(guard, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			logger := slog.New(slog.DiscardHandler)
			path := filepath.Join(t.TempDir(), "finance.db")

			db, err := database.Open(ctx, path)
			if err != nil {
				t.Fatalf("opening: %v", err)
			}
			buildSchema18WithData(t, db)
			beforeLedger := snapshotLedger(t, db)
			beforeSchema := snapshotSchema(t, db)

			source := sourceUpTo(t, 19).(fstest.MapFS)
			content := string(source[m8RebuildFile].Data)
			marker := "VALUES ('" + guard + "', "
			if !strings.Contains(content, marker) {
				t.Fatalf("00019 has no guard %q", guard)
			}
			source[m8RebuildFile] = &fstest.MapFile{Data: []byte(strings.Replace(content, marker, marker+"0 AND ", 1))}

			err = up(ctx, db, source, logger)
			if err == nil {
				t.Fatal("the migration succeeded with a failing guard")
			}
			if !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Errorf("the failure is %v, want the guard's CHECK", err)
			}
			// What cmd/web does on any migration error.
			if err := db.Close(); err != nil {
				t.Fatalf("closing: %v", err)
			}

			db, err = database.Open(ctx, path)
			if err != nil {
				t.Fatalf("reopening: %v", err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("closing: %v", err)
				}
			})
			if got := appliedVersion(t, db); got != 18 {
				t.Errorf("the database is at version %d after the failure, want 18", got)
			}
			assertLedgerUnchanged(t, beforeLedger, snapshotLedger(t, db), "a failed 00019")
			after := snapshotSchema(t, db)
			if len(after) != len(beforeSchema) {
				t.Errorf("the schema has %d objects after the failure, had %d", len(after), len(beforeSchema))
			}
			for name, was := range beforeSchema {
				if after[name] != was {
					t.Errorf("%q changed although the migration failed", name)
				}
			}
			if err := database.CheckForeignKeys(ctx, db); err != nil {
				t.Errorf("after reopening: %v", err)
			}
			assertSchemaIsSound(t, db)

			// And the real migration then succeeds on the same file.
			if err := Up(ctx, db, logger); err != nil {
				t.Fatalf("migrating after the failure: %v", err)
			}
			assertLedgerUnchanged(t, beforeLedger, snapshotLedger(t, db), "the retried upgrade")
		})
	}
}

// 00019 is forward-only; 00020 and 00021 roll back cleanly.
func TestM8MigrationsRollBack(t *testing.T) {
	t.Parallel()

	content, err := fs.ReadFile(migrationsFS, m8RebuildFile)
	if err != nil {
		t.Fatalf("reading %s: %v", m8RebuildFile, err)
	}
	_, down, _ := strings.Cut(string(content), "-- +goose Down")
	if !strings.Contains(down, "this_migration_is_forward_only") {
		t.Errorf("%s has a Down that does not refuse to run", m8RebuildFile)
	}
	if !strings.Contains(string(content), "-- +goose NO TRANSACTION") {
		t.Errorf("%s must run outside goose's transaction: its PRAGMA is a no-op inside one", m8RebuildFile)
	}

	db := newRawDB(t)
	ctx := t.Context()
	buildSchema18WithData(t, db)
	before := snapshotLedger(t, db)
	if err := Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	for _, file := range []string{
		"00021_create_credit_charges_refunds_requests.sql",
		"00020_create_credit_cards.sql",
	} {
		for _, statement := range downStatementsFromFS(t, file) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s Down failed on %q: %v", file, statement, err)
			}
		}
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
		WHERE name LIKE '%credit_%' OR name LIKE '%transaction_refunds%' OR name LIKE '%operation_requests%'`).Scan(&remaining); err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d M8 objects survived the rollback", remaining)
	}
	assertLedgerUnchanged(t, before, snapshotLedger(t, db), "the rollback")
	assertSchemaIsSound(t, db)
}
