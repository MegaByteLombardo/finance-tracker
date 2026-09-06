package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fixture returns a migrated database, a backup service over it, and the
// directory the service writes to.
func fixture(t *testing.T) (*backup.Service, *sql.DB, string) {
	t.Helper()

	db := dbtest.New(t)
	dir := filepath.Join(t.TempDir(), "backups")

	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, db, dir
}

// seed writes a row that must appear in any snapshot taken afterwards.
func seed(t *testing.T, db *sql.DB, code, name string) {
	t.Helper()

	_, err := db.ExecContext(t.Context(),
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES (?, ?, NULL, 2)", code, name)
	if err != nil {
		t.Fatalf("seeding %s: %v", code, err)
	}
}

func TestCreateProducesAValidatedSnapshot(t *testing.T) {
	t.Parallel()

	svc, db, dir := fixture(t)
	seed(t, db, "AAA", "Test Currency")

	b, err := svc.Create(t.Context(), backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if b.Reason != backup.Manual {
		t.Errorf("Reason = %q, want %q", b.Reason, backup.Manual)
	}
	if b.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want a real file", b.SizeBytes)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}

	// The file exists under exactly the reported name, and no partial is left.
	if _, err := os.Stat(filepath.Join(dir, b.Name)); err != nil {
		t.Fatalf("the reported snapshot is not on disk: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the backup directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".partial") {
			t.Errorf("a partial file survived: %s", entry.Name())
		}
	}

	// The snapshot carries the committed data.
	snapshot, err := database.OpenReadOnly(t.Context(), filepath.Join(dir, b.Name))
	if err != nil {
		t.Fatalf("opening the snapshot: %v", err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("closing the snapshot: %v", err)
		}
	}()

	var name string
	if err := snapshot.QueryRowContext(t.Context(),
		"SELECT name FROM currencies WHERE code = 'AAA'").Scan(&name); err != nil {
		t.Fatalf("reading seeded data from the snapshot: %v", err)
	}
	if name != "Test Currency" {
		t.Errorf("snapshot has %q, want %q", name, "Test Currency")
	}
}

// The primary development host is Windows, where a colon cannot appear in a
// filename.
func TestBackupNamesAreWindowsSafe(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	for _, reason := range backup.Reasons() {
		b, err := svc.Create(t.Context(), reason)
		if err != nil {
			t.Fatalf("Create(%s): %v", reason, err)
		}
		if strings.ContainsAny(b.Name, `:\/`) {
			t.Errorf("name %q contains a character Windows forbids", b.Name)
		}
		if !strings.HasSuffix(b.Name, ".db") {
			t.Errorf("name %q does not end in .db", b.Name)
		}
		if !strings.Contains(b.Name, string(reason)) {
			t.Errorf("name %q does not record its reason", b.Name)
		}
	}
}

// Two snapshots in the same second must not collide, and the second must never
// overwrite the first.
func TestCreateNeverOverwrites(t *testing.T) {
	t.Parallel()

	svc, db, dir := fixture(t)
	seed(t, db, "AAA", "First")

	first, err := svc.Create(t.Context(), backup.Manual)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// A different database state, so an overwrite would be detectable.
	seed(t, db, "BBB", "Second")

	second, err := svc.Create(t.Context(), backup.Manual)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if first.Name == second.Name {
		t.Fatalf("both snapshots are called %q", first.Name)
	}

	// The first snapshot still holds its original contents.
	snapshot, err := database.OpenReadOnly(t.Context(), filepath.Join(dir, first.Name))
	if err != nil {
		t.Fatalf("opening the first snapshot: %v", err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	var count int
	if err := snapshot.QueryRowContext(t.Context(),
		"SELECT count(*) FROM currencies WHERE code = 'BBB'").Scan(&count); err != nil {
		t.Fatalf("querying the first snapshot: %v", err)
	}
	if count != 0 {
		t.Error("the first snapshot was overwritten by the second")
	}
}

// A snapshot must stand on its own: no -wal and no -shm beside it.
func TestSnapshotDoesNotDependOnWALFiles(t *testing.T) {
	t.Parallel()

	svc, db, dir := fixture(t)
	seed(t, db, "AAA", "Test Currency")

	b, err := svc.Create(t.Context(), backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path := filepath.Join(dir, b.Name)
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Errorf("%s exists beside the snapshot", filepath.Base(sidecar))
		}
	}

	if err := backup.Validate(t.Context(), path); err != nil {
		t.Errorf("a snapshot with no sidecars did not validate: %v", err)
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	want := make([]string, 0, 3)
	for range 3 {
		b, err := svc.Create(t.Context(), backup.Manual)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		want = append(want, b.Name)
	}

	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d backups, want 3", len(got))
	}

	// Created oldest to newest, so the listing is the reverse.
	for i, name := range got {
		if name.Name != want[len(want)-1-i] {
			t.Errorf("position %d is %q, want %q", i, name.Name, want[len(want)-1-i])
		}
	}
}

// The directory belongs to the operator; a stray file is not this listing's
// problem, and a partial is not a backup.
func TestListIgnoresEverythingThatIsNotABackup(t *testing.T) {
	t.Parallel()

	svc, _, dir := fixture(t)

	created, err := svc.Create(t.Context(), backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, junk := range []string{
		"notes.txt",
		"finance-20260905T183045Z-manual.db.partial",
		"finance-nonsense-manual.db",
		"backup.db",
		"finance-20260905T183045Z-unknownreason.db",
	} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("x"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", junk, err)
		}
	}

	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != created.Name {
		t.Errorf("List returned %+v, want only %q", got, created.Name)
	}
}

func TestValidateRejectsWhatIsNotABackup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Not SQLite at all.
	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte("definitely not a database"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	if err := backup.Validate(t.Context(), garbage); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Errorf("Validate on a non-database = %v, want ErrInvalidBackup", err)
	}

	// Valid SQLite, but not ours: no migration history.
	foreign := filepath.Join(dir, "foreign.db")
	other, err := database.Open(t.Context(), foreign)
	if err != nil {
		t.Fatalf("creating the foreign database: %v", err)
	}
	if _, err := other.ExecContext(t.Context(), "CREATE TABLE whatever (x TEXT) STRICT"); err != nil {
		t.Fatalf("populating the foreign database: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("closing the foreign database: %v", err)
	}
	if err := backup.Validate(t.Context(), foreign); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Errorf("Validate on a foreign database = %v, want ErrInvalidBackup", err)
	}

	// Missing entirely.
	if err := backup.Validate(t.Context(), filepath.Join(dir, "absent.db")); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Errorf("Validate on a missing file = %v, want ErrInvalidBackup", err)
	}
}

func TestGetRejectsAnythingCarryingAPath(t *testing.T) {
	t.Parallel()

	svc, _, _ := fixture(t)

	// Every one of these must be refused before it is joined to a directory.
	for _, name := range []string{
		"../backup.db",
		"../../etc/passwd",
		"foo/backup.db",
		`foo\backup.db`,
		"/foo/backup.db",
		`C:\foo\backup.db`,
		`\\server\share\backup.db`,
		"",
		".",
		"..",
		"finance-20260905T183045Z-manual.db/../../x",
	} {
		if _, err := svc.Get(t.Context(), name); !errors.Is(err, backup.ErrInvalidName) {
			t.Errorf("Get(%q) = %v, want ErrInvalidName", name, err)
		}
	}

	// A well-formed name that simply is not there is a different failure.
	if _, err := svc.Get(t.Context(), "finance-20260905T183045Z-manual.db"); !errors.Is(err, backup.ErrNotFound) {
		t.Errorf("Get of an absent backup = %v, want ErrNotFound", err)
	}
}

func TestRetentionPrunesOnlyShutdownBackups(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	dir := filepath.Join(t.TempDir(), "backups")

	svc, err := backup.NewService(db, dir, 2, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// One of each kind that must never be pruned.
	for _, reason := range []backup.Reason{backup.Manual, backup.PreMigration, backup.PreRestore} {
		if _, err := svc.Create(t.Context(), reason); err != nil {
			t.Fatalf("Create(%s): %v", reason, err)
		}
	}

	// Five shutdown snapshots, with a retention of two.
	shutdowns := make([]string, 0, 5)
	for range 5 {
		b, err := svc.Create(t.Context(), backup.Shutdown)
		if err != nil {
			t.Fatalf("Create(shutdown): %v", err)
		}
		shutdowns = append(shutdowns, b.Name)
	}

	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	counts := map[backup.Reason]int{}
	kept := map[string]bool{}
	for _, b := range got {
		counts[b.Reason]++
		kept[b.Name] = true
	}

	if counts[backup.Shutdown] != 2 {
		t.Errorf("%d shutdown backups survived, want 2", counts[backup.Shutdown])
	}
	for _, reason := range []backup.Reason{backup.Manual, backup.PreMigration, backup.PreRestore} {
		if counts[reason] != 1 {
			t.Errorf("%d %s backups survived, want 1 -- these are never pruned", counts[reason], reason)
		}
	}

	// The two kept are the newest two, and the newest is always among them.
	for _, name := range shutdowns[len(shutdowns)-2:] {
		if !kept[name] {
			t.Errorf("%q was pruned but is one of the newest two", name)
		}
	}
}

func TestRetentionZeroKeepsEverything(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svc, err := backup.NewService(db, filepath.Join(t.TempDir(), "backups"), 0, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	for range 5 {
		if _, err := svc.Create(t.Context(), backup.Shutdown); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("%d backups survived with retention disabled, want 5", len(got))
	}
}

func TestNewServiceCreatesTheDirectory(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	dir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")

	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := os.Stat(svc.Dir()); err != nil {
		t.Fatalf("the backup directory was not created: %v", err)
	}
	if _, err := svc.Create(t.Context(), backup.Manual); err != nil {
		t.Errorf("Create into a freshly made directory: %v", err)
	}
}

func TestNewServiceRejectsBadConfiguration(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	if _, err := backup.NewService(db, "", 10, discard()); err == nil {
		t.Error("NewService accepted a blank directory")
	}
	if _, err := backup.NewService(db, t.TempDir(), -1, discard()); err == nil {
		t.Error("NewService accepted a negative retention")
	}
}

// A cancelled context must stop the snapshot rather than let it run.
func TestCreateRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	svc, _, dir := fixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := svc.Create(ctx, backup.Manual); err == nil {
		t.Error("Create succeeded with a cancelled context")
	}

	// And it left nothing behind that could be mistaken for a backup.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the backup directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled Create left %d file(s) behind", len(entries))
	}
}

// Pruning deletes files, which frees their names. A snapshot taken afterwards
// in the same second must not reuse one: the file would be new while its name
// sorted as old, which corrupts the listing order and, through it, which
// snapshot pruning believes is the oldest.
//
// This is a regression test. The first implementation chose names by checking
// the directory alone and did exactly this.
func TestPruningDoesNotFreeNamesForReuse(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	dir := filepath.Join(t.TempDir(), "backups")

	// Retention of one, so every Create after the first prunes.
	svc, err := backup.NewService(db, dir, 1, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	seen := map[string]bool{}
	var previous backup.Backup

	for i := range 6 {
		b, err := svc.Create(t.Context(), backup.Shutdown)
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		if seen[b.Name] {
			t.Fatalf("Create %d reused the name %q", i, b.Name)
		}
		seen[b.Name] = true

		// Each snapshot must list as newer than the one before it.
		if i > 0 {
			svc2 := svc
			listed, err := svc2.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(listed) == 0 {
				t.Fatal("List returned nothing after a create")
			}
			if listed[0].Name != b.Name {
				t.Errorf("after creating %q the newest listed is %q (previous was %q)",
					b.Name, listed[0].Name, previous.Name)
			}
		}
		previous = b
	}

	// Exactly one survives, and it is the last one taken.
	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d backups survived with retention 1, want 1", len(got))
	}
	if got[0].Name != previous.Name {
		t.Errorf("the survivor is %q, want the most recent %q", got[0].Name, previous.Name)
	}
}
