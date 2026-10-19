package migrations_test

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// expectedVersion is the highest migration version in this package. Bump it
// when a migration is added; the test then proves the new one actually ran.
const expectedVersion = 18

func TestUpAppliesEveryMigration(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t) // applies migrations

	var version int64
	err := db.QueryRowContext(t.Context(),
		"SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1").Scan(&version)
	if err != nil {
		t.Fatalf("reading goose_db_version: %v", err)
	}
	if version != expectedVersion {
		t.Errorf("applied version = %d, want %d", version, expectedVersion)
	}

	for _, table := range []string{
		"currencies", "accounts", "categories", "transactions", "account_movements",
		"payment_methods",
	} {
		var name string
		err := db.QueryRowContext(t.Context(),
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q was not created: %v", table, err)
		}
	}
}

// Migrations run on every start, so the second run must be a no-op rather than
// an error or a re-application.
func TestUpIsIdempotent(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	before := migrationRows(t, db)

	if err := migrations.Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if err := migrations.Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("third Up: %v", err)
	}

	if after := migrationRows(t, db); after != before {
		t.Errorf("goose_db_version has %d rows after re-running, want %d unchanged", after, before)
	}
}

// The DSN sets foreign_keys per connection; migrations must not disturb that.
func TestForeignKeysStillEnabledAfterMigrating(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	pragmas, err := database.ReadPragmas(t.Context(), db)
	if err != nil {
		t.Fatalf("ReadPragmas: %v", err)
	}
	if !pragmas.ForeignKeys {
		t.Error("foreign_keys is off after migrating")
	}
	if pragmas.JournalMode != "wal" {
		t.Errorf("journal_mode = %q after migrating, want %q", pragmas.JournalMode, "wal")
	}
}

// A database opened but never migrated must not already have the schema; this
// guards against a migration accidentally being applied by Open itself.
func TestOpenDoesNotMigrate(t *testing.T) {
	t.Parallel()

	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "finance.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	}()

	var count int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM sqlite_schema WHERE type = 'table'").Scan(&count); err != nil {
		t.Fatalf("counting tables: %v", err)
	}
	if count != 0 {
		t.Errorf("a freshly opened database has %d tables, want 0", count)
	}
}

func migrationRows(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM goose_db_version").Scan(&n); err != nil {
		t.Fatalf("counting goose_db_version rows: %v", err)
	}
	return n
}
