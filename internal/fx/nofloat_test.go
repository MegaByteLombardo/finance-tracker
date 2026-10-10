package fx_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoFloatInTheMoneyPath is the project's oldest rule, enforced rather than
// remembered.
//
// Binary floating point cannot represent 842.20670000, or 0.1, or a third of
// anything. The error per operation is tiny and compounds silently across
// conversion and summation, which is exactly what a valuation does: a net worth
// off by a cent cannot be reconciled and gives no signal, it just quietly
// disagrees.
//
// M5 is where that rule is most tempting to break. An exchange rate looks like a
// float, every scraper on the internet parses one into a float64, and the single
// call that would do it here is easy to write and impossible to see in a code
// review of a large diff. So this test reads the source.
//
// It parses rather than greps, so that the word appearing in a comment -- as it
// does, repeatedly, explaining why it is absent -- does not fail the build.
func TestNoFloatInTheMoneyPath(t *testing.T) {
	t.Parallel()

	// Every package that touches a rate, an amount, or a conversion between
	// them.
	packages := []string{".", "../money", "../valuation", "../settings", "../ledger", "../analytics"}

	for _, pkg := range packages {
		t.Run(strings.TrimPrefix(pkg, "../"), func(t *testing.T) {
			t.Parallel()

			entries, err := os.ReadDir(pkg)
			if err != nil {
				t.Fatalf("reading %s: %v", pkg, err)
			}

			fset := token.NewFileSet()
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || !strings.HasSuffix(name, ".go") {
					continue
				}
				// Test files may legitimately mention a float to prove something
				// about one; the shipped code may not.
				if strings.HasSuffix(name, "_test.go") {
					continue
				}

				path := filepath.Join(pkg, name)
				file, perr := parser.ParseFile(fset, path, nil, 0) // 0: comments discarded
				if perr != nil {
					t.Fatalf("parsing %s: %v", path, perr)
				}

				ast.Inspect(file, func(node ast.Node) bool {
					switch n := node.(type) {
					case *ast.Ident:
						if n.Name == "float32" || n.Name == "float64" {
							t.Errorf("%s uses %s; money and exchange rates are exact in this application, "+
								"see internal/money", fset.Position(n.Pos()), n.Name)
						}
					case *ast.BasicLit:
						// A floating-point literal is the other way in: 1.5
						// written where a decimal was meant.
						if n.Kind == token.FLOAT || n.Kind == token.IMAG {
							t.Errorf("%s has the floating-point literal %s; use money.ParseDecimal",
								fset.Position(n.Pos()), n.Value)
						}
					}
					return true
				})
			}
		})
	}
}

// TestNoRealColumnInTheSchema is the same rule one layer down, stated against
// the migration files rather than the database, so it fails at the moment the
// SQL is written.
//
// internal/migrations already asserts that no column in a live database has REAL
// affinity. This catches the same mistake in review, and catches it in a
// migration that has not been run yet.
func TestNoRealColumnInTheSchema(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("../migrations")
	if err != nil {
		t.Fatalf("reading the migrations directory: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		content, rerr := os.ReadFile(filepath.Join("../migrations", entry.Name()))
		if rerr != nil {
			t.Fatalf("reading %s: %v", entry.Name(), rerr)
		}

		for i, line := range strings.Split(string(content), "\n") {
			code, _, _ := strings.Cut(line, "--")
			upper := strings.ToUpper(code)

			// " REAL" as a column type, and any CAST to it -- the shortcut that
			// would make a TEXT rate numerically comparable by throwing away its
			// exactness.
			if strings.Contains(upper, " REAL") || strings.Contains(upper, "AS REAL") {
				t.Errorf("%s:%d uses REAL: %s", entry.Name(), i+1, strings.TrimSpace(line))
			}
			if strings.Contains(upper, "CAST(") && strings.Contains(upper, "REAL") {
				t.Errorf("%s:%d casts to REAL: %s", entry.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
}
