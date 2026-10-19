package migrations

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// tableIntroducedAt records the schema version each business table first
// appeared in.
//
// It lives here because this package owns the schema. It exists so that a
// snapshot can be validated against the version it was actually taken at: a
// backup made before M3 has no payment_methods table and is perfectly good, and
// demanding today's table set would reject exactly the backups a restore is
// most needed for.
//
// goose_db_version is not listed: it is goose's bookkeeping, not a business
// table, and its presence is checked separately.
var tableIntroducedAt = map[string]int64{
	"currencies":        1,
	"accounts":          3,
	"categories":        5,
	"transactions":      6,
	"account_movements": 7,
	"payment_methods":   8,
	"exchange_rates":    11,
	"app_settings":      12,

	"savings_policy_versions":      14,
	"savings_category_rules":       14,
	"savings_income_overrides":     14,
	"savings_suggestions":          15,
	"savings_suggestion_rate_legs": 15,
}

// ExpectedTables returns the business tables a database at the given schema
// version must contain, sorted.
//
// An unknown version is an error rather than an empty list: a file claiming a
// version this binary has never heard of is either from the future or is not
// ours, and neither should be quietly accepted.
func ExpectedTables(version int64) ([]string, error) {
	latest, err := LatestVersion()
	if err != nil {
		return nil, err
	}
	if version < 1 || version > latest {
		return nil, fmt.Errorf("schema version %d is outside the known range 1-%d", version, latest)
	}

	tables := make([]string, 0, len(tableIntroducedAt))
	for table, introduced := range tableIntroducedAt {
		if introduced <= version {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	return tables, nil
}

// LatestVersion is the highest migration version embedded in this package.
//
// It is read from the embedded filenames rather than hard-coded, so it cannot
// drift from the migrations that actually ship in the binary.
func LatestVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations: %w", err)
	}

	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		digits, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			continue
		}
		version, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q has no version prefix: %w", entry.Name(), err)
		}
		if version > latest {
			latest = version
		}
	}

	if latest == 0 {
		return 0, fmt.Errorf("no migrations are embedded")
	}
	return latest, nil
}
