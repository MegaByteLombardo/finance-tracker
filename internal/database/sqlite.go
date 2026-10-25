// Package database owns every decision about how this application opens SQLite.
//
// Nothing else in the codebase builds a DSN, tunes the pool, or sets a PRAGMA.
// Keeping that in one place is what makes the guarantees below checkable.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// driverName is the name modernc.org/sqlite registers itself under.
const driverName = "sqlite"

// Connection settings. These are deliberately constants rather than
// configuration: they are correctness decisions, not deployment knobs.
const (
	// BusyTimeoutMS bounds how long SQLite waits for a lock before returning
	// SQLITE_BUSY. Within this process the pool is serialised (see Open), so
	// this only matters against *other* processes touching the same file --
	// a sqlite3 CLI session, or a second container.
	BusyTimeoutMS = 5000

	// Synchronous is FULL, not NORMAL. SQLite documents that in WAL mode a
	// transaction committed with synchronous=NORMAL might roll back following
	// a power loss or system crash: consistent, but not durable. Losing an
	// acknowledged transaction is not acceptable for financial records, and
	// the cost is one extra fsync per commit on a workload of a few dozen
	// writes a day.
	Synchronous = "FULL"

	// JournalMode is a persistent property of the database file rather than a
	// per-connection setting, so it is applied once in Open instead of via the
	// DSN. See enableWAL.
	JournalMode = "wal"

	// TxLock is the BEGIN mode of every read-write transaction. IMMEDIATE takes
	// the write lock when the transaction opens; see dsn and TD-075.
	TxLock = "immediate"
)

// Open creates the database file (and its parent directory) if needed, opens it
// with the correct settings, and verifies that those settings actually took
// effect. It returns a pool that is ready to use, or an error; it never returns
// a half-configured handle.
//
// # Why the PRAGMAs live in the DSN
//
// database/sql is a *pool*. Running db.Exec("PRAGMA foreign_keys = ON") once
// after sql.Open configures exactly one arbitrary physical connection and
// leaves every other connection the pool later opens at the SQLite default of
// OFF. That failure is silent: foreign keys simply stop being enforced on some
// queries. Encoding the connection-scoped PRAGMAs in the DSN instead makes the
// driver apply them on every Connect, which is the only way to state the
// guarantee for the whole pool.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving database path %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("creating database directory for %q: %w", abs, err)
	}

	db, err := sql.Open(driverName, dsn(abs))
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", abs, err)
	}

	configurePool(db)

	// sql.Open is lazy, so nothing has touched the file yet. Ping forces a real
	// connection and turns an unusable path into an error here rather than on
	// the first request.
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("connecting to database %q: %w", abs, err), db.Close())
	}

	if err := enableWAL(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return db, nil
}

// dsn builds the connection string. abs must already be absolute.
//
// Only connection-scoped PRAGMAs go here. The shorthand keys are used in
// preference to the general _pragma= form because the driver validates all of
// them before applying any, so a malformed DSN fails cleanly instead of leaving
// a connection half-configured.
//
// _txlock=immediate makes every read-write BeginTx issue BEGIN IMMEDIATE, so a
// transaction takes the write lock when it opens rather than at its first
// write (TD-075). M7 is the first write path that reads, decides and then
// writes on the strength of what it read -- an availability or a pending amount
// -- and under DEFERRED a second process writing between the read and the write
// would make the upgrade fail with SQLITE_BUSY after the decision was taken.
// Taking the lock up front moves the only wait to BEGIN, where busy_timeout
// governs it. Within this process the single connection already serialises
// everything, so nothing changes there. A ReadOnly transaction is unaffected:
// the driver issues a plain BEGIN for it.
func dsn(abs string) string {
	// file:///abs/path on Unix, file:///C:/abs/path on Windows -- the URI form
	// SQLite documents for both platforms. Going through url.URL also
	// percent-encodes spaces and other characters in the path.
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	u := url.URL{
		Scheme: "file",
		Path:   p,
		RawQuery: url.Values{
			"_busy_timeout": {strconv.Itoa(BusyTimeoutMS)},
			"_foreign_keys": {"1"},
			"_synchronous":  {Synchronous},
			"_txlock":       {TxLock},
		}.Encode(),
	}
	return u.String()
}

// configurePool sizes the pool for SQLite specifically.
//
// The usual advice -- a handful of connections and a five minute
// ConnMaxLifetime -- is written for network databases, where connections are
// expensive to establish and get killed out from under you by load balancers
// and idle timeouts. SQLite is an in-process library: there is no socket, no
// handshake and no failover, so recycling a connection buys nothing and only
// costs reopening the file and re-applying the PRAGMAs.
//
// A single open connection serialises all access from this process, which makes
// SQLITE_BUSY structurally impossible here. The cost is that reads are
// serialised too; for a single-user local application that is not a real
// constraint. If it ever becomes one, the answer is two pools over the same
// file -- one writer, and a read-only pool with _query_only=true -- which WAL
// supports directly.
func configurePool(db *sql.DB) {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // never expire
	db.SetConnMaxIdleTime(0) // never expire while idle
}

// enableWAL switches the database to write-ahead logging and confirms it.
//
// This is done once here rather than through the DSN for two reasons. WAL is a
// persistent property of the database file -- it survives closing and
// reopening -- so re-issuing it on every pooled connection is redundant work
// that can return SQLITE_BUSY under concurrency. And more importantly,
// "PRAGMA journal_mode = WAL" does not fail when it cannot be applied: it
// returns the mode the database ended up in. Taking that at face value is
// exactly the kind of silently-ignored result this project refuses to ship, so
// the returned value is checked.
func enableWAL(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return fmt.Errorf("setting journal_mode to WAL: %w", err)
	}
	if !strings.EqualFold(mode, JournalMode) {
		return fmt.Errorf("journal_mode is %q after requesting WAL (an in-memory or read-only database cannot use WAL)", mode)
	}
	return nil
}

// Pragmas reports the settings actually in effect on a connection, as opposed
// to the ones we asked for. It backs both the startup log line and the tests
// that assert the configuration above is real.
type Pragmas struct {
	JournalMode   string
	ForeignKeys   bool
	BusyTimeoutMS int
	Synchronous   int
}

// Querier is the subset of *sql.DB that ReadPragmas needs. *sql.Conn satisfies
// it too, which is what lets a test read the PRAGMAs of several individual
// physical connections and prove the DSN applies to all of them, not just to
// whichever one the pool happens to hand out.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ReadPragmas queries the effective settings.
func ReadPragmas(ctx context.Context, q Querier) (Pragmas, error) {
	var p Pragmas

	if err := q.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&p.JournalMode); err != nil {
		return Pragmas{}, fmt.Errorf("reading journal_mode: %w", err)
	}
	if err := q.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&p.ForeignKeys); err != nil {
		return Pragmas{}, fmt.Errorf("reading foreign_keys: %w", err)
	}
	if err := q.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&p.BusyTimeoutMS); err != nil {
		return Pragmas{}, fmt.Errorf("reading busy_timeout: %w", err)
	}
	if err := q.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&p.Synchronous); err != nil {
		return Pragmas{}, fmt.Errorf("reading synchronous: %w", err)
	}

	return p, nil
}

// SynchronousName renders the numeric synchronous level using the SQLite names.
func (p Pragmas) SynchronousName() string {
	switch p.Synchronous {
	case 0:
		return "OFF"
	case 1:
		return "NORMAL"
	case 2:
		return "FULL"
	case 3:
		return "EXTRA"
	default:
		return strconv.Itoa(p.Synchronous)
	}
}

// LogValue groups the PRAGMAs into a single structured log attribute.
func (p Pragmas) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("journal_mode", p.JournalMode),
		slog.Bool("foreign_keys", p.ForeignKeys),
		slog.Int("busy_timeout_ms", p.BusyTimeoutMS),
		slog.String("synchronous", p.SynchronousName()),
	)
}

// CheckForeignKeys confirms that foreign key enforcement is ON for the
// connection q reaches.
//
// It exists because one migration (00019, TD-088) has to switch enforcement
// OFF to rebuild a parent table, and a connection left with it OFF would
// accept orphans silently for the rest of the process. The DSN turns it on for
// every new connection; this is what proves the one connection the pool keeps
// came back from that migration the way it went in. It lives here because this
// package owns every PRAGMA.
func CheckForeignKeys(ctx context.Context, q Querier) error {
	var enabled bool
	if err := q.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return fmt.Errorf("reading foreign_keys: %w", err)
	}
	if !enabled {
		return errors.New("foreign_keys is OFF on the database connection; foreign keys would not be enforced")
	}
	return nil
}
