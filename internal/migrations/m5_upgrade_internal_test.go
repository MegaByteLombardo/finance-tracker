package migrations

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
)

// M5's version of the migration test that matters.
//
// 00011 and 00012 are purely additive, which is exactly why this test exists
// rather than being waved away: "additive" is a claim about the migration, and
// the claim is only worth anything if something checks it. A migration that
// quietly rebuilt, renumbered or dropped a ledger row would not fail loudly, and
// the database this runs against on the real machine holds accounts, transfers
// and balances that cannot be reconstructed.
//
// The starting point is a schema 10 database built from the REAL migration
// files, with an M4 ledger in it -- including a transfer, which is one
// transaction with two movements and the thing most likely to be damaged by a
// careless rebuild.
func TestUpgradeFromSchema10WithRealData(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	// Stop at 10: exactly where M4 left the database.
	if err := up(ctx, db, sourceUpTo(t, 10), logger); err != nil {
		t.Fatalf("building a schema 10 database: %v", err)
	}
	if got := appliedVersion(t, db); got != 10 {
		t.Fatalf("the database is at version %d, want 10", got)
	}

	seedM3Ledger(t, db)
	seedM4Transfer(t, db)

	before := snapshotLedger(t, db)
	if len(before.transactions) != 7 || len(before.movements) != 8 {
		t.Fatalf("the fixture has %d transactions and %d movements, want 7 and 8",
			len(before.transactions), len(before.movements))
	}
	beforeMethods := snapshotPaymentMethodIDs(t, db)

	// The upgrade under test.
	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading to M5: %v", err)
	}

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 12 {
		t.Errorf("the latest embedded migration is %d, want 12", latest)
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
			t.Errorf("transaction %d changed:\n before %+v\n  after %+v", id, was, is)
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
			t.Errorf("movement %d changed:\n before %+v\n  after %+v", id, was, is)
		}
	}

	// The figure a person would actually notice.
	for account, was := range before.balances {
		if is := after.balances[account]; is != was {
			t.Errorf("the balance of account %d is %d, was %d", account, is, was)
		}
	}

	afterMethods := snapshotPaymentMethodIDs(t, db)
	for id, was := range beforeMethods {
		if is := afterMethods[id]; is != was {
			t.Errorf("transaction %d now points at payment method %v, was %v", id, is, was)
		}
	}

	assertSchemaIsSound(t, db)

	// --- and the transfer is still a transfer ------------------------------
	//
	// Two movements, opposite signs, equal magnitude, two different accounts.
	// The M4 invariant restated here because M5 is the first milestone that
	// could have broken it without touching the ledger code.
	var movements, distinctAccounts int
	var total, magnitude int64
	err = db.QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT account_id), SUM(amount_minor), SUM(abs(amount_minor))
		  FROM account_movements WHERE transaction_id = 16`).
		Scan(&movements, &distinctAccounts, &total, &magnitude)
	if err != nil {
		t.Fatalf("reading the transfer's movements: %v", err)
	}
	if movements != 2 {
		t.Errorf("the transfer has %d movements, want 2", movements)
	}
	if distinctAccounts != 2 {
		t.Errorf("the transfer touches %d accounts, want 2", distinctAccounts)
	}
	if total != 0 {
		t.Errorf("the transfer's movements sum to %d, want 0", total)
	}
	if magnitude != 50000 {
		t.Errorf("the transfer moved %d in total magnitude, want 50000", magnitude)
	}

	// --- what M5 added -----------------------------------------------------

	for _, table := range []string{"exchange_rates", "app_settings"} {
		var name string
		if err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&name); err != nil {
			t.Errorf("%s was not created: %v", table, err)
		}
	}

	// No rate is invented by the migration. An empty table is the correct
	// starting state: the application has never fetched or been told anything.
	var rates int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM exchange_rates").Scan(&rates); err != nil {
		t.Fatalf("counting exchange rates: %v", err)
	}
	if rates != 0 {
		t.Errorf("the migration created %d exchange rates, want 0", rates)
	}

	// The settings row, on the other hand, must exist: every later read is a
	// plain SELECT with no "or insert a default" branch.
	var settings int
	var base string
	if err := db.QueryRowContext(ctx,
		"SELECT count(*), max(base_currency_code) FROM app_settings").Scan(&settings, &base); err != nil {
		t.Fatalf("reading app_settings: %v", err)
	}
	if settings != 1 {
		t.Errorf("app_settings has %d rows, want exactly 1", settings)
	}
	if base != "USD" {
		t.Errorf("the seeded valuation base is %q, want USD", base)
	}
}

// TestUpgradeToM5IsIdempotent proves a second start does nothing, which is what
// makes running migrations on every boot safe.
func TestUpgradeToM5IsIdempotent(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := up(ctx, db, sourceUpTo(t, 10), logger); err != nil {
		t.Fatalf("building a schema 10 database: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	first := snapshotLedger(t, db)

	// A user changes the valuation base between the two starts; the second run
	// must not reset it, because the INSERT in 00012 must not run twice.
	if _, err := db.ExecContext(ctx,
		"UPDATE app_settings SET base_currency_code = 'EUR' WHERE id = 1"); err != nil {
		t.Fatalf("changing the valuation base: %v", err)
	}

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}

	second := snapshotLedger(t, db)
	if len(second.transactions) != len(first.transactions) ||
		len(second.movements) != len(first.movements) {
		t.Errorf("the second run changed the ledger: %d/%d transactions, %d/%d movements",
			len(second.transactions), len(first.transactions),
			len(second.movements), len(first.movements))
	}

	var base string
	var rows int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*), max(base_currency_code) FROM app_settings").Scan(&rows, &base); err != nil {
		t.Fatalf("reading app_settings: %v", err)
	}
	if rows != 1 {
		t.Errorf("app_settings has %d rows after two runs, want 1", rows)
	}
	if base != "EUR" {
		t.Errorf("the second run reset the valuation base to %q; it must leave it at EUR", base)
	}
}

// TestM5MigrationsAreReversible is the counterpart to TestM4MigrationIsForwardOnly.
//
// 00008 to 00010 refuse to roll back because they reinterpret data that has no
// representation in the older schema. 00011 and 00012 only add tables, so their
// Down genuinely restores the schema they found, and a Down that claimed to be
// forward-only here would be cargo cult rather than caution.
func TestM5MigrationsAreReversible(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := up(ctx, db, sourceUpTo(t, 10), logger); err != nil {
		t.Fatalf("building a schema 10 database: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)
	before := snapshotLedger(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading: %v", err)
	}

	// Applied newest first, which is the order goose would use. The statements
	// are taken from the files themselves so a mistake in one is caught here
	// rather than by someone rolling back a database they cared about.
	for _, file := range []string{
		"00012_create_app_settings.sql",
		"00011_create_exchange_rates.sql",
	} {
		for _, statement := range downStatementsFromFS(t, file) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s Down failed on %q: %v", file, statement, err)
			}
		}
	}

	for _, table := range []string{"exchange_rates", "app_settings"} {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err == nil {
			t.Errorf("%s survived the rollback", table)
		}
	}

	// The ledger is untouched by both directions.
	after := snapshotLedger(t, db)
	if len(after.transactions) != len(before.transactions) ||
		len(after.movements) != len(before.movements) {
		t.Errorf("the rollback changed the ledger")
	}
	for account, was := range before.balances {
		if is := after.balances[account]; is != was {
			t.Errorf("the balance of account %d is %d after the rollback, was %d", account, is, was)
		}
	}

	assertSchemaIsSound(t, db)
}

// downStatementsFromFS returns the statements in a migration's Down section,
// read out of the EMBEDDED filesystem.
//
// The M2 rollback test reads the same files from disk, because it lives in the
// external test package and cannot see migrationsFS. Reading the embedded copy
// here is the stricter of the two: it exercises the SQL that actually ships in
// the binary rather than the file sitting beside it.
//
// Splitting on ";" is naive but sufficient, for the same reason it is there: no
// Down section contains a semicolon inside a string literal, and if one ever
// does this fails loudly instead of silently skipping a statement.
func downStatementsFromFS(t *testing.T, name string) []string {
	t.Helper()

	content, err := fs.ReadFile(migrationsFS, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	_, down, found := strings.Cut(string(content), "-- +goose Down")
	if !found {
		t.Fatalf("%s has no Down section", name)
	}

	var statements []string
	for _, statement := range strings.Split(down, ";") {
		var kept []string
		for _, line := range strings.Split(statement, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			kept = append(kept, line)
		}
		if strings.TrimSpace(strings.Join(kept, "\n")) == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

// seedM4Transfer adds the transaction shape M4 introduced: one transaction, two
// movements, equal and opposite, in two accounts of the same currency.
func seedM4Transfer(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,description,effective_date,created_at,updated_at)
		 VALUES (16,'TRANSFER',NULL,NULL,'A Efectivo','2026-09-06','2026-09-06T11:00:00.000Z','2026-09-06T11:00:00.000Z')`,

		`INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		 VALUES (106,16,1,-25000,'2026-09-06T11:00:00.000Z'),
		        (107,16,2, 25000,'2026-09-06T11:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding the M4 transfer: %v", err)
		}
	}
}
