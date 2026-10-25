package migrations

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
)

// This is M4's version of the migration test that matters.
//
// Applying 00010 to an empty database proves almost nothing. The interesting
// case is a schema 9 database with real transactions in it, because that is what
// is on the machine this runs on. 00010 rebuilds both central ledger tables to
// widen a CHECK constraint, and a mistake there does not fail loudly -- it
// silently drops movements, renumbers transactions, or detaches one from its
// category or its payment method.
//
// The source for versions 1-9 is the real migration files, read out of the
// embedded filesystem, so this exercises the schema that actually shipped with
// M3 rather than a hand-written approximation of it.
func TestUpgradeFromSchema9WithRealData(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	// Stop at 9: exactly where M3 left the database.
	if err := up(ctx, db, sourceUpTo(t, 9), logger); err != nil {
		t.Fatalf("building a schema 9 database: %v", err)
	}
	if got := appliedVersion(t, db); got != 9 {
		t.Fatalf("the database is at version %d, want 9", got)
	}

	seedM3Ledger(t, db)

	before := snapshotLedger(t, db)
	if len(before.transactions) != 6 || len(before.movements) != 6 {
		t.Fatalf("the fixture has %d transactions and %d movements, want 6 and 6",
			len(before.transactions), len(before.movements))
	}
	beforeMethods := snapshotPaymentMethodIDs(t, db)

	// The upgrade under test.
	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading to M4: %v", err)
	}

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != latestSchemaVersion {
		t.Errorf("the latest embedded migration is %d, want %d", latest, latestSchemaVersion)
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
		if is != was {
			t.Errorf("transaction %d = %+v, was %+v", id, is, was)
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

	// The payment method each transaction was filed under is part of what the
	// rebuild had to carry, and it is stored as a foreign key, so it survives
	// only if both tables came across intact.
	afterMethods := snapshotPaymentMethodIDs(t, db)
	if len(afterMethods) != len(beforeMethods) {
		t.Fatalf("%d transactions have a method reading, want %d", len(afterMethods), len(beforeMethods))
	}
	for id, was := range beforeMethods {
		if is := afterMethods[id]; is != was {
			t.Errorf("transaction %d payment_method_id = %v, was %v", id, is, was)
		}
	}

	var methods int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM payment_methods").Scan(&methods); err != nil {
		t.Fatalf("counting payment methods: %v", err)
	}
	// The four seeded by 00008 plus the one the fixture's user created.
	if methods != 5 {
		t.Errorf("%d payment methods exist after migrating, want the 5 the fixture had", methods)
	}

	// --- the schema is sound ----------------------------------------------

	assertSchemaIsSound(t, db)

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

	// --- every index came back --------------------------------------------

	for _, index := range []string{
		"idx_transactions_effective_date",
		"idx_transactions_category_id",
		"idx_transactions_payment_method_id",
		"idx_account_movements_account_id",
		"idx_account_movements_transaction_id",
	} {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_schema WHERE type = 'index' AND name = ?", index).Scan(&name)
		if err != nil {
			t.Errorf("index %s is missing after the rebuild: %v", index, err)
		}
	}

	// --- and the point of the whole migration ------------------------------

	if _, err := db.ExecContext(ctx, `
		INSERT INTO transactions (id, transaction_type, category_id, payment_method_id,
		                          description, effective_date, created_at, updated_at)
		VALUES (20,'TRANSFER',NULL,NULL,'Pasar dinero a efectivo','2026-09-06',
		        '2026-09-06T11:00:00.000Z','2026-09-06T11:00:00.000Z')`); err != nil {
		t.Errorf("a TRANSFER was rejected after migrating: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_movements (id, transaction_id, account_id, amount_minor, created_at)
		VALUES (200,20,1,-25000,'2026-09-06T11:00:00.000Z'),
		       (201,20,2, 25000,'2026-09-06T11:00:00.000Z')`); err != nil {
		t.Errorf("a transfer's two movements were rejected after migrating: %v", err)
	}

	assertSchemaIsSound(t, db)
}

// Applying the migrations twice must be a no-op. Startup calls Up on every
// start, so this is the ordinary case rather than an edge one.
func TestUpgradeToM4IsIdempotent(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := up(ctx, db, sourceUpTo(t, 9), logger); err != nil {
		t.Fatalf("building a schema 9 database: %v", err)
	}
	seedM3Ledger(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	first := snapshotLedger(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	second := snapshotLedger(t, db)

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got := appliedVersion(t, db); got != latest {
		t.Errorf("the database is at version %d after re-running, want %d", got, latest)
	}
	if len(second.transactions) != len(first.transactions) {
		t.Errorf("%d transactions after re-running, want %d",
			len(second.transactions), len(first.transactions))
	}
	if len(second.movements) != len(first.movements) {
		t.Errorf("%d movements after re-running, want %d",
			len(second.movements), len(first.movements))
	}
	for id, was := range first.movements {
		if is := second.movements[id]; is != was {
			t.Errorf("movement %d = %+v after re-running, was %+v", id, is, was)
		}
	}
}

// The M4 migration is forward-only, so its Down must fail rather than quietly
// mark it reverted.
func TestM4MigrationIsForwardOnly(t *testing.T) {
	t.Parallel()

	const name = "00010_transactions_allow_transfer.sql"

	content, err := fs.ReadFile(migrationsFS, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	_, down, found := strings.Cut(string(content), "-- +goose Down")
	if !found {
		t.Fatalf("%s has no Down section at all", name)
	}
	if !strings.Contains(down, "this_migration_is_forward_only") {
		t.Errorf("%s has a Down that does not refuse to run", name)
	}
}

// assertSchemaIsSound runs the two checks that catch a botched rebuild.
func assertSchemaIsSound(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()

	var violations int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		t.Fatalf("running foreign_key_check: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d foreign key violations", violations)
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("running integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrity)
	}
}

// seedM3Ledger writes a ledger in the shape M3 actually produces: transactions
// pointing at payment_methods by id, across three accounts and two currencies,
// including an opening balance and a negative one.
func seedM3Ledger(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,institution,is_active,created_at,updated_at)
		 VALUES (1,'Mercantil USD','BANK','CHECKING','USD','Mercantil',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z'),
		        (2,'Efectivo USD','CASH',NULL,'USD',NULL,1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z'),
		        (3,'Banesco VES','BANK','SAVINGS','VES','Banesco',1,'2026-09-01T10:00:02.000Z','2026-09-01T10:00:02.000Z')`,

		`INSERT INTO categories (id,name,category_type,is_active,created_at,updated_at)
		 VALUES (1,'Salario','INCOME',1,'2026-09-01T10:00:03.000Z','2026-09-01T10:00:03.000Z'),
		        (2,'Comida','EXPENSE',1,'2026-09-01T10:00:04.000Z','2026-09-01T10:00:04.000Z')`,

		// A payment method the user created after M3 shipped, to prove the
		// rebuild carries more than the four seeded ones.
		`INSERT INTO payment_methods (id,name,is_active,created_at,updated_at)
		 VALUES (5,'Pago móvil',1,'2026-09-02T10:00:00.000Z','2026-09-02T10:00:00.000Z')`,

		`INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,description,effective_date,created_at,updated_at)
		 VALUES (10,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:00.000Z','2026-09-01T11:00:00.000Z'),
		        (11,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:01.000Z','2026-09-01T11:00:01.000Z'),
		        (12,'INCOME',1,1,'Sueldo','2026-09-03','2026-09-03T11:00:00.000Z','2026-09-03T11:00:00.000Z'),
		        (13,'EXPENSE',2,2,NULL,'2026-09-04','2026-09-04T11:00:00.000Z','2026-09-04T11:00:00.000Z'),
		        (14,'EXPENSE',2,3,'Almuerzo','2026-09-04','2026-09-04T12:00:00.000Z','2026-09-04T12:00:30.000Z'),
		        (15,'INCOME',1,5,NULL,'2026-09-05','2026-09-05T11:00:00.000Z','2026-09-05T11:00:00.000Z')`,

		`INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		 VALUES (100,10,1,100000,'2026-09-01T11:00:00.000Z'),
		        (101,11,2,-2500,'2026-09-01T11:00:01.000Z'),
		        (102,12,1,150050,'2026-09-03T11:00:00.000Z'),
		        (103,13,1,-2599,'2026-09-04T11:00:00.000Z'),
		        (104,14,3,-500000,'2026-09-04T12:00:00.000Z'),
		        (105,15,2,7500,'2026-09-05T11:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding the M3 ledger: %v", err)
		}
	}
}

// snapshotPaymentMethodIDs records which method each transaction is filed under.
//
// snapshotLedger predates payment_method_id and does not read it; rather than
// change what the M2 test asserts, M4 reads the column it cares about here.
func snapshotPaymentMethodIDs(t *testing.T, db *sql.DB) map[int64]sql.NullInt64 {
	t.Helper()

	rows, err := db.QueryContext(t.Context(),
		`SELECT id, payment_method_id FROM transactions ORDER BY id`)
	if err != nil {
		t.Fatalf("reading payment methods: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing payment method rows: %v", err)
		}
	}()

	methods := map[int64]sql.NullInt64{}
	for rows.Next() {
		var (
			id     int64
			method sql.NullInt64
		)
		if err := rows.Scan(&id, &method); err != nil {
			t.Fatalf("scanning a payment method: %v", err)
		}
		methods[id] = method
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating payment methods: %v", err)
	}
	return methods
}
