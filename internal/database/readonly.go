package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenReadOnly opens an existing database file for inspection.
//
// It differs from Open in three ways that matter when the file being opened is
// a backup rather than the operational database:
//
//   - It refuses to create anything. Open creates the file and its parent
//     directory on demand, which would turn "this backup does not exist" into
//     "here is an empty database".
//   - It opens read-only, so validating an artifact cannot alter it. A
//     read-write open can checkpoint, create a -wal beside the file, or update
//     the header; a backup that changed because someone looked at it is no
//     longer the thing that was taken.
//   - It does not touch journal_mode. That is a property of the file, and this
//     is not our file to reconfigure.
//
// The caller closes the returned pool.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving database path %q: %w", path, err)
	}

	// Checked explicitly so that a missing file is a clear error rather than
	// whatever the driver reports for an unopenable path.
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("reading database %q: %w", abs, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reading database %q: not a regular file", abs)
	}

	db, err := sql.Open(driverName, readOnlyDSN(abs))
	if err != nil {
		return nil, fmt.Errorf("opening database %q read-only: %w", abs, err)
	}

	configurePool(db)

	// sql.Open is lazy. Ping forces a real connection so that a file which is
	// not a database fails here rather than on the first query.
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("connecting to database %q: %w", abs, err), db.Close())
	}

	return db, nil
}

// readOnlyDSN builds a read-only connection string. abs must already be
// absolute.
//
// mode=ro is a SQLite URI parameter rather than one of the driver's shorthand
// keys: it maps to SQLITE_OPEN_READONLY, so the refusal to write comes from
// SQLite itself and not from a convention this package hopes callers follow.
func readOnlyDSN(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	u := url.URL{
		Scheme: "file",
		Path:   p,
		RawQuery: url.Values{
			"mode":          {"ro"},
			"_busy_timeout": {"5000"},
		}.Encode(),
	}
	return u.String()
}
