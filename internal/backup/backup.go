// Package backup takes and restores snapshots of the operational database.
//
// It exists because M2 shipped a working ledger whose only copy lived in a
// single Docker volume, and that volume was destroyed. Persistence was working
// correctly; there was simply nothing else to fall back to.
//
// Snapshots are taken with SQLite's VACUUM INTO, which produces a consistent,
// compacted copy of a live database without stopping the service and without
// depending on the -wal or -shm files. Copying those three files by hand is a
// common suggestion and it is wrong here: -shm holds no persistent state, and a
// hand-copied set taken mid-write can be internally inconsistent.
//
// This is not a disaster-recovery platform. It takes snapshots, lists them,
// validates them, prunes the ones that accumulate on their own, and puts one
// back.
package backup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Errors this package returns. Callers distinguish them with errors.Is.
var (
	// ErrNotFound means no backup with that name exists in the directory.
	ErrNotFound = errors.New("backup not found")
	// ErrInvalidName means the name is not a plain backup filename. It is
	// returned for anything carrying a path, which is what keeps a caller from
	// reaching outside the backup directory.
	ErrInvalidName = errors.New("invalid backup name")
	// ErrInvalidBackup means the file exists but is not a usable snapshot:
	// not SQLite, corrupt, or not a database this application wrote.
	ErrInvalidBackup = errors.New("invalid backup")
)

// Reason records why a snapshot was taken. It is part of the filename because
// it is what makes a retention policy possible that never deletes something a
// person asked for.
type Reason string

const (
	// Manual is a snapshot the user asked for.
	Manual Reason = "manual"
	// Shutdown is taken automatically as the process stops. These are the only
	// ones subject to retention.
	Shutdown Reason = "shutdown"
	// PreMigration is taken before applying migrations to a database that
	// already has a schema. It is the only thing standing between a bad
	// migration and lost data, so it is never pruned.
	PreMigration Reason = "premigration"
	// PreRestore is taken of the database a restore is about to replace, which
	// is what makes a restore reversible.
	PreRestore Reason = "prerestore"
)

// Reasons lists every valid reason.
func Reasons() []Reason { return []Reason{Manual, Shutdown, PreMigration, PreRestore} }

// Valid reports whether r is a known reason.
func (r Reason) Valid() bool {
	for _, known := range Reasons() {
		if r == known {
			return true
		}
	}
	return false
}

// prunable reports whether retention applies to this kind of snapshot.
//
// Only shutdown backups. They are produced by the process lifecycle rather than
// by anyone's intent, and a development day generates dozens of near-identical
// ones. Everything else was deliberate: a manual snapshot is a decision, a
// pre-migration snapshot is rare and irreplaceable, and a pre-restore snapshot
// is the undo for an operation someone just performed.
func (r Reason) prunable() bool { return r == Shutdown }

// Backup is one snapshot on disk.
type Backup struct {
	// Name is the filename, with no directory part.
	Name   string
	Reason Reason

	// CreatedAt comes from the name rather than the filesystem, so it survives
	// a copy that does not preserve mtime.
	CreatedAt time.Time

	SizeBytes int64

	// seq orders snapshots that share a second. It is not exported because it
	// is an artefact of naming rather than something a caller acts on, but the
	// listing needs it: without it, ordering falls back to comparing names, and
	// "…-manual.db" sorts after "…-manual-2.db" even though it is older.
	seq int
}

// newer reports whether b was taken after other.
func (b Backup) newer(other Backup) bool {
	if !b.CreatedAt.Equal(other.CreatedAt) {
		return b.CreatedAt.After(other.CreatedAt)
	}
	return b.seq > other.seq
}

const (
	namePrefix = "finance-"
	nameSuffix = ".db"

	// partialSuffix marks a snapshot that is still being written or has not
	// been validated yet. A file under this name is never a backup, which is
	// what stops a half-written or corrupt file from ever looking recoverable.
	partialSuffix = ".partial"

	// timestampLayout has no colons: the primary development host is Windows,
	// where a colon cannot appear in a filename. It is fixed-width, so
	// lexicographic order is chronological here as it is everywhere else in
	// this project.
	timestampLayout = "20060102T150405Z"
)

// formatName builds a snapshot filename.
//
// seq disambiguates snapshots taken within the same second -- a manual backup
// immediately followed by a shutdown, for instance. An incremental suffix is
// used rather than sub-second precision or a UUID: it stays readable, it is
// deterministic, and it only appears in the rare case that needs it.
func formatName(at time.Time, reason Reason, seq int) string {
	name := namePrefix + at.UTC().Format(timestampLayout) + "-" + string(reason)
	if seq > 1 {
		name += "-" + strconv.Itoa(seq)
	}
	return name + nameSuffix
}

// parseName reads a snapshot filename back into its parts.
//
// It is deliberately strict. Beyond describing the file, it is the check that
// decides whether a name refers to something this package wrote, so anything it
// does not recognise is refused rather than guessed at.
func parseName(name string) (Backup, error) {
	if err := checkPlainFilename(name); err != nil {
		return Backup{}, err
	}

	body, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return Backup{}, fmt.Errorf("%w: %q does not start with %q", ErrInvalidName, name, namePrefix)
	}
	body, ok = strings.CutSuffix(body, nameSuffix)
	if !ok {
		return Backup{}, fmt.Errorf("%w: %q does not end with %q", ErrInvalidName, name, nameSuffix)
	}

	stamp, rest, ok := strings.Cut(body, "-")
	if !ok {
		return Backup{}, fmt.Errorf("%w: %q has no reason", ErrInvalidName, name)
	}

	at, err := time.Parse(timestampLayout, stamp)
	if err != nil {
		return Backup{}, fmt.Errorf("%w: %q has no readable timestamp", ErrInvalidName, name)
	}

	// The sequence suffix, when present, follows the reason. Its absence means
	// the first snapshot of that second.
	reason, suffix, hasSeq := strings.Cut(rest, "-")
	seq := 1
	if hasSeq {
		n, err := strconv.Atoi(suffix)
		if err != nil || n < 2 {
			return Backup{}, fmt.Errorf("%w: %q has an unreadable sequence suffix", ErrInvalidName, name)
		}
		seq = n
	}
	if !Reason(reason).Valid() {
		return Backup{}, fmt.Errorf("%w: %q names an unknown reason %q", ErrInvalidName, name, reason)
	}

	return Backup{Name: name, Reason: Reason(reason), CreatedAt: at, seq: seq}, nil
}

// checkPlainFilename refuses anything that is not a bare filename.
//
// This is the path-traversal defence, and it works by only ever accepting a
// name that has nowhere to go -- not by resolving a path and then checking
// where it landed. A prefix comparison against the backup directory would have
// to be right about symlinks, case folding, UNC paths and "..", and being right
// about all of those is harder than refusing to accept a path at all.
//
// Both separators are rejected on every platform on purpose: a backslash is an
// ordinary filename character on Linux, so "C:\evil\x.db" is a single valid
// filename there, and accepting it would mean the same input behaved
// differently depending on where the container ran.
func checkPlainFilename(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: no name given", ErrInvalidName)
	case name == "." || name == "..":
		return fmt.Errorf("%w: %q is a directory reference", ErrInvalidName, name)
	case filepath.IsAbs(name):
		return fmt.Errorf("%w: %q is an absolute path; give the filename only", ErrInvalidName, name)
	case filepath.VolumeName(name) != "":
		return fmt.Errorf("%w: %q names a volume; give the filename only", ErrInvalidName, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%w: %q contains a path separator; give the filename only", ErrInvalidName, name)
	case name != filepath.Base(name):
		return fmt.Errorf("%w: %q is not a plain filename", ErrInvalidName, name)
	}
	return nil
}
