package database_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// The point of OpenReadOnly is that inspecting an artifact cannot change it.
// That guarantee comes from SQLITE_OPEN_READONLY, so it is worth proving the
// driver actually honours mode=ro rather than quietly ignoring it.
func TestOpenReadOnlyRefusesWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "finance.db")
	ctx := t.Context()

	writable, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := writable.ExecContext(ctx, "CREATE TABLE t (v TEXT) STRICT"); err != nil {
		t.Fatalf("creating a table: %v", err)
	}
	if _, err := writable.ExecContext(ctx, "INSERT INTO t VALUES ('kept')"); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	if err := writable.Close(); err != nil {
		t.Fatalf("closing the writable handle: %v", err)
	}

	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	var v string
	if err := db.QueryRowContext(ctx, "SELECT v FROM t").Scan(&v); err != nil {
		t.Fatalf("reading through a read-only handle: %v", err)
	}
	if v != "kept" {
		t.Errorf("read %q, want %q", v, "kept")
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO t VALUES ('written')"); err == nil {
		t.Error("a read-only handle accepted an INSERT")
	}

	// integrity_check is what backup validation runs, so it must work here.
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		t.Fatalf("PRAGMA integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check = %q, want %q", result, "ok")
	}
}

// A missing backup must be a clear error, not an empty database conjured into
// existence -- which is what Open would do.
func TestOpenReadOnlyDoesNotCreateAnything(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "missing.db")

	if _, err := database.OpenReadOnly(t.Context(), path); err == nil {
		t.Fatal("OpenReadOnly succeeded on a file that does not exist")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("OpenReadOnly created %s", path)
	}
}

// A file that is not a database must fail on open rather than on first use.
func TestOpenReadOnlyRejectsSomethingElse(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	db, err := database.OpenReadOnly(t.Context(), path)
	if err == nil {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		t.Fatal("OpenReadOnly accepted a file that is not a database")
	}
}
