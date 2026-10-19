package migrations

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// This file is in package migrations rather than migrations_test so it can
// reach `up` and substitute the migration source. It therefore cannot use
// internal/dbtest, which imports this package; it opens a database directly.

// latestSchemaVersion is the highest migration this build ships. The upgrade
// tests of earlier milestones assert it so that adding a migration is a
// deliberate, visible change to this one line rather than to four files.
const latestSchemaVersion = 18

func newRawDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "finance.db"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})
	return db
}

// A migration that fails must surface as an error. Startup depends on this:
// the process must refuse to serve rather than run against a half-built schema.
func TestUpReportsAFailingMigration(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)

	broken := fstest.MapFS{
		"00001_fine.sql": {Data: []byte(
			"-- +goose Up\nCREATE TABLE ok_so_far (id INTEGER PRIMARY KEY) STRICT;\n" +
				"-- +goose Down\nDROP TABLE ok_so_far;\n")},
		"00002_broken.sql": {Data: []byte(
			"-- +goose Up\nTHIS IS NOT VALID SQL;\n" +
				"-- +goose Down\nSELECT 1;\n")},
	}

	err := up(t.Context(), db, broken, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("expected an error from a broken migration")
	}
	// The message must identify which migration failed, otherwise diagnosing a
	// failed deployment means guessing.
	if !strings.Contains(err.Error(), "00002_broken.sql") {
		t.Errorf("error does not name the failing migration: %v", err)
	}

	// The migration before it stays applied, and the failed one rolled back:
	// SQLite has transactional DDL, so the schema is left at a consistent point.
	var count int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'ok_so_far'").Scan(&count); err != nil {
		t.Fatalf("inspecting schema: %v", err)
	}
	if count != 1 {
		t.Errorf("the migration preceding the failure is not applied (found %d tables)", count)
	}
}

// An empty source is a programming error, not a valid state, and goose reports
// it rather than silently doing nothing.
func TestUpRejectsAnEmptyMigrationSource(t *testing.T) {
	t.Parallel()

	db := newRawDB(t)

	if err := up(t.Context(), db, fstest.MapFS{}, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("expected an error when there are no migrations at all")
	}
}

// PendingOnExistingSchema decides whether startup takes a pre-migration
// snapshot, so its three answers are worth pinning down exactly.
func TestPendingOnExistingSchema(t *testing.T) {
	t.Parallel()

	t.Run("a brand-new database has nothing worth snapshotting", func(t *testing.T) {
		t.Parallel()

		db := newRawDB(t)

		pending, err := PendingOnExistingSchema(t.Context(), db, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("PendingOnExistingSchema: %v", err)
		}
		if pending {
			t.Error("a database with no applied migrations reported that it needs a snapshot")
		}
	})

	t.Run("a fully migrated database is not about to change", func(t *testing.T) {
		t.Parallel()

		db := newRawDB(t)
		if err := Up(t.Context(), db, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatalf("Up: %v", err)
		}

		pending, err := PendingOnExistingSchema(t.Context(), db, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("PendingOnExistingSchema: %v", err)
		}
		if pending {
			t.Error("an up-to-date database reported that it needs a snapshot")
		}
	})

	// The case the snapshot exists for: a schema that already holds data, about
	// to be altered. Built by applying a smaller migration source first, which
	// leaves the database at a version the real source is ahead of.
	t.Run("an older schema with migrations waiting", func(t *testing.T) {
		t.Parallel()

		db := newRawDB(t)

		partial := fstest.MapFS{
			"00001_create_currencies.sql": {Data: []byte(
				"-- +goose Up\nCREATE TABLE currencies (code TEXT NOT NULL PRIMARY KEY) STRICT;\n" +
					"-- +goose Down\nDROP TABLE currencies;\n")},
		}
		if err := up(t.Context(), db, partial, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatalf("applying the partial source: %v", err)
		}

		pending, err := PendingOnExistingSchema(t.Context(), db, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("PendingOnExistingSchema: %v", err)
		}
		if !pending {
			t.Error("an existing schema with migrations waiting reported that no snapshot is needed")
		}
	})
}
