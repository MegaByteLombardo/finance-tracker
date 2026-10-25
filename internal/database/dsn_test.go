package database

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// The DSN is the entire mechanism by which the connection-scoped PRAGMAs reach
// every pooled connection, so its exact contents are worth pinning down.
func TestDSNCarriesExactlyTheConnectionScopedPragmas(t *testing.T) {
	t.Parallel()

	abs, err := filepath.Abs(filepath.Join("testdata", "finance.db"))
	if err != nil {
		t.Fatalf("resolving a path: %v", err)
	}

	got := dsn(abs)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("dsn produced an unparseable URI %q: %v", got, err)
	}
	if u.Scheme != "file" {
		t.Errorf("scheme = %q, want %q (DSN: %s)", u.Scheme, "file", got)
	}
	if !strings.HasPrefix(got, "file:///") {
		t.Errorf("DSN %q does not use the file:/// form SQLite documents for both Unix and Windows", got)
	}

	want := map[string]string{
		"_busy_timeout": "5000",
		"_foreign_keys": "1",
		"_synchronous":  "FULL",
		"_txlock":       "immediate",
	}
	query := u.Query()
	if len(query) != len(want) {
		t.Errorf("DSN carries %d parameters, want %d: %v", len(query), len(want), query)
	}
	for key, wantValue := range want {
		if got := query.Get(key); got != wantValue {
			t.Errorf("%s = %q, want %q", key, got, wantValue)
		}
	}

	// journal_mode is a persistent property of the file and is applied once in
	// Open, not per connection.
	if query.Has("_journal_mode") || query.Has("_journal") {
		t.Error("DSN sets journal_mode; it belongs in enableWAL, where the result is checked")
	}
}

// Paths with characters that are significant in a URI must survive the round
// trip -- otherwise a database under, say, "My Documents" would silently open
// the wrong file or fail.
func TestDSNEscapesAwkwardPaths(t *testing.T) {
	t.Parallel()

	abs, err := filepath.Abs(filepath.Join("some dir", "finance & co.db"))
	if err != nil {
		t.Fatalf("resolving a path: %v", err)
	}

	u, err := url.Parse(dsn(abs))
	if err != nil {
		t.Fatalf("dsn produced an unparseable URI: %v", err)
	}

	wantPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(wantPath, "/") {
		wantPath = "/" + wantPath
	}
	if u.Path != wantPath {
		t.Errorf("decoded path = %q, want %q", u.Path, wantPath)
	}
	if strings.Contains(u.RawPath+u.EscapedPath(), " ") {
		t.Errorf("escaped path still contains a raw space: %q", u.EscapedPath())
	}
}
