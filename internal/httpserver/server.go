// Package httpserver builds the application's HTTP server.
//
// It uses net/http directly. Since Go 1.22 the standard ServeMux matches on
// method and path wildcards ("GET /accounts/{id}"), which was the one thing a
// third-party router used to be needed for.
package httpserver

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

// Server timeouts. Without these a single slow or malicious client can hold a
// connection open indefinitely, so net/http's zero-value "no timeout" defaults
// are never the right choice for a server that listens on a real socket.
const (
	// ReadHeaderTimeout bounds how long a client may take to send its request
	// headers -- the cheapest defence against a Slowloris-style stall.
	ReadHeaderTimeout = 5 * time.Second
	// ReadTimeout bounds reading the entire request, headers and body.
	ReadTimeout = 10 * time.Second
	// WriteTimeout bounds writing the response.
	WriteTimeout = 15 * time.Second
	// IdleTimeout bounds how long a keep-alive connection may sit unused.
	IdleTimeout = 60 * time.Second
	// MaxHeaderBytes caps the request header size.
	MaxHeaderBytes = 1 << 20 // 1 MiB
)

// New builds the server and its routes.
//
// Addr is deliberately left unset: the caller creates the listener itself and
// calls Serve. That way a port conflict surfaces as an error before the process
// claims to be listening, and binding to :0 yields a usable address.
func New(logger *slog.Logger, db *sql.DB) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(logger, db))

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		// Route net/http's own error reporting through slog instead of letting
		// it write unstructured lines to stderr.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}
