// Package config loads and validates the application configuration from the
// process environment.
//
// It deliberately depends on nothing outside the standard library. Docker
// Compose already interpolates a .env file on its own, and every setting has a
// working local default, so a dotenv or configuration library would add a
// dependency without adding capability.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
)

// Environment names the deployment context. It selects the log handler and is
// reported at startup; nothing else branches on it.
type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

// Defaults chosen so that `go run ./cmd/web` works with no environment set.
const (
	DefaultEnv      = Development
	DefaultHTTPAddr = ":8080"
	DefaultDBPath   = "./data/finance.db"
	DefaultLogLevel = slog.LevelInfo

	// DefaultBackupDir sits beside the default database so that a bare
	// `go run ./cmd/web` has somewhere usable to write snapshots. Under Docker
	// it is overridden to /backups, which is a bind mount to the host -- the
	// database's named volume would be destroyed by the same `down -v` that
	// makes backups worth having.
	DefaultBackupDir = "./backups"

	// DefaultBackupRetention is how many SHUTDOWN backups are kept. Only that
	// kind is pruned: they are produced by the process lifecycle rather than by
	// anyone's intent, and in development there are dozens of near-identical
	// ones. Manual, pre-migration and pre-restore snapshots are never pruned.
	//
	// Zero disables pruning entirely.
	DefaultBackupRetention = 10
)

// Config is the fully validated configuration of a single process.
type Config struct {
	Env      Environment
	HTTPAddr string
	DBPath   string
	LogLevel slog.Level

	// BackupDir is where snapshots are written. It must not be inside the
	// directory holding the database: the point of a backup is to survive the
	// destruction of the operational store.
	BackupDir string

	// BackupRetention bounds how many shutdown backups are kept. Zero keeps all.
	BackupRetention int
}

// Load reads the configuration through getenv.
//
// getenv is injected rather than calling os.Getenv directly so that tests can
// describe an environment without mutating the one the test binary runs in.
// Production callers pass os.Getenv.
//
// Every problem is reported at once: errors are collected and joined instead of
// returning on the first failure, so a misconfigured deployment reveals all of
// its mistakes in a single startup attempt rather than one per restart.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Env:             DefaultEnv,
		HTTPAddr:        DefaultHTTPAddr,
		DBPath:          DefaultDBPath,
		LogLevel:        DefaultLogLevel,
		BackupDir:       DefaultBackupDir,
		BackupRetention: DefaultBackupRetention,
	}

	var errs []error

	if raw, set := lookup(getenv, "APP_ENV"); set {
		switch Environment(raw) {
		case Development, Production:
			cfg.Env = Environment(raw)
		default:
			errs = append(errs, fmt.Errorf(
				"APP_ENV=%q: must be %q or %q", raw, Development, Production))
		}
	}

	if raw, set := lookup(getenv, "HTTP_ADDR"); set {
		// SplitHostPort accepts a bare ":8080" (empty host means all
		// interfaces), which is exactly the shape we want to allow.
		if _, _, err := net.SplitHostPort(raw); err != nil {
			errs = append(errs, fmt.Errorf(
				"HTTP_ADDR=%q: not a valid host:port address: %w", raw, err))
		} else {
			cfg.HTTPAddr = raw
		}
	}

	if raw, set := lookup(getenv, "DB_PATH"); set {
		if raw == "" {
			errs = append(errs, errors.New(`DB_PATH: must not be blank`))
		} else {
			cfg.DBPath = raw
		}
	}

	if raw, set := lookup(getenv, "BACKUP_DIR"); set {
		if raw == "" {
			errs = append(errs, errors.New(`BACKUP_DIR: must not be blank`))
		} else {
			cfg.BackupDir = raw
		}
	}

	if raw, set := lookup(getenv, "BACKUP_RETENTION"); set {
		switch n, err := strconv.Atoi(raw); {
		case err != nil:
			errs = append(errs, fmt.Errorf(
				"BACKUP_RETENTION=%q: must be a whole number", raw))
		case n < 0:
			errs = append(errs, fmt.Errorf(
				"BACKUP_RETENTION=%d: must not be negative; use 0 to keep every backup", n))
		default:
			cfg.BackupRetention = n
		}
	}

	if raw, set := lookup(getenv, "LOG_LEVEL"); set {
		var level slog.Level
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf(
				"LOG_LEVEL=%q: must be one of debug, info, warn, error: %w", raw, err))
		} else {
			cfg.LogLevel = level
		}
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// lookup reads key and reports whether the caller provided it at all.
//
// An unset variable and one set to the empty string are indistinguishable
// through getenv, so both mean "use the default". A value made only of
// whitespace is different: it is reported as provided but trims to empty, which
// each caller then rejects as the configuration mistake it is.
func lookup(getenv func(string) string, key string) (value string, provided bool) {
	raw := getenv(key)
	if raw == "" {
		return "", false
	}
	return strings.TrimSpace(raw), true
}
