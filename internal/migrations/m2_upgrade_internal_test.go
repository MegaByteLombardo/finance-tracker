package migrations

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// This is the migration test that matters.
//
// Applying M3 to an empty database proves almost nothing: the interesting case
// is a schema 7 database with real transactions in it, because that is what is
// on the machine this runs on. The M3 migrations rebuild the two central ledger
// tables, and a mistake there does not fail loudly -- it silently drops
// movements, or detaches a transaction from how it was paid.
//
// The source for versions 1-7 is the real migration files, read out of the
// embedded filesystem, so this exercises the schema that actually shipped rather
// than a hand-written approximation of it.
func TestUpgradeFromSchema7WithRealData(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	// Stop at 7: exactly where M2 left the database.
	if err := up(ctx, db, sourceUpTo(t, 7), logger); err != nil {
		t.Fatalf("building a schema 7 database: %v", err)
	}
	if got := appliedVersion(t, db); got != 7 {
		t.Fatalf("the database is at version %d, want 7", got)
	}

	seedM2Ledger(t, db)

	before := snapshotLedger(t, db)
	if len(before.transactions) != 5 || len(before.movements) != 5 {
		t.Fatalf("the fixture has %d transactions and %d movements, want 5 and 5",
			len(before.transactions), len(before.movements))
	}

	// The upgrade under test.
	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading to M3: %v", err)
	}

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got := appliedVersion(t, db); got != latest {
		t.Errorf("the database is at version %d after upgrading, want %d", got, latest)
	}

	// --- nothing was lost --------------------------------------------------

	after := snapshotLedger(t, db)

	if len(after.transactions) != len(before.transactions) {
		t.Fatalf("%d transactions survived, want %d", len(after.transactions), len(before.transactions))
	}
	for id, was := range before.transactions {
		is, ok := after.transactions[id]
		if !ok {
			t.Errorf("transaction %d disappeared", id)
			continue
		}
		if is.txType != was.txType {
			t.Errorf("transaction %d: type %q, was %q", id, is.txType, was.txType)
		}
		if is.categoryID != was.categoryID {
			t.Errorf("transaction %d: category %v, was %v", id, is.categoryID, was.categoryID)
		}
		if is.description != was.description {
			t.Errorf("transaction %d: description %v, was %v", id, is.description, was.description)
		}
		if is.effectiveDate != was.effectiveDate {
			t.Errorf("transaction %d: effective_date %q, was %q", id, is.effectiveDate, was.effectiveDate)
		}
		if is.createdAt != was.createdAt || is.updatedAt != was.updatedAt {
			t.Errorf("transaction %d: timestamps %q/%q, were %q/%q",
				id, is.createdAt, is.updatedAt, was.createdAt, was.updatedAt)
		}
	}

	if len(after.movements) != len(before.movements) {
		t.Fatalf("%d movements survived, want %d", len(after.movements), len(before.movements))
	}
	for id, was := range before.movements {
		is, ok := after.movements[id]
		if !ok {
			t.Errorf("movement %d disappeared", id)
			continue
		}
		if is != was {
			t.Errorf("movement %d = %+v, was %+v", id, is, was)
		}
	}

	for account, want := range before.balances {
		if got := after.balances[account]; got != want {
			t.Errorf("account %d balance = %d, was %d", account, got, want)
		}
	}

	// --- every transaction kept its payment method -------------------------

	wantMethod := map[int64]string{
		10: "",              // OPENING_BALANCE has none
		11: "Bank transfer", // was BANK_TRANSFER
		12: "Point of sale", // was POS
		13: "Cash",          // was CASH
		14: "Other",         // was OTHER
	}
	for id, want := range wantMethod {
		var name sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT pm.name FROM transactions t
			  LEFT JOIN payment_methods pm ON pm.id = t.payment_method_id
			 WHERE t.id = ?`, id).Scan(&name)
		if err != nil {
			t.Errorf("reading the method of transaction %d: %v", id, err)
			continue
		}
		if want == "" {
			if name.Valid {
				t.Errorf("transaction %d has method %q, want none", id, name.String)
			}
			continue
		}
		if !name.Valid || name.String != want {
			t.Errorf("transaction %d has method %v, want %q", id, name, want)
		}
	}

	// The four M2 methods exist, and nothing else was invented.
	var methods int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM payment_methods").Scan(&methods); err != nil {
		t.Fatalf("counting payment methods: %v", err)
	}
	if methods != 4 {
		t.Errorf("%d payment methods exist after migrating, want the 4 M2 had", methods)
	}

	// --- the schema is sound ----------------------------------------------

	var violations int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		t.Fatalf("running foreign_key_check: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d foreign key violations after migrating", violations)
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("running integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrity)
	}

	// Foreign keys must never have been switched off to get here: the rebuild
	// is ordered so that it does not need to be.
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("reading foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Error("foreign_keys is off after migrating")
	}

	// And the constraint really is live on the rebuilt tables.
	_, err = db.ExecContext(ctx, `
		INSERT INTO account_movements (transaction_id, account_id, amount_minor, created_at)
		VALUES (9999, 1, 100, '2026-09-05T11:00:00.000Z')`)
	if err == nil {
		t.Error("a movement against a non-existent transaction was accepted after the rebuild")
	}
}

// The M3 migrations are forward-only, so their Down must fail rather than
// quietly mark them reverted.
func TestM3MigrationsAreForwardOnly(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"00008_create_payment_methods.sql",
		"00009_transactions_payment_method_id.sql",
	} {
		content, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		_, down, found := strings.Cut(string(content), "-- +goose Down")
		if !found {
			t.Errorf("%s has no Down section at all", name)
			continue
		}
		if !strings.Contains(down, "this_migration_is_forward_only") {
			t.Errorf("%s has a Down that does not refuse to run", name)
		}
	}
}

// sourceUpTo builds a migration source holding the real files up to a version.
func sourceUpTo(t *testing.T, version int64) fs.FS {
	t.Helper()

	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}

	source := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		digits, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || v > version {
			continue
		}
		content, err := fs.ReadFile(migrationsFS, entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		source[entry.Name()] = &fstest.MapFile{Data: content}
	}

	if len(source) != int(version) {
		t.Fatalf("built a source with %d migrations, want %d", len(source), version)
	}
	return source
}

func appliedVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()

	var version int64
	err := db.QueryRowContext(t.Context(),
		"SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1").Scan(&version)
	if err != nil {
		t.Fatalf("reading the applied version: %v", err)
	}
	return version
}

// seedM2Ledger writes a ledger that uses every M2 payment method plus an
// opening balance, across two accounts and two currencies.
func seedM2Ledger(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,institution,is_active,created_at,updated_at)
		 VALUES (1,'Mercantil USD','BANK','CHECKING','USD','Mercantil',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z'),
		        (2,'Efectivo VES','CASH',NULL,'VES',NULL,1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z')`,

		`INSERT INTO categories (id,name,category_type,is_active,created_at,updated_at)
		 VALUES (1,'Salario','INCOME',1,'2026-09-01T10:00:02.000Z','2026-09-01T10:00:02.000Z'),
		        (2,'Comida','EXPENSE',1,'2026-09-01T10:00:03.000Z','2026-09-01T10:00:03.000Z')`,

		`INSERT INTO transactions (id,transaction_type,category_id,payment_method,description,effective_date,created_at,updated_at)
		 VALUES (10,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:00.000Z','2026-09-01T11:00:00.000Z'),
		        (11,'INCOME',1,'BANK_TRANSFER','Sueldo','2026-09-03','2026-09-03T11:00:00.000Z','2026-09-03T11:00:00.000Z'),
		        (12,'EXPENSE',2,'POS',NULL,'2026-09-04','2026-09-04T11:00:00.000Z','2026-09-04T11:00:00.000Z'),
		        (13,'EXPENSE',2,'CASH','Almuerzo','2026-09-04','2026-09-04T12:00:00.000Z','2026-09-04T12:00:00.000Z'),
		        (14,'INCOME',1,'OTHER',NULL,'2026-09-05','2026-09-05T11:00:00.000Z','2026-09-05T11:00:00.000Z')`,

		`INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		 VALUES (100,10,1,100000,'2026-09-01T11:00:00.000Z'),
		        (101,11,1,150050,'2026-09-03T11:00:00.000Z'),
		        (102,12,1,-2599,'2026-09-04T11:00:00.000Z'),
		        (103,13,2,-500000,'2026-09-04T12:00:00.000Z'),
		        (104,14,1,7500,'2026-09-05T11:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding the M2 ledger: %v", err)
		}
	}
}

type transactionRow struct {
	txType        string
	categoryID    sql.NullInt64
	description   sql.NullString
	effectiveDate string
	createdAt     string
	updatedAt     string
}

type movementRow struct {
	transactionID int64
	accountID     int64
	amountMinor   int64
	createdAt     string
}

type ledgerSnapshot struct {
	transactions map[int64]transactionRow
	movements    map[int64]movementRow
	balances     map[int64]int64
}

// snapshotLedger reads everything the migration must preserve.
func snapshotLedger(t *testing.T, db *sql.DB) ledgerSnapshot {
	t.Helper()
	ctx := t.Context()

	snapshot := ledgerSnapshot{
		transactions: map[int64]transactionRow{},
		movements:    map[int64]movementRow{},
		balances:     map[int64]int64{},
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, transaction_type, category_id, description, effective_date, created_at, updated_at
		  FROM transactions ORDER BY id`)
	if err != nil {
		t.Fatalf("reading transactions: %v", err)
	}
	for rows.Next() {
		var id int64
		var r transactionRow
		if err := rows.Scan(&id, &r.txType, &r.categoryID, &r.description,
			&r.effectiveDate, &r.createdAt, &r.updatedAt); err != nil {
			t.Fatalf("scanning a transaction: %v", err)
		}
		snapshot.transactions[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating transactions: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closing transaction rows: %v", err)
	}

	rows, err = db.QueryContext(ctx, `
		SELECT id, transaction_id, account_id, amount_minor, created_at
		  FROM account_movements ORDER BY id`)
	if err != nil {
		t.Fatalf("reading movements: %v", err)
	}
	for rows.Next() {
		var id int64
		var r movementRow
		if err := rows.Scan(&id, &r.transactionID, &r.accountID, &r.amountMinor, &r.createdAt); err != nil {
			t.Fatalf("scanning a movement: %v", err)
		}
		snapshot.movements[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating movements: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closing movement rows: %v", err)
	}

	rows, err = db.QueryContext(ctx, `
		SELECT account_id, SUM(amount_minor) FROM account_movements GROUP BY account_id`)
	if err != nil {
		t.Fatalf("reading balances: %v", err)
	}
	for rows.Next() {
		var account, balance int64
		if err := rows.Scan(&account, &balance); err != nil {
			t.Fatalf("scanning a balance: %v", err)
		}
		snapshot.balances[account] = balance
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating balances: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closing balance rows: %v", err)
	}

	return snapshot
}
