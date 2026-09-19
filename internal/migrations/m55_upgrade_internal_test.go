package migrations

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
)

// The M5.5 migration rebuilds transactions and account_movements to admit
// FX_EXCHANGE. A rebuild is the operation most able to lose data quietly, and
// this database holds a ledger, so the upgrade is exercised against a schema 12
// database carrying everything the previous four milestones could put in it: an
// M3 ledger, an M4 transfer, M5 exchange rates and the app settings row.
//
// What it proves is the same thing the M3 and M5 upgrade tests prove, in the
// same shape: nothing is renumbered, no balance moves, the foreign keys survive
// the rename, and the new type is accepted afterwards while the invented ones
// are not.
func TestUpgradeFromSchema12WithRealData(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := up(ctx, db, sourceUpTo(t, 12), logger); err != nil {
		t.Fatalf("building a schema 12 database: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)
	seedM5Reference(t, db)

	before := snapshotLedger(t, db)
	beforeRates := snapshotRates(t, db)

	// The upgrade under test.
	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading to M5.5: %v", err)
	}

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 13 {
		t.Errorf("the latest embedded migration is %d, want 13", latest)
	}
	if got := appliedVersion(t, db); got != latest {
		t.Errorf("the database is at version %d after upgrading, want %d", got, latest)
	}

	// --- nothing was lost --------------------------------------------------

	after := snapshotLedger(t, db)

	if len(after.transactions) != len(before.transactions) {
		t.Errorf("%d transactions survived the upgrade, want %d",
			len(after.transactions), len(before.transactions))
	}
	if len(after.movements) != len(before.movements) {
		t.Errorf("%d movements survived the upgrade, want %d",
			len(after.movements), len(before.movements))
	}
	for id, was := range before.transactions {
		is, ok := after.transactions[id]
		if !ok {
			t.Errorf("transaction %d disappeared in the upgrade", id)
			continue
		}
		if is != was {
			t.Errorf("transaction %d is %+v after the upgrade, was %+v", id, is, was)
		}
	}
	for id, was := range before.movements {
		is, ok := after.movements[id]
		if !ok {
			t.Errorf("movement %d disappeared in the upgrade", id)
			continue
		}
		if is != was {
			t.Errorf("movement %d is %+v after the upgrade, was %+v", id, is, was)
		}
	}
	for account, was := range before.balances {
		if is := after.balances[account]; is != was {
			t.Errorf("the balance of account %d is %d after the upgrade, was %d", account, is, was)
		}
	}

	// The reference rates are a different table entirely and the rebuild must
	// not have gone anywhere near them.
	if afterRates := snapshotRates(t, db); afterRates != beforeRates {
		t.Errorf("exchange_rates is %+v after the upgrade, was %+v", afterRates, beforeRates)
	}

	// --- the new type is admitted, and only it -----------------------------

	if _, err := db.ExecContext(ctx, insertTransactionSQL,
		"FX_EXCHANGE", nil, nil, "Compra de USD", "2026-09-16"); err != nil {
		t.Errorf("an FX_EXCHANGE was refused after the upgrade: %v", err)
	}
	for _, invented := range []string{"BUY_USD", "SELL_USD", "EXCHANGE", "fx_exchange"} {
		if _, err := db.ExecContext(ctx, insertTransactionSQL,
			invented, nil, nil, nil, "2026-09-16"); err == nil {
			t.Errorf("%q was accepted as a transaction type", invented)
		}
	}

	// The two movements of an exchange are in two different accounts, in two
	// different currencies, and their magnitudes are unrelated. Nothing in the
	// schema says so -- that is Go's job, per TD-044 -- but nothing in the
	// schema may refuse it either.
	var exchangeID int64
	if err := db.QueryRowContext(ctx,
		"SELECT id FROM transactions WHERE transaction_type = 'FX_EXCHANGE'").Scan(&exchangeID); err != nil {
		t.Fatalf("reading the new exchange: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_movements (transaction_id, account_id, amount_minor, created_at)
		 VALUES (?, 1, -8422067, '2026-09-16T11:00:00.000Z'),
		        (?, 3,    10000, '2026-09-16T11:00:00.000Z')`, exchangeID, exchangeID); err != nil {
		t.Errorf("an exchange's two movements were refused: %v", err)
	}

	// --- the structure the rebuild had to preserve -------------------------

	assertSchemaIsSound(t, db)
}

// TestUpgradeToM55IsIdempotent runs the whole set twice. goose records what it
// applied, so the second run must be a no-op rather than a second rebuild.
func TestUpgradeToM55IsIdempotent(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)
	before := snapshotLedger(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("second migration run: %v", err)
	}

	after := snapshotLedger(t, db)
	if len(after.transactions) != len(before.transactions) ||
		len(after.movements) != len(before.movements) {
		t.Errorf("the second run changed the ledger")
	}
	for account, was := range before.balances {
		if is := after.balances[account]; is != was {
			t.Errorf("the balance of account %d is %d after the second run, was %d", account, is, was)
		}
	}
	assertSchemaIsSound(t, db)
}

// TestM55MigrationIsForwardOnly holds 00013 to the same rule as 00008, 00009 and
// 00010: its Down must refuse to run rather than invent a lossy mapping back.
//
// There is no honest one. An exchange cannot become a transfer, because its two
// accounts are in different currencies and a transfer's cannot be; it cannot
// become an income plus an expense without inventing a category for each; and
// discarding it would destroy the record of money that actually changed hands.
func TestM55MigrationIsForwardOnly(t *testing.T) {
	t.Parallel()

	const name = "00013_transactions_allow_fx_exchange.sql"

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

// insertTransactionSQL is this package's own copy of the statement the external
// schema test uses. The two test packages cannot see each other's constants, and
// duplicating six words is cheaper than exporting a test helper.
const insertTransactionSQL = `INSERT INTO transactions
                              (transaction_type, category_id, payment_method_id, description, effective_date)
                              VALUES (?, ?, ?, ?, ?)`

// seedM5Reference adds the M5 rows the rebuild must not disturb: a reference
// exchange rate and the valuation base.
//
// They are seeded precisely because recording an exchange has nothing to do with
// them. If a later change ever made the ledger write to exchange_rates, this
// test is where it would show.
func seedM5Reference(t *testing.T, db *sql.DB) {
	t.Helper()

	statements := []string{
		`INSERT INTO exchange_rates
		     (base_currency_code, quote_currency_code, rate, effective_date, source, fetched_at, is_active, created_at, updated_at)
		 VALUES ('USD','VES','842.20670000','2026-09-15','BCV_OFFICIAL',
		         '2026-09-15T12:00:00.000Z',1,'2026-09-15T12:00:00.000Z','2026-09-15T12:00:00.000Z')`,

		// Migration 00012 creates the singleton row itself, so this sets the
		// valuation base rather than inserting a second one -- which the CHECK
		// would refuse anyway.
		`UPDATE app_settings SET base_currency_code = 'USD' WHERE id = 1`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seeding the M5 reference data: %v", err)
		}
	}
}

// ratesSnapshot is what exchange_rates held, reduced to the facts a rebuild of
// another table could plausibly disturb.
type ratesSnapshot struct {
	count int
	first string
}

func snapshotRates(t *testing.T, db *sql.DB) ratesSnapshot {
	t.Helper()

	var snapshot ratesSnapshot
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*), COALESCE(min(base_currency_code || '/' || quote_currency_code || '@' || rate), '')
		   FROM exchange_rates`).Scan(&snapshot.count, &snapshot.first); err != nil {
		t.Fatalf("snapshotting exchange_rates: %v", err)
	}
	return snapshot
}
