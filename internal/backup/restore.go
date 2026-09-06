package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// RestoreOptions describes one restore.
type RestoreOptions struct {
	// Dir is the backup directory. Name is resolved inside it and nowhere else.
	Dir string

	// Name is a plain backup filename. Anything carrying a path is refused.
	Name string

	// DBPath is the operational database to replace.
	DBPath string

	Logger *slog.Logger
}

// Restore replaces the operational database with a snapshot.
//
// It is an offline operation and must run with the server stopped. Replacing
// the file underneath open connections would leave handlers reading a database
// that no longer exists, which is why there is no way to trigger this from the
// web.
//
// Deliberately, it does NOT run migrations afterwards. Restoring and migrating
// are separate concerns, and keeping them separate is what lets an older
// snapshot be brought forward by the ordinary startup path: restore ends here,
// and the next normal start does database.Open, migrations.Up, serve.
//
// The sequence is ordered so that every failure leaves the machine in a state
// someone can reason about:
//
//	validate the snapshot        -- before anything is touched
//	snapshot what is there now   -- so the restore itself is reversible
//	copy the backup alongside    -- the live file is still untouched
//	move the live file aside     -- with its -wal and -shm, as a group
//	move the copy into place
//	discard what was moved aside
//
// A failure after the live database has been moved aside puts it back.
func Restore(ctx context.Context, opts RestoreOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if err := checkPlainFilename(opts.Name); err != nil {
		return err
	}
	if _, err := parseName(opts.Name); err != nil {
		return err
	}

	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return fmt.Errorf("resolving backup directory %q: %w", opts.Dir, err)
	}
	dbPath, err := filepath.Abs(opts.DBPath)
	if err != nil {
		return fmt.Errorf("resolving database path %q: %w", opts.DBPath, err)
	}

	// The name was already proven to be a bare filename, so joining it cannot
	// escape the directory.
	source := filepath.Join(dir, opts.Name)
	if _, err := os.Stat(source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %q in %s", ErrNotFound, opts.Name, dir)
		}
		return fmt.Errorf("reading backup %q: %w", opts.Name, err)
	}

	// Checked before anything is touched: a restore that fails validation must
	// leave the running system exactly as it was.
	if err := Validate(ctx, source); err != nil {
		return err
	}
	logger.InfoContext(ctx, "backup validated", slog.String("backup", opts.Name))

	if err := snapshotBeforeRestore(ctx, dbPath, dir, logger); err != nil {
		return err
	}

	return replaceDatabase(ctx, source, dbPath, logger)
}

// snapshotBeforeRestore captures the database that is about to be replaced.
//
// This is what makes a restore reversible: someone who restores the wrong
// snapshot has the one they just overwrote sitting in the same directory.
//
// A database that cannot be snapshotted does not block the restore. If the
// operational file is missing there is nothing to preserve, and if it is corrupt
// then being unable to read it is precisely why a restore is happening --
// refusing to proceed would deny the operation to the only person who needs it.
func snapshotBeforeRestore(ctx context.Context, dbPath, dir string, logger *slog.Logger) error {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.InfoContext(ctx, "no existing database to snapshot before restoring",
				slog.String("db_path", dbPath))
			return nil
		}
		return fmt.Errorf("reading the database at %q: %w", dbPath, err)
	}

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		logger.WarnContext(ctx, "the database being replaced could not be opened; restoring without a pre-restore snapshot",
			slog.String("db_path", dbPath), slog.Any("error", err))
		return nil
	}
	defer func() {
		// Closed before the file is touched. Replacing a file that this process
		// still holds open is exactly the situation this operation exists to
		// avoid.
		if cerr := db.Close(); cerr != nil {
			logger.ErrorContext(ctx, "closing the database being replaced", slog.Any("error", cerr))
		}
	}()

	svc, err := NewService(db, dir, 0, logger)
	if err != nil {
		return fmt.Errorf("preparing the pre-restore snapshot: %w", err)
	}

	created, err := svc.Create(ctx, PreRestore)
	if err != nil {
		logger.WarnContext(ctx, "could not snapshot the database being replaced; continuing",
			slog.Any("error", err))
		return nil
	}
	logger.InfoContext(ctx, "pre-restore snapshot taken", slog.String("backup", created.Name))
	return nil
}

// replaceDatabase puts source in place of dbPath.
func replaceDatabase(ctx context.Context, source, dbPath string, logger *slog.Logger) (err error) {
	staging := dbPath + ".restoring"

	// Removed if anything below fails, so a half-finished restore never leaves
	// a file that looks like a database beside the real one.
	defer func() {
		if err != nil {
			if rerr := os.Remove(staging); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				logger.ErrorContext(ctx, "removing the staged database", slog.Any("error", rerr))
			}
		}
	}()

	if err := copyFile(source, staging); err != nil {
		return fmt.Errorf("staging the restored database: %w", err)
	}

	// The live database and its journal files move aside together.
	//
	// They must travel as a group. A -wal belongs to the database it was
	// written for: leaving a stale one beside a freshly restored file is the one
	// combination that can corrupt the result, and deleting it while keeping the
	// old database would strip that database of its most recent transactions.
	stamp := time.Now().UTC().Format(timestampLayout)
	moved, err := moveAside(dbPath, stamp)
	if err != nil {
		return err
	}

	if err := os.Rename(staging, dbPath); err != nil {
		// Put back exactly what was there.
		if rerr := restoreAside(moved); rerr != nil {
			return errors.Join(
				fmt.Errorf("moving the restored database into place: %w", err),
				fmt.Errorf("THE ORIGINAL DATABASE COULD NOT BE PUT BACK; it is beside it with the suffix .previous-%s: %w", stamp, rerr),
			)
		}
		return fmt.Errorf("moving the restored database into place: %w", err)
	}

	for _, path := range moved {
		if rerr := os.Remove(path.to); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			// The restore succeeded; a leftover file is untidy, not a failure.
			logger.WarnContext(ctx, "could not remove the replaced database",
				slog.String("path", path.to), slog.Any("error", rerr))
		}
	}

	logger.InfoContext(ctx, "database restored",
		slog.String("from", filepath.Base(source)),
		slog.String("db_path", dbPath),
	)
	return nil
}

// rename records a file that was moved, so it can be moved back.
type rename struct{ from, to string }

// moveAside renames the database and its journal files out of the way.
func moveAside(dbPath, stamp string) ([]rename, error) {
	var moved []rename
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := dbPath + suffix
		if _, err := os.Stat(from); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %q: %w", from, err)
		}

		to := dbPath + ".previous-" + stamp + suffix
		if err := os.Rename(from, to); err != nil {
			// Undo whatever moved already, so a failure here changes nothing.
			if rerr := restoreAside(moved); rerr != nil {
				return nil, errors.Join(fmt.Errorf("moving %q aside: %w", from, err), rerr)
			}
			return nil, fmt.Errorf("moving %q aside: %w", from, err)
		}
		moved = append(moved, rename{from: from, to: to})
	}
	return moved, nil
}

// restoreAside undoes moveAside.
func restoreAside(moved []rename) error {
	var errs []error
	for _, m := range moved {
		if err := os.Rename(m.to, m.from); err != nil {
			errs = append(errs, fmt.Errorf("restoring %q: %w", m.from, err))
		}
	}
	return errors.Join(errs...)
}

// copyFile writes src to dst, which must not already exist, and flushes it to
// disk before returning.
//
// The fsync matters: a restore that is reported as successful and then lost to a
// power cut would be worse than one that failed, because nobody would go looking
// for it.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %q: %w", src, err)
	}
	defer func() {
		if cerr := in.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing %q: %w", src, cerr))
		}
	}()

	// O_EXCL: never write over something already there.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing %q: %w", dst, cerr))
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %q to %q: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("flushing %q: %w", dst, err)
	}
	return nil
}
