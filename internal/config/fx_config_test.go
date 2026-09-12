package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/config"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
)

// env builds a getenv function from a map, so a test describes an environment
// instead of mutating the one it runs in.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// TestFXDefaults: with nothing set, `go run ./cmd/web` has a working automatic
// source.
func TestFXDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.FXProviderEnabled {
		t.Error("the automatic rate source is off by default")
	}
	if cfg.FXProviderURL != "https://www.bcv.org.ve/" {
		t.Errorf("FX URL = %q, want the BCV's page", cfg.FXProviderURL)
	}
	if cfg.FXTimeout != 15*time.Second {
		t.Errorf("FX timeout = %v, want 15s", cfg.FXTimeout)
	}
	if cfg.FXRefreshInterval != 4*time.Hour {
		t.Errorf("FX refresh interval = %v, want 4h", cfg.FXRefreshInterval)
	}
}

// TestTheDefaultURLMatchesTheProvider is the counterpart to the duplication in
// config: the constant is written twice so that config depends on nothing, and
// this is what stops the two copies drifting apart.
//
// It is the same arrangement money and currency use for the minor-unit bounds.
func TestTheDefaultURLMatchesTheProvider(t *testing.T) {
	t.Parallel()

	if config.DefaultFXProviderURL != fx.DefaultBCVURL {
		t.Errorf("config says %q and fx says %q; the two copies have drifted",
			config.DefaultFXProviderURL, fx.DefaultBCVURL)
	}

	// And the host the TLS workaround is scoped to is the host that URL names,
	// because otherwise the default configuration could not connect at all.
	if !strings.Contains(config.DefaultFXProviderURL, fx.BCVOfficialHost) {
		t.Errorf("the default URL %q does not name the official host %q",
			config.DefaultFXProviderURL, fx.BCVOfficialHost)
	}
}

func TestFXSettingsAreRead(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"FX_BCV_ENABLED":   "false",
		"FX_BCV_URL":       "http://localhost:9999/rates",
		"FX_BCV_TIMEOUT":   "30s",
		"FX_REFRESH_EVERY": "90m",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.FXProviderEnabled {
		t.Error("FX_BCV_ENABLED=false was not honoured")
	}
	if cfg.FXProviderURL != "http://localhost:9999/rates" {
		t.Errorf("FX URL = %q", cfg.FXProviderURL)
	}
	if cfg.FXTimeout != 30*time.Second {
		t.Errorf("FX timeout = %v, want 30s", cfg.FXTimeout)
	}
	if cfg.FXRefreshInterval != 90*time.Minute {
		t.Errorf("FX refresh interval = %v, want 90m", cfg.FXRefreshInterval)
	}
}

func TestFXSettingsAreValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"a non-boolean switch", map[string]string{"FX_BCV_ENABLED": "maybe"}, "FX_BCV_ENABLED"},
		{"a URL with no scheme", map[string]string{"FX_BCV_URL": "www.bcv.org.ve"}, "FX_BCV_URL"},
		{"a URL with no host", map[string]string{"FX_BCV_URL": "https://"}, "FX_BCV_URL"},
		{"a file URL", map[string]string{"FX_BCV_URL": "file:///etc/passwd"}, "FX_BCV_URL"},
		{"a timeout that is not a duration", map[string]string{"FX_BCV_TIMEOUT": "30"}, "FX_BCV_TIMEOUT"},
		{"a zero timeout, which would mean no timeout at all",
			map[string]string{"FX_BCV_TIMEOUT": "0s"}, "FX_BCV_TIMEOUT"},
		{"a negative timeout", map[string]string{"FX_BCV_TIMEOUT": "-5s"}, "FX_BCV_TIMEOUT"},
		{"an interval that is not a duration", map[string]string{"FX_REFRESH_EVERY": "daily"}, "FX_REFRESH_EVERY"},
		{"a zero interval, which would panic a ticker",
			map[string]string{"FX_REFRESH_EVERY": "0"}, "FX_REFRESH_EVERY"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(env(tc.env))
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

// TestEveryFXMistakeIsReportedAtOnce keeps the package's promise: a
// misconfigured deployment reveals all of its mistakes in one startup attempt.
func TestEveryFXMistakeIsReportedAtOnce(t *testing.T) {
	t.Parallel()

	_, err := config.Load(env(map[string]string{
		"FX_BCV_ENABLED":   "perhaps",
		"FX_BCV_URL":       "not a url at all",
		"FX_BCV_TIMEOUT":   "soon",
		"FX_REFRESH_EVERY": "-1h",
	}))
	if err == nil {
		t.Fatal("Load succeeded, want errors")
	}

	for _, key := range []string{"FX_BCV_ENABLED", "FX_BCV_URL", "FX_BCV_TIMEOUT", "FX_REFRESH_EVERY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the combined error does not mention %s:\n%s", key, err)
		}
	}
}
