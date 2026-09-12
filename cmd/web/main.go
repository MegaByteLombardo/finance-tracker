// Command web runs Finance Tracker.
//
// With no arguments it serves HTTP, which is what the container does. It also
// carries one offline subcommand:
//
//	finance-tracker restore --file finance-20260905T183045Z-manual.db
//
// Restore lives in this binary rather than a second one because it needs
// exactly what this binary already has -- the configuration, the database
// conventions and the backup package -- and a second binary would have to be
// built, shipped and kept in step for no gain.
//
// This file is the composition root: it is the only place that wires the
// packages together. There is no dependency injection framework, and there does
// not need to be.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
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

	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/config"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/httpserver"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
	"github.com/MegaByteLombardo/finance-tracker/internal/web/adapter"
)

const (
	// shutdownTimeout bounds how long in-flight requests get to finish.
	shutdownTimeout = 10 * time.Second

	// backupTimeout bounds the snapshot taken on the way out.
	//
	// It is separate from shutdownTimeout so that a slow drain cannot eat the
	// whole budget and leave nothing for the backup. Both together must stay
	// under the container's stop grace period, which compose.yaml raises to 30s
	// for exactly this reason: past it, Docker sends SIGKILL and the snapshot is
	// abandoned.
	backupTimeout = 10 * time.Second
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		// The logger may not exist yet if configuration failed, so this goes
		// through the default logger. Exiting non-zero matters more than the
		// formatting here.
		slog.Error("finance-tracker exited with an error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run loads the configuration, builds the logger, and dispatches to a command.
//
// args is os.Args[1:]. getenv and stdout are parameters rather than direct
// references to os so that a test can drive a complete cycle.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	logger := newLogger(cfg, stdout)
	slog.SetDefault(logger)

	// No subcommand means serve, which keeps the container's ENTRYPOINT and
	// every existing invocation working unchanged.
	if len(args) == 0 {
		return serve(ctx, cfg, logger)
	}

	switch command := args[0]; command {
	case "serve":
		return serve(ctx, cfg, logger)
	case "restore":
		return restoreCommand(ctx, cfg, logger, args[1:])
	default:
		return fmt.Errorf("unknown command %q: expected \"serve\" or \"restore\"", command)
	}
}

// serve runs the HTTP server until a signal arrives, then shuts down in order.
//
// Every failure is a returned error rather than a panic or a bare os.Exit
// buried in a helper.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	// SIGINT is Ctrl+C; SIGTERM is what `docker compose stop` sends.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting finance-tracker",
		slog.String("env", string(cfg.Env)),
		slog.String("db_path", cfg.DBPath),
		slog.String("backup_dir", cfg.BackupDir),
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

	// Built before migrations run, because the first thing it does is take a
	// snapshot of the schema they are about to change.
	//
	// A backup directory that cannot be created ends startup. Running without
	// somewhere to put snapshots is the state that cost this project its data
	// once already, and doing it silently is how that happened.
	backups, err := backup.NewService(db, cfg.BackupDir, cfg.BackupRetention, logger)
	if err != nil {
		return errors.Join(fmt.Errorf("preparing backups: %w", err), db.Close())
	}

	if err := backupBeforeMigrating(ctx, db, backups, logger); err != nil {
		return errors.Join(err, db.Close())
	}

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

	// The exchange-rate service, and the external provider behind it if one is
	// configured.
	//
	// A provider that cannot be BUILT -- a malformed URL, an unreadable embedded
	// certificate -- is a configuration error and ends startup, because nothing
	// about it will fix itself. A provider that cannot be REACHED is not: that
	// is handled later, at fetch time, and never stops this process.
	var provider fx.RateProvider
	if cfg.FXProviderEnabled {
		bcv, perr := fx.NewBCVProvider(cfg.FXProviderURL, nil, cfg.FXTimeout)
		if perr != nil {
			return errors.Join(fmt.Errorf("preparing the exchange rate provider: %w", perr), db.Close())
		}
		provider = bcv
	} else {
		logger.Info("the automatic exchange rate provider is disabled by configuration")
	}
	rates := fx.NewService(db, provider, logger)

	webSvcs := adapter.New(db, backups, rates)
	webRouter := web.NewRouter(logger, webSvcs, web.DefaultConfig())

	srv := httpserver.New(logger, db, webRouter)

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

	// The background rate refresher starts AFTER the server is listening, and
	// never before.
	//
	// This ordering is the whole resilience decision, stated as code. Exchange
	// rates come from a government website that is regularly slow, occasionally
	// unreachable, and entirely outside this application's control. Fetching
	// them during startup -- the way migrations run during startup -- would make
	// somebody else's downtime into this application's downtime, for a feature
	// that only changes how a total is PRESENTED.
	//
	// So: accounts, the ledger, balances, manual rates and every stored rate are
	// available the moment the listener opens. The refresher then catches up in
	// its own time, and every failure it meets is a log line.
	refreshCtx, stopRefresher := context.WithCancel(context.WithoutCancel(ctx))
	refresherDone := make(chan struct{})
	go func() {
		defer close(refresherDone)
		fx.NewRefresher(rates, cfg.FXRefreshInterval, logger).Run(refreshCtx)
	}()

	select {
	case err := <-serveErr:
		// The server fell over on its own; the listener is already closed.
		// The refresher still has to be stopped and waited for before the
		// database is closed underneath it.
		stopRefresher()
		<-refresherDone
		return errors.Join(fmt.Errorf("http server: %w", err), db.Close())
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Restore default signal handling: a second Ctrl+C should be able to kill a
	// process that is wedged mid-shutdown.
	stop()

	// The refresher is stopped before the drain, not after: a fetch in flight
	// holds the single SQLite connection, and the database must not be closed
	// underneath it. Its context is cancelled and it is waited for.
	stopRefresher()
	<-refresherDone

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

	// Taken after the drain and before the close, which is the only moment the
	// database is both complete and still open.
	//
	// A failure here is reported and makes the process exit non-zero, but it
	// does not stop the database being closed: an unclean close would turn a
	// missing snapshot into a damaged database.
	//
	// This is also the trigger that would have saved the data lost after M2:
	// `docker compose down` sends SIGTERM and waits before destroying anything.
	backupCtx, backupCancel := context.WithTimeout(context.WithoutCancel(ctx), backupTimeout)
	defer backupCancel()

	if _, err := backups.Create(backupCtx, backup.Shutdown); err != nil {
		logger.Error("the shutdown backup failed", slog.Any("error", err))
		errs = append(errs, fmt.Errorf("shutdown backup: %w", err))
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

// backupBeforeMigrating takes a snapshot when migrations are about to change a
// schema that already exists.
//
// The condition is deliberately narrow. A brand-new database has nothing worth
// snapshotting, and a database already at the latest version is not about to
// change, so neither produces a file. What is left is the case the snapshot
// exists for: an existing database, with data in it, about to be altered.
//
// A failure here ends startup. Being unable to take this particular snapshot is
// precisely the moment not to proceed -- everything after it is irreversible
// without one.
func backupBeforeMigrating(
	ctx context.Context, db *sql.DB, backups *backup.Service, logger *slog.Logger,
) error {
	pending, err := migrations.PendingOnExistingSchema(ctx, db, logger)
	if err != nil {
		return fmt.Errorf("checking for pending migrations: %w", err)
	}
	if !pending {
		return nil
	}

	logger.Info("migrations are pending on an existing database; taking a snapshot first")
	created, err := backups.Create(ctx, backup.PreMigration)
	if err != nil {
		return fmt.Errorf("pre-migration backup: %w", err)
	}
	logger.Info("pre-migration backup taken", slog.String("backup", created.Name))
	return nil
}

// restoreCommand replaces the operational database with a snapshot.
//
// It is offline on purpose: it must not run while the server is serving, since
// replacing the file underneath open connections would leave handlers reading a
// database that no longer exists.
//
//	docker compose stop
//	docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
//	docker compose up -d
//
// It does not migrate afterwards. The next ordinary start does that, which is
// what allows a snapshot from an older schema to be brought forward.
func restoreCommand(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are returned, not printed by the flag package

	file := flags.String("file", "",
		"name of the backup to restore, as listed in the backup directory (filename only)")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if *file == "" {
		return errors.New("restore: --file is required; give the filename of a backup in " + cfg.BackupDir)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("restore: unexpected argument %q", flags.Arg(0))
	}

	logger.Info("restoring database",
		slog.String("backup", *file),
		slog.String("backup_dir", cfg.BackupDir),
		slog.String("db_path", cfg.DBPath),
	)

	if err := backup.Restore(ctx, backup.RestoreOptions{
		Dir:    cfg.BackupDir,
		Name:   *file,
		DBPath: cfg.DBPath,
		Logger: logger,
	}); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	logger.Info("restore complete; start the application normally to bring the schema up to date")
	return nil
}
