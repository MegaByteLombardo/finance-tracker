// Command web runs the Finance Tracker HTTP server.
//
// This file is the composition root: it is the only place that wires the
// packages together. There is no dependency injection framework, and there does
// not need to be.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/config"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/httpserver"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// shutdown signal arrives.
const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(context.Background(), os.Getenv, os.Stdout); err != nil {
		// The logger may not exist yet if configuration failed, so this goes
		// through the default logger. Exiting non-zero matters more than the
		// formatting here.
		slog.Error("finance-tracker exited with an error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run holds all of the startup and shutdown logic so that every failure is a
// returned error rather than a panic or a bare os.Exit buried in a helper.
//
// getenv and stdout are parameters rather than direct references to os so that
// a test can drive a complete startup/shutdown cycle.
func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	// SIGINT is Ctrl+C; SIGTERM is what `docker compose stop` sends.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	logger := newLogger(cfg, stdout)
	slog.SetDefault(logger)

	logger.Info("starting finance-tracker",
		slog.String("env", string(cfg.Env)),
		slog.String("db_path", cfg.DBPath),
		slog.String("log_level", cfg.LogLevel.String()),
		slog.String("go_version", runtime.Version()),
	)

	db, err := database.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}

	// Report the settings SQLite actually applied, not the ones we asked for.
	pragmas, err := database.ReadPragmas(ctx, db)
	if err != nil {
		return errors.Join(fmt.Errorf("reading sqlite settings: %w", err), db.Close())
	}
	logger.Info("database opened", slog.Any("sqlite", pragmas))

	// Migrations run before anything listens. Serving requests against a
	// half-migrated schema would be worse than not serving at all, so a failure
	// here ends the process.
	if err := migrations.Up(ctx, db, logger); err != nil {
		return errors.Join(fmt.Errorf("migrating database: %w", err), db.Close())
	}

	// The listener is created here rather than through http.Server.Addr so that
	// a port conflict is an error before anything claims to be listening.
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return errors.Join(fmt.Errorf("listening on %q: %w", cfg.HTTPAddr, err), db.Close())
	}

	srv := httpserver.New(logger, db)

	// Serve always returns a non-nil error. ErrServerClosed is the ordinary
	// result of Shutdown, so it is normalised to nil rather than discarded.
	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	logger.Info("http server listening", slog.String("addr", listener.Addr().String()))

	select {
	case err := <-serveErr:
		// The server fell over on its own; the listener is already closed.
		return errors.Join(fmt.Errorf("http server: %w", err), db.Close())
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Restore default signal handling: a second Ctrl+C should be able to kill a
	// process that is wedged mid-shutdown.
	stop()

	// A fresh context -- the one above is already cancelled, and Shutdown needs
	// a live deadline of its own.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	var errs []error

	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("shutting down http server: %w", err))
	}
	// Wait for Serve to return so that no handler is still touching the
	// database when it is closed.
	if err := <-serveErr; err != nil {
		errs = append(errs, fmt.Errorf("http server: %w", err))
	}
	if len(errs) == 0 {
		logger.Info("http server stopped")
	}

	// The database closes last, on purpose: closing it before in-flight
	// requests have drained would break them.
	if err := db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("closing database: %w", err))
	} else {
		logger.Info("database closed")
	}

	return errors.Join(errs...)
}

// newLogger builds the structured logger. The environment picks the format:
// text is readable in a terminal, JSON is what a log collector wants.
func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: cfg.LogLevel,
		// Source locations are noise in normal operation and the first thing
		// you want when debugging.
		AddSource: cfg.LogLevel <= slog.LevelDebug,
	}

	if cfg.Env == config.Production {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
