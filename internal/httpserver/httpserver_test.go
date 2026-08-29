package httpserver_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/httpserver"
)

// discardLogger keeps test output readable while still exercising every logging
// path in the handlers.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newTestDB opens a real SQLite database in a temporary directory. The health
// endpoint's whole job is to report on SQLite, so substituting a mock would
// test nothing.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "finance.db"))
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	return db
}

func TestHealthzReportsOK(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})

	srv := httpserver.New(discardLogger(), db)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body %q is not valid JSON: %v", body, err)
	}
	if payload.Status != "ok" {
		t.Errorf("status field = %q, want %q", payload.Status, "ok")
	}
}

// Closing a real database is how an unreachable database is simulated: no mock,
// and no dependence on timing or on an error string.
func TestHealthzReportsUnavailableWhenDatabaseIsDown(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("closing database: %v", err)
	}

	srv := httpserver.New(discardLogger(), db)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusServiceUnavailable)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body %q is not valid JSON: %v", body, err)
	}
	if payload.Status == "ok" {
		t.Error(`status field is "ok" even though the database is closed`)
	}
}

// ServeMux matches on method, so anything other than GET must not reach the
// handler.
func TestHealthzRejectsNonGET(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})

	srv := httpserver.New(discardLogger(), db)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}
}

// A server with no timeouts lets one stalled client hold a connection forever,
// and the zero value of http.Server is exactly that. Pin them down.
func TestServerHasTimeoutsConfigured(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing database: %v", err)
		}
	})

	srv := httpserver.New(discardLogger(), db)

	if srv.ReadHeaderTimeout != httpserver.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, httpserver.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != httpserver.ReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", srv.ReadTimeout, httpserver.ReadTimeout)
	}
	if srv.WriteTimeout != httpserver.WriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", srv.WriteTimeout, httpserver.WriteTimeout)
	}
	if srv.IdleTimeout != httpserver.IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", srv.IdleTimeout, httpserver.IdleTimeout)
	}
	if srv.MaxHeaderBytes != httpserver.MaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, httpserver.MaxHeaderBytes)
	}
	if srv.ErrorLog == nil {
		t.Error("ErrorLog is nil; net/http would write unstructured errors to stderr")
	}
	// Addr must stay empty: main creates the listener so that a port conflict
	// is reported before the process claims to be listening.
	if srv.Addr != "" {
		t.Errorf("Addr = %q, want it left empty for the caller's listener", srv.Addr)
	}
}
