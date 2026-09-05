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
