package migrations_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

const insertRate = `
	INSERT INTO exchange_rates
	    (base_currency_code, quote_currency_code, effective_date, rate, source, is_active, fetched_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)`

// fetchedNow is a well-formed timestamp for the rows that need one.
const fetchedNow = "2026-09-11T14:30:00.000Z"

// TestExchangeRateAcceptsWhatASourcePublishes covers the shapes that must get
// in, including the exact precision the BCV publishes at.
func TestExchangeRateAcceptsWhatASourcePublishes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		base   string
		quote  string
		date   string
		rate   string
		source string
	}{
		{"the BCV's USD rate at its published precision", "USD", "VES", "2026-09-11", "842.20670000", "BCV_OFFICIAL"},
		{"the BCV's EUR rate", "EUR", "VES", "2026-09-11", "977.87777730", "BCV_OFFICIAL"},
		{"a whole number", "USD", "VES", "2026-09-10", "500", "MANUAL"},
		{"the smallest value the scale allows", "EUR", "USD", "2026-09-10", "0.000000000001", "MANUAL"},
		{"a rate below one", "VES", "USD", "2026-09-10", "0.00118735", "MANUAL"},
		{"trailing zeros, which record the published precision", "USD", "EUR", "2026-09-10", "1.10000000", "MANUAL"},
		{"a leap day", "USD", "VES", "2024-02-29", "36.50", "MANUAL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)

			var fetchedAt any
			if tc.source != "MANUAL" {
				fetchedAt = fetchedNow
			}

			if _, err := db.ExecContext(t.Context(), insertRate,
				tc.base, tc.quote, tc.date, tc.rate, tc.source, 1, fetchedAt); err != nil {
				t.Fatalf("inserting %s %s/%s = %s: %v", tc.source, tc.base, tc.quote, tc.rate, err)
			}

			// The stored text is byte-for-byte what was written: no coercion, no
			// normalisation, no rounding. This is the whole reason rate is TEXT.
			var stored string
			if err := db.QueryRowContext(t.Context(),
				"SELECT rate FROM exchange_rates").Scan(&stored); err != nil {
				t.Fatalf("reading the rate back: %v", err)
			}
			if stored != tc.rate {
				t.Errorf("stored rate = %q, want %q", stored, tc.rate)
			}
		})
	}
}

// TestExchangeRateConstraints is the list of rows the database itself must
// refuse, so that a hand-written INSERT cannot get past them either.
func TestExchangeRateConstraints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		base   string
		quote  string
		date   string
		rate   string
		source string
		active int
	}{
		// A rate of zero is not a rate. This is the case the CHECK has to catch
		// WITHOUT casting to REAL, which would reintroduce the float this
		// application refuses to have anywhere near a monetary value.
		{"a zero rate", "USD", "VES", "2026-09-11", "0", "MANUAL", 1},
		{"a zero rate with decimals", "USD", "VES", "2026-09-11", "0.00", "MANUAL", 1},
		{"a long zero", "USD", "VES", "2026-09-11", "0000.00000000", "MANUAL", 1},

		// A sign has no place in a rate, and a leading point is not canonical.
		{"a negative rate", "USD", "VES", "2026-09-11", "-500", "MANUAL", 1},
		{"a leading decimal point", "USD", "VES", "2026-09-11", ".5", "MANUAL", 1},
		{"a leading plus", "USD", "VES", "2026-09-11", "+500", "MANUAL", 1},

		// Anything that is not digits and at most one point.
		{"two decimal points", "USD", "VES", "2026-09-11", "1.2.3", "MANUAL", 1},
		{"letters", "USD", "VES", "2026-09-11", "abc", "MANUAL", 1},
		{"exponent notation", "USD", "VES", "2026-09-11", "5e2", "MANUAL", 1},
		{"a comma decimal separator", "USD", "VES", "2026-09-11", "842,20670000", "MANUAL", 1},
		{"a thousands separator", "USD", "VES", "2026-09-11", "1,000.50", "MANUAL", 1},
		{"surrounding whitespace", "USD", "VES", "2026-09-11", " 500 ", "MANUAL", 1},
		{"an empty rate", "USD", "VES", "2026-09-11", "", "MANUAL", 1},
		{"an absurdly long rate", "USD", "VES", "2026-09-11", strings.Repeat("9", 60), "MANUAL", 1},

		// A currency is worth exactly one of itself; that is arithmetic, not
		// data, and storing it would create a row that could be edited into
		// being false.
		{"a self-referencing pair", "USD", "USD", "2026-09-11", "1", "MANUAL", 1},

		// The date is an economic calendar date, with the same two rules every
		// other date column in this schema has.
		{"an unpadded date", "USD", "VES", "2026-9-11", "500", "MANUAL", 1},
		{"a date that does not exist", "USD", "VES", "2026-02-31", "500", "MANUAL", 1},
		{"a date that is not a date", "USD", "VES", "not-a-date", "500", "MANUAL", 1},
		{"a timestamp instead of a date", "USD", "VES", "2026-09-11T00:00:00.000Z", "500", "MANUAL", 1},
		{"an empty date", "USD", "VES", "", "500", "MANUAL", 1},

		// Provenance is a whitelist. A source this binary does not implement
		// must not be storable, because valuation would have no precedence rule
		// for it.
		{"an unknown source", "USD", "VES", "2026-09-11", "500", "SOMEWHERE", 1},
		{"a mirror that was deliberately left out of M5", "USD", "VES", "2026-09-11", "500", "BCV_MIRROR", 1},
		{"a lower-case source", "USD", "VES", "2026-09-11", "500", "manual", 1},
		{"an empty source", "USD", "VES", "2026-09-11", "500", "", 1},

		{"an is_active outside 0 and 1", "USD", "VES", "2026-09-11", "500", "MANUAL", 2},

		// The foreign keys.
		{"an unknown base currency", "XXX", "VES", "2026-09-11", "500", "MANUAL", 1},
		{"an unknown quote currency", "USD", "XXX", "2026-09-11", "500", "MANUAL", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)

			var fetchedAt any
			if tc.source != "MANUAL" {
				fetchedAt = fetchedNow
			}

			_, err := db.ExecContext(t.Context(), insertRate,
				tc.base, tc.quote, tc.date, tc.rate, tc.source, tc.active, fetchedAt)
			if err == nil {
				t.Errorf("the database accepted %s %s/%s = %q on %q",
					tc.source, tc.base, tc.quote, tc.rate, tc.date)
			}
		})
	}
}

// TestExchangeRateFetchedAtBelongsToAFetch covers all four combinations of the
// table CHECK, which is written with IS NULL / IS NOT NULL on both sides so it
// can never evaluate to NULL and silently pass (TD-025).
func TestExchangeRateFetchedAtBelongsToAFetch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		fetchedAt  any
		wantAccept bool
	}{
		{"a manual rate with no fetched_at", "MANUAL", nil, true},
		{"a provider rate with a fetched_at", "BCV_OFFICIAL", fetchedNow, true},
		{"a manual rate claiming to have been fetched", "MANUAL", fetchedNow, false},
		{"a provider rate with no fetched_at", "BCV_OFFICIAL", nil, false},
		{"a provider rate with a malformed fetched_at", "BCV_OFFICIAL", "2026-09-11", false},
		{"a provider rate with a fetched_at missing milliseconds", "BCV_OFFICIAL", "2026-09-11T14:30:00Z", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)

			_, err := db.ExecContext(t.Context(), insertRate,
				"USD", "VES", "2026-09-11", "500", tc.source, 1, tc.fetchedAt)

			switch {
			case tc.wantAccept && err != nil:
				t.Errorf("the database refused %s: %v", tc.name, err)
			case !tc.wantAccept && err == nil:
				t.Errorf("the database accepted %s", tc.name)
			}
		})
	}
}

// TestExchangeRateObservationIsUniquePerSource states the natural identity of an
// observation: one source, one pair, one economic day.
//
// Two sources disagreeing about the same day is not a conflict, it is the
// situation precedence exists for, so both rows must be storable.
func TestExchangeRateObservationIsUniquePerSource(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, insertRate,
		"USD", "VES", "2026-09-11", "842.20670000", "BCV_OFFICIAL", 1, fetchedNow); err != nil {
		t.Fatalf("inserting the official rate: %v", err)
	}

	// The same source, pair and day again: a second observation of one fact.
	if _, err := db.ExecContext(ctx, insertRate,
		"USD", "VES", "2026-09-11", "999.00000000", "BCV_OFFICIAL", 1, fetchedNow); err == nil {
		t.Error("the database accepted two BCV_OFFICIAL rows for the same pair and day")
	}

	// A different source for the same day: two facts, both kept.
	if _, err := db.ExecContext(ctx, insertRate,
		"USD", "VES", "2026-09-11", "500.00", "MANUAL", 1, nil); err != nil {
		t.Errorf("the database refused a manual rate alongside the official one: %v", err)
	}

	// A different day, same source.
	if _, err := db.ExecContext(ctx, insertRate,
		"USD", "VES", "2026-09-10", "840.00000000", "BCV_OFFICIAL", 1, fetchedNow); err != nil {
		t.Errorf("the database refused the previous day's official rate: %v", err)
	}

	// The reversed pair is a different observation, not a duplicate. Nothing in
	// this application stores both directions, but the schema must not be what
	// prevents it -- that is a decision of the service, recorded in the
	// contract.
	if _, err := db.ExecContext(ctx, insertRate,
		"VES", "USD", "2026-09-11", "0.00118735", "BCV_OFFICIAL", 1, fetchedNow); err != nil {
		t.Errorf("the database refused the reversed pair: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM exchange_rates").Scan(&count); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if count != 4 {
		t.Errorf("%d rates were stored, want 4", count)
	}
}

// TestCurrencyWithRatesCannotBeDeletedOrRenamed is the RESTRICT rule applied to
// the new foreign keys, on both sides of the pair.
func TestCurrencyWithRatesCannotBeDeletedOrRenamed(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, insertRate,
		"USD", "VES", "2026-09-11", "842.20670000", "BCV_OFFICIAL", 1, fetchedNow); err != nil {
		t.Fatalf("inserting the rate: %v", err)
	}

	for _, code := range []string{"USD", "VES"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM currencies WHERE code = ?", code); err == nil {
			t.Errorf("%s was deleted while a rate referenced it", code)
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE currencies SET code = 'ZZZ' WHERE code = ?", code); err == nil {
			t.Errorf("%s was renamed while a rate referenced it", code)
		}
	}
}

// TestAppSettingsIsASingleton is the constraint that makes "the settings" a
// meaningful phrase: with two rows, every reader would have to pick one.
func TestAppSettingsIsASingleton(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	var count int
	var base string
	if err := db.QueryRowContext(ctx,
		"SELECT count(*), max(base_currency_code) FROM app_settings").Scan(&count, &base); err != nil {
		t.Fatalf("reading app_settings: %v", err)
	}
	if count != 1 {
		t.Fatalf("a fresh database has %d settings rows, want exactly 1", count)
	}
	if base != "USD" {
		t.Errorf("the seeded valuation base is %q, want USD", base)
	}

	if _, err := db.ExecContext(ctx,
		"INSERT INTO app_settings (id, base_currency_code) VALUES (2, 'EUR')"); err == nil {
		t.Error("a second settings row was accepted")
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO app_settings (id, base_currency_code) VALUES (1, 'EUR')"); err == nil {
		t.Error("a duplicate settings row was accepted")
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE app_settings SET id = 2 WHERE id = 1"); err == nil {
		t.Error("the settings row was renumbered away from 1")
	}
}

func TestAppSettingsBaseCurrencyMustExist(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx,
		"UPDATE app_settings SET base_currency_code = 'XXX' WHERE id = 1"); err == nil {
		t.Error("the valuation base was set to a currency that does not exist")
	}

	// An archived currency is still a currency: archiving one must not break a
	// database that already values in it. The rule "only an active currency may
	// be CHOSEN" belongs to the service, not to the foreign key.
	if _, err := db.ExecContext(ctx,
		"UPDATE currencies SET is_active = 0 WHERE code = 'USD'"); err != nil {
		t.Fatalf("archiving USD: %v", err)
	}
	var base string
	if err := db.QueryRowContext(ctx,
		"SELECT base_currency_code FROM app_settings WHERE id = 1").Scan(&base); err != nil {
		t.Fatalf("reading the valuation base: %v", err)
	}
	if base != "USD" {
		t.Errorf("the valuation base changed to %q when USD was archived", base)
	}

	// And the currency it points at cannot be deleted out from under it.
	if _, err := db.ExecContext(ctx, "DELETE FROM currencies WHERE code = 'USD'"); err == nil {
		t.Error("the currency the valuation base points at was deleted")
	}
}

// TestExchangeRateTimestampDefaultsMatchTheRequiredFormat proves the SQL
// DEFAULT produces a value its own CHECK accepts. A default that violated the
// constraint would only fail on the first INSERT that relied on it.
func TestExchangeRateTimestampDefaultsMatchTheRequiredFormat(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO exchange_rates (base_currency_code, quote_currency_code, effective_date, rate, source)
		VALUES ('USD', 'VES', '2026-09-11', '842.20670000', 'MANUAL')`); err != nil {
		t.Fatalf("inserting with defaulted timestamps: %v", err)
	}

	var created, updated string
	var active int
	var fetched sql.NullString
	if err := db.QueryRowContext(ctx,
		"SELECT created_at, updated_at, is_active, fetched_at FROM exchange_rates").
		Scan(&created, &updated, &active, &fetched); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}

	for _, ts := range []string{created, updated} {
		if len(ts) != len("2026-09-11T14:30:00.000Z") || !strings.HasSuffix(ts, "Z") {
			t.Errorf("timestamp %q does not match the stored convention", ts)
		}
	}
	if active != 1 {
		t.Errorf("is_active defaulted to %d, want 1", active)
	}
	if fetched.Valid {
		t.Errorf("fetched_at defaulted to %q, want NULL for a manual rate", fetched.String)
	}
}
