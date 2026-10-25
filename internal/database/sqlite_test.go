package database_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// These tests run against a real SQLite database in a temporary directory.
// Mocking the driver would only prove that the mock behaves as written; the
// whole point here is to find out what SQLite actually does with our DSN.

// openTemp opens a database under t.TempDir() and closes it when the test ends.
func openTemp(t *testing.T, elems ...string) (*sql.DB, string) {
	t.Helper()

	if len(elems) == 0 {
		elems = []string{"finance.db"}
	}
	path := filepath.Join(append([]string{t.TempDir()}, elems...)...)

	db, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})

	return db, path
}

func TestOpenCreatesDatabaseAndParentDirectories(t *testing.T) {
	t.Parallel()

	// A nested path that does not exist yet: DB_PATH defaults to ./data/finance.db,
	// so Open has to be able to create the directory it lives in.
	db, path := openTemp(t, "nested", "data", "finance.db")

	if err := db.PingContext(t.Context()); err != nil {
		t.Errorf("PingContext: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file was not created at %q: %v", path, err)
	}
}

func TestEffectivePragmas(t *testing.T) {
	t.Parallel()

	db, _ := openTemp(t)

	got, err := database.ReadPragmas(t.Context(), db)
	if err != nil {
		t.Fatalf("ReadPragmas: %v", err)
	}

	want := database.Pragmas{
		JournalMode:   "wal",
		ForeignKeys:   true,
		BusyTimeoutMS: database.BusyTimeoutMS,
		Synchronous:   2, // FULL
	}
	if got != want {
		t.Errorf("effective pragmas = %+v, want %+v", got, want)
	}
	if got.SynchronousName() != database.Synchronous {
		t.Errorf("SynchronousName() = %q, want %q", got.SynchronousName(), database.Synchronous)
	}
}

// Reading "PRAGMA foreign_keys" only proves SQLite reports the flag as on.
// This proves the constraint is actually enforced.
func TestForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()

	db, _ := openTemp(t)
	ctx := t.Context()

	// A throwaway fixture, not application schema. M0 owns no domain tables.
	const schema = `
		CREATE TABLE parent (id INTEGER PRIMARY KEY) STRICT;
		CREATE TABLE child (
			id        INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL REFERENCES parent(id)
		) STRICT;`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		t.Fatalf("creating fixture schema: %v", err)
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO child (id, parent_id) VALUES (1, 999)"); err == nil {
		t.Fatal("inserting a child row with no matching parent succeeded; foreign keys are not being enforced")
	} else if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("insert failed, but not with a foreign key error: %v", err)
	}
}

// The reason the PRAGMAs live in the DSN rather than in a one-off Exec after
// sql.Open: database/sql is a pool, and every physical connection it opens has
// to end up configured the same way. This test holds several connections open
// at once so that each one is guaranteed to be distinct, then checks all of them.
func TestPragmasApplyToEveryPooledConnection(t *testing.T) {
	t.Parallel()

	db, _ := openTemp(t)
	ctx := t.Context()

	const connections = 4
	// Open() pins the pool to a single connection on purpose; widen it here so
	// the pool is forced to create additional physical connections.
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)

	held := make([]*sql.Conn, 0, connections)
	for i := range connections {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("acquiring connection %d: %v", i, err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Errorf("releasing connection: %v", err)
			}
		})
		held = append(held, conn)
	}

	want := database.Pragmas{
		JournalMode:   "wal",
		ForeignKeys:   true,
		BusyTimeoutMS: database.BusyTimeoutMS,
		Synchronous:   2, // FULL
	}
	for i, conn := range held {
		got, err := database.ReadPragmas(ctx, conn)
		if err != nil {
			t.Fatalf("ReadPragmas on connection %d: %v", i, err)
		}
		if got != want {
			t.Errorf("connection %d: pragmas = %+v, want %+v", i, got, want)
		}
	}
}

// Writes must survive the process, which is the only persistence property worth
// asserting. The -wal and -shm sidecars are SQLite's own business: their
// lifecycle depends on checkpointing and clean shutdown, so testing for their
// presence would only buy flakes.
func TestDataSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "finance.db")
	ctx := t.Context()

	first, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := first.ExecContext(ctx, "CREATE TABLE note (body TEXT NOT NULL) STRICT"); err != nil {
		t.Fatalf("creating fixture table: %v", err)
	}
	if _, err := first.ExecContext(ctx, "INSERT INTO note (body) VALUES ('written before reopen')"); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing first handle: %v", err)
	}

	second, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Errorf("closing second handle: %v", err)
		}
	}()

	var body string
	if err := second.QueryRowContext(ctx, "SELECT body FROM note").Scan(&body); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if body != "written before reopen" {
		t.Errorf("body = %q, want %q", body, "written before reopen")
	}

	// WAL is persistent, so the reopened database must still be in WAL mode.
	got, err := database.ReadPragmas(ctx, second)
	if err != nil {
		t.Fatalf("ReadPragmas after reopen: %v", err)
	}
	if got.JournalMode != "wal" {
		t.Errorf("journal_mode after reopen = %q, want %q", got.JournalMode, "wal")
	}
}

// An unusable path has to fail at startup, not on the first request.
func TestOpenRejectsUnusablePath(t *testing.T) {
	t.Parallel()

	// A directory is not a database file.
	dir := t.TempDir()

	db, err := database.Open(t.Context(), dir)
	if err == nil {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("closing unexpected handle: %v", cerr)
		}
		t.Fatalf("Open(%q) on a directory unexpectedly succeeded", dir)
	}
	if db != nil {
		t.Error("Open returned both a database handle and an error")
	}
}

// CheckForeignKeys is the post-condition migrations.Up asserts after 00019,
// which switches enforcement off on the one pooled connection (TD-088).
func TestCheckForeignKeys(t *testing.T) {
	t.Parallel()

	db, _ := openTemp(t, "finance.db")
	ctx := t.Context()

	if err := database.CheckForeignKeys(ctx, db); err != nil {
		t.Fatalf("on a fresh connection: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("switching foreign keys off: %v", err)
	}
	if err := database.CheckForeignKeys(ctx, db); err == nil {
		t.Error("CheckForeignKeys passed with foreign keys off")
	}
}
