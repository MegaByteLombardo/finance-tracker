package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// testEnv wires a run() into a temporary database and an ephemeral port.
func testEnv(t *testing.T, overrides map[string]string) func(string) string {
	t.Helper()

	env := map[string]string{
		"APP_ENV":   "development",
		"HTTP_ADDR": "127.0.0.1:0",
		"DB_PATH":   filepath.Join(t.TempDir(), "finance.db"),
		"LOG_LEVEL": "error",
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
	go func() { done <- run(ctx, testEnv(t, map[string]string{"LOG_LEVEL": "info"}), logWriter) }()

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

	err := run(t.Context(), testEnv(t, map[string]string{"APP_ENV": "staging"}), io.Discard)
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

	runErr := run(t.Context(), testEnv(t, map[string]string{
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

	runErr := run(t.Context(), testEnv(t, map[string]string{
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
