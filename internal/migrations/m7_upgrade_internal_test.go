package migrations

import (
	"log/slog"
	"testing"
)

// M7.1 adds five tables and changes nothing that exists. The upgrade is still
// exercised against a schema 13 database carrying everything earlier milestones
// can hold, because "purely additive" is a claim, and this is what checks it: no
// transaction, movement, balance or rate moves; the new tables arrive empty --
// no suggestion is fabricated for history (TD-080); and both migrations roll
// back cleanly.
func TestUpgradeFromSchema13WithRealData(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	if err := up(ctx, db, sourceUpTo(t, 13), logger); err != nil {
		t.Fatalf("building a schema 13 database: %v", err)
	}
	seedM3Ledger(t, db)
	seedM4Transfer(t, db)
	seedM5Reference(t, db)

	before := snapshotLedger(t, db)
	beforeRates := snapshotRates(t, db)

	if err := Up(ctx, db, logger); err != nil {
		t.Fatalf("upgrading to M7.1: %v", err)
	}
	if got := appliedVersion(t, db); got != latestSchemaVersion {
		t.Errorf("the database is at version %d after upgrading, want %d", got, latestSchemaVersion)
	}

	assertLedgerUnchanged(t, before, snapshotLedger(t, db), "the upgrade")
	if afterRates := snapshotRates(t, db); afterRates != beforeRates {
		t.Errorf("exchange_rates is %+v after the upgrade, was %+v", afterRates, beforeRates)
	}

	// Nothing is generated for incomes that already exist. A policy has to be
	// set, and a backfill previewed and confirmed, before any history is
	// suggested from.
	for _, table := range []string{
		"savings_policy_versions", "savings_category_rules", "savings_income_overrides",
		"savings_suggestions", "savings_suggestion_rate_legs",
		"savings_operations", "savings_entries",
	} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after the upgrade, want 0", table, n)
		}
	}

	assertSchemaIsSound(t, db)

	// Reversible, newest first, straight from the embedded files.
	for _, file := range []string{
		"00016_create_savings_fund.sql",
		"00015_create_savings_suggestions.sql",
		"00014_create_savings_policy.sql",
	} {
		for _, statement := range downStatementsFromFS(t, file) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s Down failed on %q: %v", file, statement, err)
			}
		}
	}
	var remaining int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_schema WHERE name LIKE 'savings_%' OR name LIKE 'idx_savings_%'").Scan(&remaining); err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d savings objects survived the rollback", remaining)
	}
	assertLedgerUnchanged(t, before, snapshotLedger(t, db), "the rollback")
	assertSchemaIsSound(t, db)
}

func assertLedgerUnchanged(t *testing.T, before, after ledgerSnapshot, what string) {
	t.Helper()
	if len(after.transactions) != len(before.transactions) || len(after.movements) != len(before.movements) {
		t.Errorf("%s changed the number of transactions or movements", what)
	}
	for id, was := range before.transactions {
		if is := after.transactions[id]; is != was {
			t.Errorf("transaction %d is %+v after %s, was %+v", id, is, what, was)
		}
	}
	for id, was := range before.movements {
		if is := after.movements[id]; is != was {
			t.Errorf("movement %d is %+v after %s, was %+v", id, is, what, was)
		}
	}
	for account, was := range before.balances {
		if is := after.balances[account]; is != was {
			t.Errorf("the balance of account %d is %d after %s, was %d", account, is, what, was)
		}
	}
}
