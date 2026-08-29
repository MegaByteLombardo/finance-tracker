package main

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
func TestRunShutsDownCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- run(ctx, testEnv(t, nil), io.Discard) }()

	// Let startup reach the listening state first; a shutdown that races
	// startup would not prove much.
	time.Sleep(250 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned an error on clean shutdown: %v", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("run did not return after the context was cancelled")
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
