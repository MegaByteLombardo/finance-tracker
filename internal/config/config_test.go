package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/config"
)

// envFunc turns a map into the getenv function Load expects. Missing keys read
// as empty, matching os.Getenv.
func envFunc(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envFunc(nil))
	if err != nil {
		t.Fatalf("Load with an empty environment: unexpected error: %v", err)
	}

	if cfg.Env != config.Development {
		t.Errorf("Env = %q, want %q", cfg.Env, config.Development)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.DBPath != "./data/finance.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "./data/finance.db")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envFunc(map[string]string{
		"APP_ENV":   "production",
		"HTTP_ADDR": "127.0.0.1:9999",
		"DB_PATH":   "/var/lib/finance/finance.db",
		"LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Env != config.Production {
		t.Errorf("Env = %q, want %q", cfg.Env, config.Production)
	}
	if cfg.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "127.0.0.1:9999")
	}
	if cfg.DBPath != "/var/lib/finance/finance.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/var/lib/finance/finance.db")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

// Surrounding whitespace is almost always an artefact of hand-edited .env files
// rather than intent, so it is trimmed rather than rejected.
func TestLoadTrimsWhitespace(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envFunc(map[string]string{
		"APP_ENV":   "  production  ",
		"HTTP_ADDR": "\t:9090\n",
		"LOG_LEVEL": " WARN ",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Env != config.Production {
		t.Errorf("Env = %q, want %q", cfg.Env, config.Production)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9090")
	}
	// slog parses level names case-insensitively.
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelWarn)
	}
}

func TestLoadInvalidValues(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		env      map[string]string
		wantName string // the variable the error must name
	}{
		"unknown environment": {
			env:      map[string]string{"APP_ENV": "staging"},
			wantName: "APP_ENV",
		},
		"blank environment": {
			env:      map[string]string{"APP_ENV": "   "},
			wantName: "APP_ENV",
		},
		"address without port": {
			env:      map[string]string{"HTTP_ADDR": "localhost"},
			wantName: "HTTP_ADDR",
		},
		"blank address": {
			env:      map[string]string{"HTTP_ADDR": " "},
			wantName: "HTTP_ADDR",
		},
		"blank database path": {
			env:      map[string]string{"DB_PATH": "   "},
			wantName: "DB_PATH",
		},
		"unknown log level": {
			env:      map[string]string{"LOG_LEVEL": "verbose"},
			wantName: "LOG_LEVEL",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(envFunc(tc.env))
			if err == nil {
				t.Fatalf("expected an error, got config %+v", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantName) {
				t.Errorf("error %q does not name the offending variable %q", err, tc.wantName)
			}
			if cfg != (config.Config{}) {
				t.Errorf("expected the zero Config on failure, got %+v", cfg)
			}
		})
	}
}

// A deployment with several mistakes should reveal all of them in one startup
// attempt, not one per restart.
func TestLoadReportsEveryError(t *testing.T) {
	t.Parallel()

	_, err := config.Load(envFunc(map[string]string{
		"APP_ENV":   "staging",
		"HTTP_ADDR": "localhost",
		"LOG_LEVEL": "verbose",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, name := range []string{"APP_ENV", "HTTP_ADDR", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("joined error does not mention %q:\n%v", name, err)
		}
	}
}
