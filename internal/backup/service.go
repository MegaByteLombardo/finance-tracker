package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
)

// Service takes and manages snapshots of one database.
type Service struct {
	db        *sql.DB
	dir       string
	retention int
	logger    *slog.Logger

	// mu guards the naming state below. Create can be called concurrently --
	// a manual backup from a request while the process is shutting down -- and
	// choosing a name must be atomic with recording it.
	mu sync.Mutex

	// lastKey and lastSeq remember the last name issued for a given second and
	// reason, so sequence numbers only ever move forward.
	//
	// Checking the directory is not enough on its own: pruning DELETES old
	// snapshots, which frees their names, and a later snapshot in the same
	// second would then reuse a name that sorts as older than it is. That would
	// corrupt the listing order and, through it, which backup pruning considers
	// oldest.
	lastKey string
	lastSeq int
}

// NewService builds the service over the pool holding the operational
// database.
//
// dir is created if it does not exist. retention bounds how many shutdown
// snapshots are kept; zero keeps every one.
func NewService(db *sql.DB, dir string, retention int, logger *slog.Logger) (*Service, error) {
	if dir == "" {
		return nil, errors.New("backup directory must not be blank")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving backup directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("creating backup directory %q: %w", abs, err)
	}
	if retention < 0 {
		return nil, fmt.Errorf("backup retention %d must not be negative", retention)
	}

	return &Service{db: db, dir: abs, retention: retention, logger: logger}, nil
}

// Dir is where this service writes snapshots.
func (s *Service) Dir() string { return s.dir }

// Create takes a snapshot of the live database.
//
// The snapshot is written under a .partial name and validated before it is
// renamed into place, so a file bearing a real backup name has always been
// checked. A failure at any point removes the partial file: leaving something
// that looks recoverable and is not would be worse than leaving nothing.
func (s *Service) Create(ctx context.Context, reason Reason) (Backup, error) {
	if !reason.Valid() {
		return Backup{}, fmt.Errorf("unknown backup reason %q", reason)
	}

	at := time.Now().UTC().Truncate(time.Second)
	name, err := s.freeName(at, reason)
	if err != nil {
		return Backup{}, err
	}

	finalPath := filepath.Join(s.dir, name)
	partialPath := finalPath + partialSuffix

	// VACUUM INTO is what makes this safe against a live database: it takes a
	// read transaction and writes a consistent, compacted copy, so the result
	// never contains a half-applied write and never needs the -wal to be
	// complete.
	//
	// The destination must not already exist; freeName guaranteed that.
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", partialPath); err != nil {
		s.removeQuietly(partialPath)
		return Backup{}, fmt.Errorf("writing snapshot %q: %w", name, err)
	}

	if err := Validate(ctx, partialPath); err != nil {
		s.removeQuietly(partialPath)
		return Backup{}, fmt.Errorf("snapshot %q failed validation and was discarded: %w", name, err)
	}

	if err := os.Rename(partialPath, finalPath); err != nil {
		s.removeQuietly(partialPath)
		return Backup{}, fmt.Errorf("naming snapshot %q: %w", name, err)
	}

	info, err := os.Stat(finalPath)
	if err != nil {
		return Backup{}, fmt.Errorf("reading snapshot %q: %w", name, err)
	}

	created, err := parseName(name)
	if err != nil {
		return Backup{}, fmt.Errorf("reading back the name of snapshot %q: %w", name, err)
	}
	created.SizeBytes = info.Size()
	s.logger.InfoContext(ctx, "backup created",
		slog.String("backup", created.Name),
		slog.String("reason", string(created.Reason)),
		slog.Int64("bytes", created.SizeBytes),
	)

	if reason.prunable() {
		// A pruning failure does not invalidate the snapshot that was just
		// taken, so it is reported rather than returned.
		if err := s.prune(ctx); err != nil {
			s.logger.ErrorContext(ctx, "pruning old backups failed", slog.Any("error", err))
		}
	}
	return created, nil
}

// List returns the snapshots in the directory, newest first.
//
// Files that are not backups -- partials, and anything else that happens to be
// in the directory -- are skipped rather than reported: the directory belongs to
// the operator, and this is a listing, not an audit.
func (s *Service) List(ctx context.Context) ([]Backup, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading backup directory %q: %w", s.dir, err)
	}

	backups := make([]Backup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := parseName(entry.Name())
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// The file was removed between the listing and the stat. Nothing
			// here can act on that, and it is not this call's failure.
			s.logger.DebugContext(ctx, "backup vanished while listing",
				slog.String("backup", entry.Name()), slog.Any("error", err))
			continue
		}
		b.SizeBytes = info.Size()
		backups = append(backups, b)
	}

	// Newest first. The sequence suffix breaks ties within a second, which is
	// what makes the order total -- and it must be compared as a number, not as
	// text: "…-manual.db" sorts after "…-manual-2.db" lexicographically while
	// being the older of the two.
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].newer(backups[j])
	})
	return backups, nil
}

// Get returns one snapshot by name, or ErrNotFound.
func (s *Service) Get(ctx context.Context, name string) (Backup, error) {
	b, err := parseName(name)
	if err != nil {
		return Backup{}, err
	}
	info, err := os.Stat(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return Backup{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err != nil {
		return Backup{}, fmt.Errorf("reading backup %q: %w", name, err)
	}
	b.SizeBytes = info.Size()
	return b, nil
}

// Validate reports whether the named snapshot in this directory is usable.
func (s *Service) Validate(ctx context.Context, name string) error {
	if _, err := s.Get(ctx, name); err != nil {
		return err
	}
	return Validate(ctx, filepath.Join(s.dir, name))
}

// Validate reports whether the file at path is a snapshot this application can
// restore from.
//
// It is version-aware on purpose. A snapshot taken before the current schema is
// perfectly good -- restoring one and letting migrations bring it forward is the
// whole point of keeping pre-migration backups -- so the table set it is checked
// against is the one that belonged to the version it records, not today's.
//
// The check is PRAGMA integrity_check rather than quick_check. quick_check skips
// verifying that index contents agree with table contents, which is exactly the
// kind of silent corruption a backup exists to rule out; on a database of this
// size the difference is milliseconds, and the file may be opened in anger years
// from now.
func Validate(ctx context.Context, path string) error {
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBackup, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing a validated backup", slog.Any("error", cerr))
		}
	}()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("%w: integrity check could not run: %s", ErrInvalidBackup, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: integrity check reported %q", ErrInvalidBackup, result)
	}

	// A .db extension proves nothing. This is what distinguishes a snapshot of
	// this application from any other SQLite file.
	var version int64
	err = db.QueryRowContext(ctx,
		"SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1").Scan(&version)
	if err != nil {
		return fmt.Errorf("%w: no migration history; this is not a Finance Tracker database: %s",
			ErrInvalidBackup, err)
	}

	tables, err := migrations.ExpectedTables(version)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBackup, err)
	}
	for _, table := range tables {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			return fmt.Errorf("%w: schema version %d is missing table %q: %s",
				ErrInvalidBackup, version, table, err)
		}
	}
	return nil
}

// freeName picks a filename nothing occupies.
//
// The sequence suffix only appears when a snapshot already exists for the same
// second and reason, which happens when a manual backup is followed immediately
// by a shutdown. Both the final name and its partial are checked, so a snapshot
// still being written cannot have its name taken.
//
// The directory is checked as well as the counter, so a snapshot left by a
// previous run of the process cannot have its name taken either.
func (s *Service) freeName(at time.Time, reason Reason) (string, error) {
	const maxSeq = 1000

	s.mu.Lock()
	defer s.mu.Unlock()

	key := at.Format(timestampLayout) + "-" + string(reason)
	start := 1
	if key == s.lastKey {
		start = s.lastSeq + 1
	}

	for seq := start; seq <= maxSeq; seq++ {
		name := formatName(at, reason, seq)
		path := filepath.Join(s.dir, name)

		_, ferr := os.Stat(path)
		_, perr := os.Stat(path + partialSuffix)
		if errors.Is(ferr, os.ErrNotExist) && errors.Is(perr, os.ErrNotExist) {
			s.lastKey, s.lastSeq = key, seq
			return name, nil
		}
		if ferr != nil && !errors.Is(ferr, os.ErrNotExist) {
			return "", fmt.Errorf("checking for an existing backup %q: %w", name, ferr)
		}
		if perr != nil && !errors.Is(perr, os.ErrNotExist) {
			return "", fmt.Errorf("checking for a partial backup %q: %w", name, perr)
		}
	}
	return "", fmt.Errorf("more than %d backups already exist for %s at %s",
		maxSeq, reason, at.Format(timestampLayout))
}

// prune enforces retention over shutdown snapshots only.
//
// Manual, pre-migration and pre-restore snapshots are never considered. Each of
// those was taken because someone or something decided it was worth keeping,
// and a retention policy that quietly removes them is a policy nobody can trust.
func (s *Service) prune(ctx context.Context) error {
	if s.retention == 0 {
		return nil
	}

	all, err := s.List(ctx)
	if err != nil {
		return err
	}

	prunable := make([]Backup, 0, len(all))
	for _, b := range all {
		if b.Reason.prunable() {
			prunable = append(prunable, b)
		}
	}
	if len(prunable) <= s.retention {
		return nil
	}

	// List is newest first, so everything past the retention count is older
	// than everything kept. The newest is never in this slice.
	var errs []error
	for _, b := range prunable[s.retention:] {
		if err := os.Remove(filepath.Join(s.dir, b.Name)); err != nil {
			errs = append(errs, fmt.Errorf("removing old backup %q: %w", b.Name, err))
			continue
		}
		s.logger.InfoContext(ctx, "old backup pruned",
			slog.String("backup", b.Name),
			slog.Int("retention", s.retention),
		)
	}
	return errors.Join(errs...)
}

// removeQuietly deletes a file that should not survive, reporting a failure
// without turning it into the caller's error.
func (s *Service) removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Error("removing a failed backup", slog.String("path", path), slog.Any("error", err))
	}
}
