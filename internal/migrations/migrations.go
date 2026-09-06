// Package migrations owns the database schema and its evolution.
//
// The SQL files live beside this one and are compiled into the binary, so a
// deployment is a single executable: no goose CLI in the runtime image and no
// loose .sql files to keep in sync with it.
//
// This is the only package in the codebase that imports goose. Everything else
// sees one function.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// goose discovers migrations by globbing "*.sql" at the ROOT of the fs.FS, so
// the .sql files must sit next to this file. That is why this package holds
// both, and why no fs.Sub is needed.
//
//go:embed *.sql
var migrationsFS embed.FS

// Up applies every pending migration and reports what happened.
//
// It is safe to call on every start: goose records applied versions in its own
// table, so a database that is already current is a no-op.
//
// Migrations must run before the process accepts traffic. A failure here is
// fatal by design — serving requests against a half-migrated schema would be
// worse than not serving at all.
func Up(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	return up(ctx, db, migrationsFS, logger)
}

// PendingOnExistingSchema reports whether there are migrations to apply to a
// database that already has a schema.
//
// Both halves matter, and they are asked together because the answer is only
// useful as one question: "is a schema this database already has about to
// change?"
//
//   - No applied version means the database is brand new. Everything is about
//     to be created, and a snapshot of nothing is a file nobody will ever want.
//   - No pending migrations means nothing is about to change, so there is
//     nothing to take a snapshot before.
//
// It is what decides whether startup takes a pre-migration backup.
func PendingOnExistingSchema(ctx context.Context, db *sql.DB, logger *slog.Logger) (bool, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsFS, goose.WithSlog(logger))
	if err != nil {
		return false, fmt.Errorf("creating migration provider: %w", err)
	}
	// Deliberately no provider.Close(): it closes the *sql.DB the caller owns.

	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return false, fmt.Errorf("reading schema versions: %w", err)
	}
	return current >= 1 && current < target, nil
}

// up is Up with the migration source injected, so that tests can exercise the
// failure path with a deliberately broken migration without shipping one.
func up(ctx context.Context, db *sql.DB, fsys fs.FS, logger *slog.Logger) error {
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		fsys,
		goose.WithSlog(logger),
	)
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}

	// Deliberately no `defer provider.Close()`.
	//
	// goose's Close() closes the *sql.DB it was handed:
	//
	//     func (p *Provider) Close() error { return p.db.Close() }
	//
	// That database belongs to the caller for the whole life of the process.
	// Closing it here would leave the HTTP server without a database the moment
	// migrations finished. The provider is a transient object; there is nothing
	// else in it to release.

	results, err := provider.Up(ctx)
	if err != nil {
		// A partial failure means some migrations were applied and one failed.
		// Each SQL migration runs in its own transaction and SQLite has
		// transactional DDL, so the failed one rolled back cleanly; the
		// database is left at the last consistent version.
		var partial *goose.PartialError
		if errors.As(err, &partial) {
			return fmt.Errorf("migration %s failed after %d applied successfully: %w",
				partial.Failed.Source.Path, len(partial.Applied), err)
		}
		return fmt.Errorf("applying migrations: %w", err)
	}

	if len(results) == 0 {
		logger.InfoContext(ctx, "database schema already current")
		return nil
	}

	for _, result := range results {
		logger.DebugContext(ctx, "migration applied",
			slog.String("migration", result.Source.Path),
			slog.Int64("version", result.Source.Version),
			slog.Duration("duration", result.Duration),
		)
	}

	logger.InfoContext(ctx, "migrations applied",
		slog.Int("count", len(results)),
		slog.Int64("version", results[len(results)-1].Source.Version),
	)
	return nil
}
