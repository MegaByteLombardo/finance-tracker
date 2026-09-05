// Package dbtest provides a migrated database for integration tests.
//
// Tests run against real SQLite rather than a mock. A mock would only prove
// that the mock behaves as written, whereas most of what is worth asserting
// here — CHECK constraints, foreign keys, STRICT typing, transactional
// rollback — is behaviour that only real SQLite exhibits.
package dbtest

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// New opens a fresh database in the test's temporary directory, applies every
// migration, and closes it when the test finishes.
//
// It goes through database.Open rather than sql.Open so that tests exercise the
// same PRAGMAs and pool settings the application runs with. A test that passed
// against a differently configured database would prove very little.
func New(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "finance.db"))
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing test database: %v", err)
		}
	})

	if err := migrations.Up(t.Context(), db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("applying migrations to test database: %v", err)
	}

	return db
}
