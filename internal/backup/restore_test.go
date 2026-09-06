package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// ledger builds a database at a real path with accounts, categories,
// transactions and movements in it, and returns the path and the balance that
// must survive a round trip.
func ledger(t *testing.T) (dbPath string, wantBalance int64) {
	t.Helper()

	dbPath = filepath.Join(t.TempDir(), "data", "finance.db")
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := migrations.Up(ctx, db, discard()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	statements := []string{
		`INSERT INTO accounts (id,name,account_type,account_subtype,currency_code,institution,is_active,created_at,updated_at)
		 VALUES (1,'Mercantil USD','BANK','CHECKING','USD','Mercantil',1,'2026-09-01T10:00:00.000Z','2026-09-01T10:00:00.000Z')`,
		`INSERT INTO categories (id,name,category_type,is_active,created_at,updated_at)
		 VALUES (1,'Salario','INCOME',1,'2026-09-01T10:00:01.000Z','2026-09-01T10:00:01.000Z'),
		        (2,'Comida','EXPENSE',1,'2026-09-01T10:00:02.000Z','2026-09-01T10:00:02.000Z')`,
		`INSERT INTO transactions (id,transaction_type,category_id,payment_method_id,description,effective_date,created_at,updated_at)
		 VALUES (10,'OPENING_BALANCE',NULL,NULL,NULL,'2026-09-01','2026-09-01T11:00:00.000Z','2026-09-01T11:00:00.000Z'),
		        (11,'INCOME',1,1,'Sueldo','2026-09-03','2026-09-03T11:00:00.000Z','2026-09-03T11:00:00.000Z'),
		        (12,'EXPENSE',2,2,NULL,'2026-09-04','2026-09-04T11:00:00.000Z','2026-09-04T11:00:00.000Z')`,
		`INSERT INTO account_movements (id,transaction_id,account_id,amount_minor,created_at)
		 VALUES (100,10,1,100000,'2026-09-01T11:00:00.000Z'),
		        (101,11,1,150050,'2026-09-03T11:00:00.000Z'),
		        (102,12,1,-2599,'2026-09-04T11:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(amount_minor), 0) FROM account_movements WHERE account_id = 1").
		Scan(&wantBalance); err != nil {
		t.Fatalf("reading the balance: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	return dbPath, wantBalance
}

// counts reads what a restore must preserve.
func counts(t *testing.T, dbPath string) (accounts, categories, transactions, movements int, balance int64) {
	t.Helper()

	db, err := database.OpenReadOnly(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("opening %s: %v", dbPath, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	for _, q := range []struct {
		query string
		into  *int
	}{
		{"SELECT count(*) FROM accounts", &accounts},
		{"SELECT count(*) FROM categories", &categories},
		{"SELECT count(*) FROM transactions", &transactions},
		{"SELECT count(*) FROM account_movements", &movements},
	} {
		if err := db.QueryRowContext(t.Context(), q.query).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	if err := db.QueryRowContext(t.Context(),
		"SELECT COALESCE(SUM(amount_minor), 0) FROM account_movements WHERE account_id = 1").
		Scan(&balance); err != nil {
		t.Fatalf("summing movements: %v", err)
	}
	return accounts, categories, transactions, movements, balance
}

func TestRestoreBringsEverythingBack(t *testing.T) {
	t.Parallel()

	dbPath, wantBalance := ledger(t)
	dir := filepath.Join(filepath.Dir(dbPath), "..", "backups")
	ctx := t.Context()

	// Take the snapshot.
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := svc.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Destroy the data, exactly as a mistake would.
	if _, err := db.ExecContext(ctx, "DELETE FROM account_movements"); err != nil {
		t.Fatalf("deleting movements: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM transactions"); err != nil {
		t.Fatalf("deleting transactions: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if _, _, transactions, _, _ := counts(t, dbPath); transactions != 0 {
		t.Fatalf("the setup did not actually destroy the data: %d transactions remain", transactions)
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir: svc.Dir(), Name: snapshot.Name, DBPath: dbPath, Logger: discard(),
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	accounts, categories, transactions, movements, balance := counts(t, dbPath)
	if accounts != 1 || categories != 2 || transactions != 3 || movements != 3 {
		t.Errorf("restored %d accounts, %d categories, %d transactions, %d movements; want 1, 2, 3, 3",
			accounts, categories, transactions, movements)
	}
	if balance != wantBalance {
		t.Errorf("balance = %d, want %d", balance, wantBalance)
	}

	// Nothing is left lying around.
	entries, err := os.ReadDir(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("reading the data directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".restoring") || strings.Contains(name, ".previous-") {
			t.Errorf("%s survived a successful restore", name)
		}
	}

	// A pre-restore snapshot exists, so the restore itself can be undone.
	listed, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, b := range listed {
		if b.Reason == backup.PreRestore {
			found = true
		}
	}
	if !found {
		t.Error("no pre-restore snapshot was taken; the restore is not reversible")
	}
}

// A stale -wal beside a freshly restored database is the one combination that
// can corrupt the result.
func TestRestoreClearsStaleJournalFiles(t *testing.T) {
	t.Parallel()

	dbPath, _ := ledger(t)
	dir := filepath.Join(filepath.Dir(dbPath), "..", "backups")
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := svc.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// Plant journal files as an unclean shutdown would leave them.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatalf("planting %s: %v", suffix, err)
		}
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir: svc.Dir(), Name: snapshot.Name, DBPath: dbPath, Logger: discard(),
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Errorf("a stale %s survived the restore", suffix)
		}
	}

	// And the result is readable, which it would not be if a stale WAL had been
	// applied over it.
	if _, _, transactions, _, _ := counts(t, dbPath); transactions != 3 {
		t.Errorf("restored database has %d transactions, want 3", transactions)
	}
}

func TestRestoreRefusesAnythingCarryingAPath(t *testing.T) {
	t.Parallel()

	dbPath, _ := ledger(t)
	dir := t.TempDir()

	for _, name := range []string{
		"../backup.db",
		"../../etc/passwd",
		"foo/backup.db",
		`foo\backup.db`,
		"/foo/backup.db",
		`C:\foo\backup.db`,
		`\\server\share\backup.db`,
		"",
		"..",
	} {
		err := backup.Restore(t.Context(), backup.RestoreOptions{
			Dir: dir, Name: name, DBPath: dbPath, Logger: discard(),
		})
		if !errors.Is(err, backup.ErrInvalidName) {
			t.Errorf("Restore(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

// A restore that cannot go ahead must leave the running system exactly as it
// was -- that is what makes it safe to attempt.
func TestRestoreLeavesTheDatabaseAloneWhenItCannotProceed(t *testing.T) {
	t.Parallel()

	dbPath, wantBalance := ledger(t)
	dir := t.TempDir()
	ctx := t.Context()

	corrupt := filepath.Join(dir, "finance-20260905T183045Z-manual.db")
	if err := os.WriteFile(corrupt, []byte("not a database at all"), 0o600); err != nil {
		t.Fatalf("writing the corrupt backup: %v", err)
	}

	cases := map[string]string{
		"corrupt backup": "finance-20260905T183045Z-manual.db",
		"absent backup":  "finance-20260101T000000Z-manual.db",
	}

	for label, name := range cases {
		err := backup.Restore(ctx, backup.RestoreOptions{
			Dir: dir, Name: name, DBPath: dbPath, Logger: discard(),
		})
		if err == nil {
			t.Fatalf("Restore with a %s succeeded", label)
		}

		accounts, categories, transactions, movements, balance := counts(t, dbPath)
		if accounts != 1 || categories != 2 || transactions != 3 || movements != 3 {
			t.Errorf("after a failed restore (%s): %d/%d/%d/%d, want 1/2/3/3",
				label, accounts, categories, transactions, movements)
		}
		if balance != wantBalance {
			t.Errorf("after a failed restore (%s): balance %d, want %d", label, balance, wantBalance)
		}

		if _, serr := os.Stat(dbPath + ".restoring"); serr == nil {
			t.Errorf("a staged file survived a failed restore (%s)", label)
		}
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir: dir, Name: cases["corrupt backup"], DBPath: dbPath, Logger: discard(),
	}); !errors.Is(err, backup.ErrInvalidBackup) {
		t.Errorf("a corrupt backup = %v, want ErrInvalidBackup", err)
	}
}

// Restoring onto a machine where the database does not exist yet -- the shape of
// a real recovery, where the volume was destroyed -- must work.
func TestRestoreOntoNothing(t *testing.T) {
	t.Parallel()

	dbPath, wantBalance := ledger(t)
	dir := filepath.Join(filepath.Dir(dbPath), "..", "backups")
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := svc.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// The volume is gone.
	fresh := filepath.Join(t.TempDir(), "newvolume", "finance.db")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o755); err != nil {
		t.Fatalf("creating the new volume: %v", err)
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir: svc.Dir(), Name: snapshot.Name, DBPath: fresh, Logger: discard(),
	}); err != nil {
		t.Fatalf("Restore onto an empty volume: %v", err)
	}

	accounts, _, transactions, movements, balance := counts(t, fresh)
	if accounts != 1 || transactions != 3 || movements != 3 {
		t.Errorf("restored %d accounts, %d transactions, %d movements; want 1, 3, 3",
			accounts, transactions, movements)
	}
	if balance != wantBalance {
		t.Errorf("balance = %d, want %d", balance, wantBalance)
	}
}

// The restored database must be usable by the ordinary startup path, which is
// what lets an older snapshot be brought forward by migrations rather than by
// the restore itself.
func TestRestoredDatabaseAcceptsMigrationsAfterwards(t *testing.T) {
	t.Parallel()

	dbPath, _ := ledger(t)
	dir := filepath.Join(filepath.Dir(dbPath), "..", "backups")
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := svc.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir: svc.Dir(), Name: snapshot.Name, DBPath: dbPath, Logger: discard(),
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Exactly what the next normal start does.
	reopened, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening after restore: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	if err := migrations.Up(ctx, reopened, discard()); err != nil {
		t.Errorf("migrating a restored database: %v", err)
	}

	var version int64
	if err := reopened.QueryRowContext(ctx,
		"SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1").Scan(&version); err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if version != latest {
		t.Errorf("restored database is at version %d, want %d", version, latest)
	}
}

// A restore must not silently do nothing when the staging copy cannot be
// written.
func TestRestoreFailsLoudlyWhenItCannotStage(t *testing.T) {
	t.Parallel()

	dbPath, _ := ledger(t)
	dir := filepath.Join(filepath.Dir(dbPath), "..", "backups")
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	svc, err := backup.NewService(db, dir, 10, discard())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := svc.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// A destination whose directory does not exist.
	missing := filepath.Join(t.TempDir(), "no", "such", "dir", "finance.db")
	err = backup.Restore(ctx, backup.RestoreOptions{
		Dir: svc.Dir(), Name: snapshot.Name, DBPath: missing, Logger: discard(),
	})
	if err == nil {
		t.Fatal("Restore into a missing directory succeeded")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("error = %v, want it to name the staging step", err)
	}
}
