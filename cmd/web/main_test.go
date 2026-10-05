package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// testEnv wires a run() into a temporary database and an ephemeral port.
func testEnv(t *testing.T, overrides map[string]string) func(string) string {
	t.Helper()

	// Both paths point into the test's own directory. BACKUP_DIR especially:
	// its default is ./backups, and a test that used it would create that
	// directory inside the package being tested.
	dir := t.TempDir()
	env := map[string]string{
		"APP_ENV":    "development",
		"HTTP_ADDR":  "127.0.0.1:0",
		"DB_PATH":    filepath.Join(dir, "data", "finance.db"),
		"BACKUP_DIR": filepath.Join(dir, "backups"),
		"LOG_LEVEL":  "error",
	}
	for key, value := range overrides {
		env[key] = value
	}

	return func(key string) string { return env[key] }
}

// Cancelling the context must bring the whole process down cleanly: the server
// stops, the database is closed, and run reports no error.
//
// Startup is awaited by watching the log stream for the line that says the
// server is listening, not by sleeping. A fixed sleep would be a guess about
// how long migrations take, and under load it loses: cancelling mid-migration
// makes startup fail correctly, but that is a different scenario from the one
// this test is about.
func TestRunShutsDownCleanly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	logs, logWriter := io.Pipe()
	listening := make(chan struct{})

	go func() {
		scanner := bufio.NewScanner(logs)
		signalled := false
		for scanner.Scan() {
			if !signalled && strings.Contains(scanner.Text(), "http server listening") {
				close(listening)
				signalled = true
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, testEnv(t, map[string]string{"LOG_LEVEL": "info"}), logWriter) }()

	select {
	case <-listening:
	case err := <-done:
		t.Fatalf("run returned before it started listening: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the server never reported that it was listening")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned an error on clean shutdown: %v", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}

	// Ends the scanner goroutine.
	if err := logWriter.Close(); err != nil {
		t.Errorf("closing the log pipe: %v", err)
	}
}

// Configuration problems must be reported as errors, never as a panic, and must
// stop the process before it opens a database or a socket.
func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	err := run(t.Context(), nil, testEnv(t, map[string]string{"APP_ENV": "staging"}), io.Discard)
	if err == nil {
		t.Fatal("expected an error for an invalid APP_ENV")
	}
	if !strings.Contains(err.Error(), "APP_ENV") {
		t.Errorf("error does not name the offending variable: %v", err)
	}
}

// An unavailable port must fail at startup rather than leaving a process that
// logs "listening" and serves nothing.
func TestRunFailsWhenAddressIsUnavailable(t *testing.T) {
	t.Parallel()

	// Hold a port open, then point run at it.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	t.Cleanup(func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("releasing the reserved port: %v", err)
		}
	})

	runErr := run(t.Context(), nil, testEnv(t, map[string]string{
		"HTTP_ADDR": occupied.Addr().String(),
	}), io.Discard)

	if runErr == nil {
		t.Fatal("expected an error when the address is already in use")
	}
	if !strings.Contains(runErr.Error(), "listening on") {
		t.Errorf("error does not identify the listen failure: %v", runErr)
	}
}

// Migrations must run before anything listens: serving against a half-migrated
// schema would be worse than not serving at all.
//
// The ordering is proved rather than assumed. The port is occupied *and* the
// database is primed to make migration 00001 fail. Whichever step runs first
// determines the error, so an error about migrations means migrations ran
// before the listener was created.
func TestRunMigratesBeforeListening(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "finance.db")

	// A conflicting table makes "CREATE TABLE currencies" fail.
	seed, err := database.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("preparing the database: %v", err)
	}
	if _, err := seed.ExecContext(t.Context(), "CREATE TABLE currencies (wrong TEXT) STRICT"); err != nil {
		t.Fatalf("creating the conflicting table: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing the seed handle: %v", err)
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	t.Cleanup(func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("releasing the reserved port: %v", err)
		}
	})

	runErr := run(t.Context(), nil, testEnv(t, map[string]string{
		"DB_PATH":   dbPath,
		"HTTP_ADDR": occupied.Addr().String(),
	}), io.Discard)

	if runErr == nil {
		t.Fatal("expected an error from a failing migration")
	}
	if !strings.Contains(runErr.Error(), "migrating database") {
		t.Errorf("run failed for the wrong reason; migrations should have run first: %v", runErr)
	}
	if strings.Contains(runErr.Error(), "listening on") {
		t.Error("the listener was created before migrations ran")
	}
}

// envWithPaths is testEnv when the test needs to look at the files afterwards.
func envWithPaths(t *testing.T, overrides map[string]string) (getenv func(string) string, dbPath, backupDir string) {
	t.Helper()

	dir := t.TempDir()
	dbPath = filepath.Join(dir, "data", "finance.db")
	backupDir = filepath.Join(dir, "backups")

	env := map[string]string{
		"APP_ENV":    "development",
		"HTTP_ADDR":  "127.0.0.1:0",
		"DB_PATH":    dbPath,
		"BACKUP_DIR": backupDir,
		"LOG_LEVEL":  "error",
	}
	for key, value := range overrides {
		env[key] = value
	}
	return func(key string) string { return env[key] }, dbPath, backupDir
}

// serveUntilListening starts the server and returns once it reports that it is
// listening: a channel carrying run's eventual result, and the address the
// server actually bound.
//
// The address is READ OUT OF THE LOG rather than guessed. Tests ask for port 0
// so they cannot collide, which means the real port is only known after the
// listener exists; reserving a port and closing it again would reintroduce
// exactly the race port 0 avoids.
func serveUntilListening(
	t *testing.T, ctx context.Context, getenv func(string) string,
) (<-chan error, string) {
	t.Helper()

	logs, logWriter := io.Pipe()
	listening := make(chan struct{})

	// Written before close(listening) and read only after <-listening, so the
	// channel provides the happens-before the race detector needs.
	var addr string

	go func() {
		scanner := bufio.NewScanner(logs)
		signalled := false
		for scanner.Scan() {
			if !signalled && strings.Contains(scanner.Text(), "http server listening") {
				addr = addrFromLogLine(scanner.Text())
				close(listening)
				signalled = true
			}
		}
	}()
	t.Cleanup(func() {
		if err := logWriter.Close(); err != nil {
			t.Errorf("closing the log pipe: %v", err)
		}
	})

	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, getenv, logWriter) }()

	select {
	case <-listening:
	case err := <-done:
		t.Fatalf("run returned before it started listening: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the server never reported that it was listening")
	}
	return done, addr
}

// addrFromLogLine pulls the listening address out of the startup log line.
//
// The line is slog's text format, where a value containing no spaces is written
// unquoted: `msg="http server listening" addr=127.0.0.1:54321`. Nothing else on
// that line carries an addr key.
func addrFromLogLine(line string) string {
	_, rest, ok := strings.Cut(line, "addr=")
	if !ok {
		return ""
	}
	addr, _, _ := strings.Cut(rest, " ")
	return strings.Trim(addr, `"`)
}

// backupsIn lists the snapshot filenames in a directory.
func backupsIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// A clean shutdown must leave a snapshot behind. This is the trigger that would
// have saved the data lost after M2: `docker compose down` sends SIGTERM and
// waits before it destroys anything.
func TestShutdownTakesABackup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	getenv, _, backupDir := envWithPaths(t, map[string]string{"LOG_LEVEL": "info"})

	done, _ := serveUntilListening(t, ctx, getenv)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned an error on clean shutdown: %v", err)
		}
	case <-time.After(shutdownTimeout + backupTimeout + 5*time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}

	names := backupsIn(t, backupDir)
	if len(names) != 1 {
		t.Fatalf("shutdown left %v in the backup directory, want exactly one snapshot", names)
	}
	if !strings.Contains(names[0], "-shutdown.db") {
		t.Errorf("the snapshot is called %q, want a shutdown backup", names[0])
	}
}

// A fresh database has nothing worth snapshotting, so startup must not produce
// a pre-migration backup of an empty file.
func TestStartupDoesNotBackUpAnEmptyDatabase(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	getenv, _, backupDir := envWithPaths(t, map[string]string{"LOG_LEVEL": "info"})

	done, _ := serveUntilListening(t, ctx, getenv)

	// Before shutdown, so only a pre-migration backup could exist.
	if names := backupsIn(t, backupDir); len(names) != 0 {
		t.Errorf("a fresh database produced %v, want nothing", names)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(shutdownTimeout + backupTimeout + 5*time.Second):
		t.Fatal("run did not return")
	}
}

// The restore subcommand is the whole point of backups existing.
func TestRestoreSubcommandRoundTrip(t *testing.T) {
	t.Parallel()

	getenv, dbPath, backupDir := envWithPaths(t, nil)
	ctx := t.Context()

	// A database with something in it, and a snapshot of it.
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := migrations.Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES ('AAA', 'Kept', NULL, 2)"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	backups, err := backup.NewService(db, backupDir, 10, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	snapshot, err := backups.Create(ctx, backup.Manual)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Lose it.
	if _, err := db.ExecContext(ctx, "DELETE FROM currencies WHERE code = 'AAA'"); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if err := run(ctx, []string{"restore", "--file", snapshot.Name}, getenv, io.Discard); err != nil {
		t.Fatalf("restore subcommand: %v", err)
	}

	restored, err := database.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening the restored database: %v", err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	var name string
	if err := restored.QueryRowContext(ctx,
		"SELECT name FROM currencies WHERE code = 'AAA'").Scan(&name); err != nil {
		t.Fatalf("the restored row is missing: %v", err)
	}
	if name != "Kept" {
		t.Errorf("restored %q, want %q", name, "Kept")
	}
}

func TestRestoreSubcommandRejectsBadInvocations(t *testing.T) {
	t.Parallel()

	getenv, _, _ := envWithPaths(t, nil)

	tests := map[string][]string{
		"no file given":    {"restore"},
		"empty file":       {"restore", "--file", ""},
		"path traversal":   {"restore", "--file", "../../etc/passwd"},
		"absent backup":    {"restore", "--file", "finance-20260101T000000Z-manual.db"},
		"stray argument":   {"restore", "--file", "finance-20260101T000000Z-manual.db", "extra"},
		"unknown flag":     {"restore", "--everything"},
		"unknown command":  {"vacuum"},
		"misspelled serve": {"serv"},
	}

	for label, args := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			if err := run(t.Context(), args, getenv, io.Discard); err == nil {
				t.Errorf("run(%v) succeeded, want an error", args)
			}
		})
	}
}

// --- healthcheck -------------------------------------------------------------

// The health check must agree with the server it probes, and it must find that
// server through configuration rather than through a hard-coded port.
//
// Both halves are real here: a real server on a real socket, and the subcommand
// pointed at it by HTTP_ADDR. That is also what keeps the "ok" this command
// insists on in step with the "ok" internal/httpserver writes -- the string is
// spelled in two packages, and this is what would catch them diverging.
func TestHealthcheckSubcommandAgainstARunningServer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	getenv, _, _ := envWithPaths(t, map[string]string{"LOG_LEVEL": "info"})

	done, addr := serveUntilListening(t, ctx, getenv)
	if addr == "" {
		t.Fatal("the startup log did not report a listening address")
	}

	// A separate environment, carrying only the address: the health check must
	// need nothing else.
	probe, _, _ := envWithPaths(t, map[string]string{"HTTP_ADDR": addr})

	var out strings.Builder
	if err := run(ctx, []string{"healthcheck"}, probe, &out); err != nil {
		t.Fatalf("healthcheck against a healthy server: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "ok" {
		t.Errorf("healthcheck printed %q, want %q", got, "ok")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned an error on clean shutdown: %v", err)
		}
	case <-time.After(shutdownTimeout + backupTimeout + 5*time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}

	// And the same check must fail once that server is gone. A health check that
	// cannot report "dead" is not a health check. The context is a fresh one:
	// the cancelled one would fail the request for the wrong reason.
	if err := run(t.Context(), []string{"healthcheck"}, probe, io.Discard); err == nil {
		t.Error("healthcheck succeeded against a server that is not running")
	}
}

// A check that read only the status code would pass against anything answering
// 200 on that port; one that read only the body would pass a 503 that still
// said "ok". Both halves are checked, so both are tested.
func TestHealthcheckSubcommandRejectsAnUnhealthyAnswer(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		status int
		body   string
	}{
		"the database is unreachable": {http.StatusServiceUnavailable, `{"status":"unavailable"}`},
		"a failing status with an ok body": {
			http.StatusInternalServerError, `{"status":"ok"}`,
		},
		"another service on the port": {http.StatusOK, "<html>not finance tracker</html>"},
		"an empty body":               {http.StatusOK, ""},
		"an unknown status":           {http.StatusOK, `{"status":"degraded"}`},
	}

	for label, answer := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(answer.status)
				if _, err := io.WriteString(w, answer.body); err != nil {
					t.Errorf("writing the test response body: %v", err)
				}
			}))
			t.Cleanup(srv.Close)

			getenv, _, _ := envWithPaths(t, map[string]string{
				"HTTP_ADDR": srv.Listener.Addr().String(),
			})

			if err := run(t.Context(), []string{"healthcheck"}, getenv, io.Discard); err == nil {
				t.Error("healthcheck reported a healthy server")
			}
		})
	}
}

// A wildcard listen address is not an address anything can dial, so the probe
// has to resolve it to the loopback -- which is where the server is listening in
// every one of those cases.
func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://127.0.0.1:8080/healthz",
		"127.0.0.1:9999": "http://127.0.0.1:9999/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
		"[::1]:8080":     "http://[::1]:8080/healthz",
	}

	for addr, want := range tests {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()

			got, err := healthEndpoint(addr)
			if err != nil {
				t.Fatalf("healthEndpoint(%q): %v", addr, err)
			}
			if got != want {
				t.Errorf("healthEndpoint(%q) = %q, want %q", addr, got, want)
			}
		})
	}
}

func TestHealthEndpointRejectsAnAddressWithNoPort(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"", "8080", "::", "127.0.0.1"} {
		if _, err := healthEndpoint(addr); err == nil {
			t.Errorf("healthEndpoint(%q) succeeded, want an error", addr)
		}
	}
}

func TestHealthcheckSubcommandRejectsBadInvocations(t *testing.T) {
	t.Parallel()

	getenv, _, _ := envWithPaths(t, nil)

	tests := map[string][]string{
		"stray argument":    {"healthcheck", "extra"},
		"unknown flag":      {"healthcheck", "--everything"},
		"zero timeout":      {"healthcheck", "--timeout", "0s"},
		"negative timeout":  {"healthcheck", "--timeout", "-1s"},
		"malformed timeout": {"healthcheck", "--timeout", "soon"},
	}

	for label, args := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			if err := run(t.Context(), args, getenv, io.Discard); err == nil {
				t.Errorf("run(%v) succeeded, want an error", args)
			}
		})
	}
}

// --- verify ------------------------------------------------------------------

// Verify is what makes "we have a backup" checkable, so what it reports has to
// be true of the file in front of it -- not merely plausible.
func TestVerifySubcommandAcceptsARealDatabase(t *testing.T) {
	t.Parallel()

	getenv, dbPath, _ := envWithPaths(t, nil)
	ctx := t.Context()

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := migrations.Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO currencies (code, name, symbol, minor_unit) VALUES ('AAA', 'Reported', NULL, 2)"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// The counts the report must agree with, read independently.
	var currencies int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM currencies").Scan(&currencies); err != nil {
		t.Fatalf("counting currencies: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}

	var out strings.Builder
	if err := run(ctx, []string{"verify", "--file", dbPath}, getenv, &out); err != nil {
		t.Fatalf("verify: %v", err)
	}

	report := out.String()
	for _, want := range []string{
		"integrity:      ok",
		fmt.Sprintf("schema version: %d", latest),
		fmt.Sprintf("  %-18s %d", "currencies", currencies),
		fmt.Sprintf("  %-18s %d", "transactions", 0),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not contain %q:\n%s", want, report)
		}
	}
}

// Verify must refuse anything it cannot vouch for. A command that says "ok" to a
// file that is not a recoverable database is worse than no command at all.
func TestVerifySubcommandRejectsWhatItCannotVouchFor(t *testing.T) {
	t.Parallel()

	getenv, _, _ := envWithPaths(t, nil)
	ctx := t.Context()
	dir := t.TempDir()

	notSQLite := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(notSQLite, []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("writing the garbage file: %v", err)
	}

	// A real SQLite database that is not a Finance Tracker one: no migration
	// history, so nothing says what schema it is supposed to have.
	foreign := filepath.Join(dir, "foreign.db")
	other, err := database.Open(ctx, foreign)
	if err != nil {
		t.Fatalf("creating the foreign database: %v", err)
	}
	if _, err := other.ExecContext(ctx, "CREATE TABLE unrelated (id INTEGER PRIMARY KEY) STRICT"); err != nil {
		t.Fatalf("seeding the foreign database: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("closing the foreign database: %v", err)
	}

	tests := map[string][]string{
		"no file given":                  {"verify"},
		"a blank file":                   {"verify", "--file", ""},
		"a file that does not exist":     {"verify", "--file", filepath.Join(dir, "absent.db")},
		"a directory":                    {"verify", "--file", dir},
		"a file that is not SQLite":      {"verify", "--file", notSQLite},
		"a database that is not ours":    {"verify", "--file", foreign},
		"a stray argument":               {"verify", "--file", notSQLite, "extra"},
		"an unknown flag":                {"verify", "--everything"},
		"a backup name instead of path":  {"verify", "--file", "finance-20260101T000000Z-manual.db"},
		"a relative path that is absent": {"verify", "--file", "./nowhere/finance.db"},
	}

	for label, args := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			if err := run(t.Context(), args, getenv, io.Discard); err == nil {
				t.Errorf("run(%v) succeeded, want an error", args)
			}
		})
	}
}
