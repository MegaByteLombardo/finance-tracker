package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// healthCheckTimeout bounds the database probe. The pool is intentionally
// limited to a single connection, so a health check can end up queued behind a
// slow query; this keeps the endpoint from hanging when that happens.
const healthCheckTimeout = 2 * time.Second

// healthResponse is the body of GET /healthz.
type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz reports whether the process is alive and SQLite still answers.
//
// It logs only on failure. A health endpoint is typically polled on a short
// interval, and a line per successful probe would bury everything else.
func handleHealthz(logger *slog.Logger, db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		defer cancel()

		if err := db.PingContext(ctx); err != nil {
			logger.ErrorContext(ctx, "health check failed: database is unreachable",
				slog.Any("error", err))
			writeJSON(ctx, w, logger, http.StatusServiceUnavailable,
				healthResponse{Status: "unavailable"})
			return
		}

		writeJSON(ctx, w, logger, http.StatusOK, healthResponse{Status: "ok"})
	})
}

// writeJSON sends payload with the given status code.
//
// The body is marshalled into memory first: once WriteHeader has been called
// the status is fixed, so an encoding failure discovered mid-stream could no
// longer be reported honestly.
func writeJSON(ctx context.Context, w http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.ErrorContext(ctx, "encoding response body", slog.Any("error", err))
		body, status = []byte(`{"status":"error"}`), http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// The client may have gone away mid-write. Nothing can be done about it at
	// this point, but it is not something to swallow silently either.
	if _, err := w.Write(body); err != nil {
		logger.ErrorContext(ctx, "writing response body", slog.Any("error", err))
	}
}
