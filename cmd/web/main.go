// Command web runs Finance Tracker.
//
// With no arguments it serves HTTP, which is what the container does. It also
// carries three subcommands:
//
//	finance-tracker restore --file finance-20260905T183045Z-manual.db
//	finance-tracker verify --file /tmp/finance-restore-test.db
//	finance-tracker healthcheck
//
// They live in this binary rather than in separate ones because each needs
// exactly what this binary already has -- the configuration, the database
// conventions, the backup package and the migration history -- and a second
// binary would have to be built, shipped and kept in step for no gain.
//
// There is a second reason, which production made concrete: the runtime image
// is gcr.io/distroless/static. It has no shell, no curl and no sqlite3. Whatever
// an operator or an orchestrator needs to run against a deployment is reachable
// through this executable or it is not reachable at all.
//
// This file is the composition root: it is the only place that wires the
// packages together. There is no dependency injection framework, and there does
// not need to be.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"strings"
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

	// defaultHealthcheckTimeout bounds the healthcheck subcommand end to end.
	//
	// It is longer than the 2s database probe behind /healthz so that a slow
	// answer is reported as slow rather than as dead: the pool holds a single
	// connection, so a health check can legitimately queue behind a write.
	defaultHealthcheckTimeout = 5 * time.Second

	// maxHealthBodyBytes caps what the health check reads. /healthz answers with
	// a few dozen bytes. A response larger than this is something other than
	// Finance Tracker answering on that port, and reading it without a bound
	// would be trusting it.
	maxHealthBodyBytes = 4 << 10
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
	case "verify":
		return verifyCommand(ctx, args[1:], stdout)
	case "healthcheck":
		return healthcheckCommand(ctx, cfg, args[1:], stdout)
	default:
		return fmt.Errorf(
			"unknown command %q: expected \"serve\", \"restore\", \"verify\" or \"healthcheck\"",
			command)
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

// healthcheckCommand probes the running server's /healthz and turns the answer
// into an exit code.
//
// It exists so the image can carry a HEALTHCHECK. The runtime base is
// gcr.io/distroless/static -- no shell, no curl -- so the usual
// `HEALTHCHECK CMD curl -f http://localhost:8080/healthz` form is simply not
// available. What the image does have is this binary, and this binary already
// knows the address the server was told to listen on. That last part is the
// point: a health check that probes a hard-coded port is one that passes against
// the wrong process the first time somebody moves the port.
//
// This closes the carry-over recorded in docs/milestones.md, which named the
// clean approach -- a self-check flag on the binary -- and parked it until
// something was orchestrating restarts. Production is that something.
func healthcheckCommand(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are returned, not printed by the flag package

	timeout := flags.Duration("timeout", defaultHealthcheckTimeout,
		"how long to wait for the server to answer")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("healthcheck: unexpected argument %q", flags.Arg(0))
	}
	if *timeout <= 0 {
		return fmt.Errorf("healthcheck: --timeout must be positive, got %s", *timeout)
	}

	endpoint, err := healthEndpoint(cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: building a request for %s: %w", endpoint, err)
	}

	// A client of its own, with keep-alives disabled: this process makes exactly
	// one request and then exits, so a pooled connection is only something left
	// behind. http.DefaultClient would also do, but it has no timeout of its
	// own and sharing it hides that the deadline here comes from the context.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: requesting %s: %w", endpoint, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing the health check response", slog.Any("error", cerr))
		}
	}()

	var report healthReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHealthBodyBytes)).Decode(&report); err != nil {
		return fmt.Errorf("healthcheck: %s answered %s, but the body is not a health report: %w",
			endpoint, resp.Status, err)
	}

	// Both halves are checked. The status code alone would accept a 200 from
	// whatever else happened to be listening; the body alone would accept a 503
	// that still said "ok".
	if resp.StatusCode != http.StatusOK || report.Status != healthStatusOK {
		return fmt.Errorf("healthcheck: %s answered %s and reported %q",
			endpoint, resp.Status, report.Status)
	}

	if _, err := fmt.Fprintln(stdout, healthStatusOK); err != nil {
		return fmt.Errorf("healthcheck: writing the result: %w", err)
	}
	return nil
}

// healthStatusOK is the value /healthz reports when the process is serving and
// SQLite still answers. It is duplicated from internal/httpserver rather than
// exported from it, because one string is not worth widening that package's API
// and a test asserts the two agree.
const healthStatusOK = "ok"

// healthReport is the body of GET /healthz, as this command needs to read it.
type healthReport struct {
	Status string `json:"status"`
}

// healthEndpoint turns the address the server listens on into one a client can
// dial.
//
// An empty host in HTTP_ADDR means "every interface"; 0.0.0.0 and :: say the
// same thing explicitly. None of the three is an address anything can connect
// to, so the probe goes to the loopback -- which is where the server is
// listening in all three cases, and the only place this check is ever meant to
// reach. A health check that left the machine would be measuring the network.
func healthEndpoint(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("HTTP_ADDR=%q is not a host:port address: %w", addr, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

// verifyCommand checks that a file is a Finance Tracker database and reports
// what is in it.
//
// It is what turns "we have a backup" into a claim something can check. The work
// was already written: backup.Validate runs PRAGMA integrity_check -- the full
// one, not quick_check -- confirms the file carries a migration history, and
// confirms it has the tables belonging to the schema version it records. This
// puts that behind an exit code, so rehearsing a restore can be a script rather
// than a memory.
//
// It then prints the schema version and a row count per table, because
// "something was restored" and "my data was restored" are different claims and
// an integrity check only makes the first one.
//
// Unlike restore, --file takes a PATH. The two differ because they are doing
// different things: restore resolves a name inside the backup directory and must
// never be talked into reaching outside it, whereas verify is pointed at an
// arbitrary file an operator has just produced -- a Litestream restore under
// /tmp, typically -- and opens it READ-ONLY, so looking at it cannot change it.
func verifyCommand(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are returned, not printed by the flag package

	file := flags.String("file", "",
		"path to the database file to verify (a path, not a backup name)")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if *file == "" {
		return errors.New("verify: --file is required; give the path of a database file")
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("verify: unexpected argument %q", flags.Arg(0))
	}

	// The gate. Everything printed afterwards is only worth printing because
	// this passed.
	if err := backup.Validate(ctx, *file); err != nil {
		return fmt.Errorf("verify %s: %w", *file, err)
	}

	report, err := describeDatabase(ctx, *file)
	if err != nil {
		return fmt.Errorf("verify %s: %w", *file, err)
	}
	if err := report.write(stdout); err != nil {
		return fmt.Errorf("verify: writing the report: %w", err)
	}
	return nil
}

// databaseReport is what verify prints after the integrity gate passes.
type databaseReport struct {
	Path          string
	SizeBytes     int64
	SchemaVersion int64

	// Counts is a slice rather than a map so the order is the one
	// migrations.ExpectedTables returns, which is sorted and stable. A map would
	// print in a different order on every run, which makes two reports
	// impossible to diff.
	Counts []tableCount
}

// tableCount is one business table and how many rows it holds.
type tableCount struct {
	Table string
	Rows  int64
}

// write renders the report.
//
// The lines are assembled first and written once, so there is exactly one error
// to handle rather than one per line.
func (r databaseReport) write(w io.Writer) error {
	lines := []string{
		fmt.Sprintf("database:       %s", r.Path),
		fmt.Sprintf("size:           %d bytes", r.SizeBytes),
		fmt.Sprintf("integrity:      %s", healthStatusOK),
		fmt.Sprintf("schema version: %d", r.SchemaVersion),
		"rows:",
	}
	for _, count := range r.Counts {
		lines = append(lines, fmt.Sprintf("  %-18s %d", count.Table, count.Rows))
	}

	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		return err
	}
	return nil
}

// describeDatabase reads the facts verify reports.
//
// It opens the file a second time rather than threading a connection out of
// backup.Validate, which closes its own. Two read-only opens of a file this size
// cost nothing, and the alternative would be widening that package's API for a
// command-line convenience.
func describeDatabase(ctx context.Context, path string) (databaseReport, error) {
	info, err := os.Stat(path)
	if err != nil {
		return databaseReport{}, fmt.Errorf("reading %q: %w", path, err)
	}

	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return databaseReport{}, err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing the database being verified", slog.Any("error", cerr))
		}
	}()

	report := databaseReport{Path: path, SizeBytes: info.Size()}

	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1",
	).Scan(&report.SchemaVersion); err != nil {
		return databaseReport{}, fmt.Errorf("reading the schema version: %w", err)
	}

	// The tables belonging to the version this file records, not to today's
	// binary. A snapshot taken before the current schema is a perfectly good
	// snapshot -- bringing one forward through the ordinary startup path is what
	// restore is for -- and asking it for a table it predates would report a
	// problem that is not one.
	tables, err := migrations.ExpectedTables(report.SchemaVersion)
	if err != nil {
		return databaseReport{}, err
	}

	for _, table := range tables {
		var rows int64
		// The name comes from migrations.ExpectedTables, a fixed list compiled
		// into this binary, never from input. Interpolation is the only way to
		// name a table in SQL, and this is the case where it is safe.
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&rows); err != nil {
			return databaseReport{}, fmt.Errorf("counting %s: %w", table, err)
		}
		report.Counts = append(report.Counts, tableCount{Table: table, Rows: rows})
	}

	return report, nil
}
