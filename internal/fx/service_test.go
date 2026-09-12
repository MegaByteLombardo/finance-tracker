package fx_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

func newService(t *testing.T, provider fx.RateProvider) (*fx.Service, *sql.DB) {
	t.Helper()

	db := dbtest.New(t)
	return fx.NewService(db, provider, slog.New(slog.DiscardHandler)), db
}

func date(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing the date %q: %v", s, err)
	}
	return parsed
}

// seedOfficial writes an observation as the automatic source would, without
// going through the provider. It uses the package's own repository, so the rows
// are exactly the shape a real fetch produces.
func seedOfficial(t *testing.T, db *sql.DB, base, quote, on, value string) fx.Rate {
	t.Helper()

	fetchedAt := database.Now()
	rate, err := fx.NewRepository(db).Insert(t.Context(), fx.Rate{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		EffectiveDate:     date(t, on),
		Value:             money.MustParseDecimal(value),
		Source:            fx.SourceBCVOfficial,
		IsActive:          true,
		FetchedAt:         &fetchedAt,
		CreatedAt:         fetchedAt,
		UpdatedAt:         fetchedAt,
	})
	if err != nil {
		t.Fatalf("seeding the official %s/%s rate on %s: %v", base, quote, on, err)
	}
	return rate
}

func seedManual(t *testing.T, service *fx.Service, base, quote, on, value string) fx.Rate {
	t.Helper()

	rate, err := service.CreateManual(t.Context(), fx.CreateManualInput{
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		Rate:              value,
		EffectiveDate:     on,
	})
	if err != nil {
		t.Fatalf("seeding the manual %s/%s rate on %s: %v", base, quote, on, err)
	}
	return rate
}

// --- manual entry ---------------------------------------------------------

func TestCreateManualStoresExactlyWhatWasTyped(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)

	created, err := service.CreateManual(t.Context(), fx.CreateManualInput{
		BaseCurrencyCode:  "usd", // lower case, to prove normalisation
		QuoteCurrencyCode: " ves ",
		Rate:              "842.20670000",
		EffectiveDate:     "2026-09-11",
	})
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}

	if created.BaseCurrencyCode != "USD" || created.QuoteCurrencyCode != "VES" {
		t.Errorf("pair = %s/%s, want USD/VES", created.BaseCurrencyCode, created.QuoteCurrencyCode)
	}
	if got := created.Value.String(); got != "842.20670000" {
		t.Errorf("rate = %q, want %q; the scale the user typed is part of the value", got, "842.20670000")
	}
	// The backend decides the source. There is no field on the input through
	// which a submission could claim to be the Banco Central.
	if created.Source != fx.SourceManual {
		t.Errorf("source = %q, want %q", created.Source, fx.SourceManual)
	}
	if created.FetchedAt != nil {
		t.Error("a typed rate was recorded as having been fetched")
	}
	if !created.IsActive {
		t.Error("a new manual rate is archived")
	}
	if got := created.EffectiveDate.Format("2006-01-02"); got != "2026-09-11" {
		t.Errorf("effective date = %s, want 2026-09-11", got)
	}
}

func TestCreateManualRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      fx.CreateManualInput
		wantErr error
	}{
		{"a currency that does not exist",
			fx.CreateManualInput{BaseCurrencyCode: "XXX", QuoteCurrencyCode: "VES", Rate: "1", EffectiveDate: "2026-09-11"},
			fx.ErrCurrencyNotFound},
		{"a quote currency that does not exist",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "ZZZ", Rate: "1", EffectiveDate: "2026-09-11"},
			fx.ErrCurrencyNotFound},
		{"the same currency on both sides",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "USD", Rate: "1", EffectiveDate: "2026-09-11"},
			fx.ErrSameCurrency},
		{"a two-letter code",
			fx.CreateManualInput{BaseCurrencyCode: "US", QuoteCurrencyCode: "VES", Rate: "1", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidInput},
		{"a date that does not exist",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "1", EffectiveDate: "2026-02-31"},
			fx.ErrInvalidInput},
		{"an unpadded date",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "1", EffectiveDate: "2026-9-11"},
			fx.ErrInvalidInput},
		{"no date at all",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "1", EffectiveDate: ""},
			fx.ErrInvalidInput},
		{"a rate of zero",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "0", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
		{"a negative rate",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "-500", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
		{"a comma decimal separator, which only the provider normalises",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "842,2067", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
		{"a grouped number",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "1,000.50", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
		{"not a number",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "n/a", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
		{"more precision than is supported",
			fx.CreateManualInput{BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Rate: "1.0000000000001", EffectiveDate: "2026-09-11"},
			fx.ErrInvalidRate},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service, db := newService(t, nil)

			if _, err := service.CreateManual(t.Context(), tc.in); err == nil {
				t.Fatal("CreateManual succeeded, want an error")
			} else if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}

			// And nothing was written on the way to the refusal.
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&count); err != nil {
				t.Fatalf("counting rates: %v", err)
			}
			if count != 0 {
				t.Errorf("%d rates were stored by a rejected input", count)
			}
		})
	}
}

// TestCreateManualRefusesADuplicate is the difference between the manual path
// and the provider path, stated as a test because it is easy to "fix" into an
// upsert later.
func TestCreateManualRefusesADuplicate(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)
	seedManual(t, service, "USD", "VES", "2026-09-11", "500")

	_, err := service.CreateManual(t.Context(), fx.CreateManualInput{
		BaseCurrencyCode:  "USD",
		QuoteCurrencyCode: "VES",
		Rate:              "600",
		EffectiveDate:     "2026-09-11",
	})
	if !errors.Is(err, fx.ErrConflict) {
		t.Fatalf("error = %v, want it to wrap ErrConflict", err)
	}

	// A person who typed a value that already exists must be told, not silently
	// obeyed: the existing value is untouched.
	rates, err := service.List(t.Context(), fx.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("%d rates stored, want 1", len(rates))
	}
	if got := rates[0].Value.String(); got != "500" {
		t.Errorf("the stored rate is %q, want the original 500", got)
	}
}

// TestCreateManualAcceptsAnArchivedCurrency is a deliberate asymmetry with
// accounts and transactions, which refuse archived things.
//
// An ACTIVE account denominated in an ARCHIVED currency is a reachable state
// this schema documents. Refusing to record the rate that values it would leave
// that money permanently unvaluable, which is a worse outcome than allowing a
// rate for a retired currency.
func TestCreateManualAcceptsAnArchivedCurrency(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)

	if _, err := db.ExecContext(t.Context(),
		"UPDATE currencies SET is_active = 0 WHERE code = 'VES'"); err != nil {
		t.Fatalf("archiving VES: %v", err)
	}

	if _, err := service.CreateManual(t.Context(), fx.CreateManualInput{
		BaseCurrencyCode:  "USD",
		QuoteCurrencyCode: "VES",
		Rate:              "500",
		EffectiveDate:     "2026-09-11",
	}); err != nil {
		t.Fatalf("CreateManual with an archived currency: %v", err)
	}
}

func TestUpdateManualChangesOnlyTheValue(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)
	created := seedManual(t, service, "USD", "VES", "2026-09-11", "500")

	updated, err := service.UpdateManual(t.Context(), created.ID, fx.UpdateManualInput{Rate: "512.75"})
	if err != nil {
		t.Fatalf("UpdateManual: %v", err)
	}

	if got := updated.Value.String(); got != "512.75" {
		t.Errorf("rate = %q, want 512.75", got)
	}
	if !updated.EffectiveDate.Equal(created.EffectiveDate) {
		t.Error("the effective date changed; it is part of the observation's identity")
	}
	if updated.BaseCurrencyCode != created.BaseCurrencyCode ||
		updated.QuoteCurrencyCode != created.QuoteCurrencyCode {
		t.Error("the pair changed; it is part of the observation's identity")
	}
	if updated.Source != fx.SourceManual {
		t.Errorf("source = %q, want it unchanged", updated.Source)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) && !updated.UpdatedAt.Equal(created.UpdatedAt) {
		t.Error("updated_at went backwards")
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Error("created_at changed; when it was first recorded does not change because it was corrected")
	}
}

// TestOfficialRatesAreNotEditable protects the record of what a source actually
// published.
//
// The way to disagree with a publication is a manual observation for the same
// day, which wins by precedence -- not editing the publication into saying
// something it did not.
func TestOfficialRatesAreNotEditable(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)
	official := seedOfficial(t, db, "USD", "VES", "2026-09-11", "842.20670000")

	if _, err := service.UpdateManual(t.Context(), official.ID,
		fx.UpdateManualInput{Rate: "1"}); !errors.Is(err, fx.ErrNotEditable) {
		t.Errorf("UpdateManual error = %v, want ErrNotEditable", err)
	}
	if _, err := service.SetManualActive(t.Context(), official.ID, false); !errors.Is(err, fx.ErrNotEditable) {
		t.Errorf("SetManualActive error = %v, want ErrNotEditable", err)
	}

	stored, err := service.Get(t.Context(), official.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := stored.Value.String(); got != "842.20670000" {
		t.Errorf("the published rate is now %q", got)
	}
	if !stored.IsActive {
		t.Error("the published rate was archived")
	}
}

func TestUpdateAndArchiveRejectAnUnknownID(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)

	if _, err := service.UpdateManual(t.Context(), 999, fx.UpdateManualInput{Rate: "1"}); !errors.Is(err, fx.ErrNotFound) {
		t.Errorf("UpdateManual error = %v, want ErrNotFound", err)
	}
	if _, err := service.SetManualActive(t.Context(), 999, false); !errors.Is(err, fx.ErrNotFound) {
		t.Errorf("SetManualActive error = %v, want ErrNotFound", err)
	}
	if _, err := service.Get(t.Context(), 999); !errors.Is(err, fx.ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
}

// --- lookup ---------------------------------------------------------------

func TestResolveUsesTheLatestRateOnOrBeforeTheDate(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)
	seedOfficial(t, db, "USD", "VES", "2026-09-07", "800")
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "842.20670000")

	tests := []struct {
		name string
		on   string
		want string
	}{
		{"the day of the publication", "2026-09-11", "842.20670000"},
		{"the day after, with nothing newer published", "2026-09-12", "842.20670000"},
		{"a week later", "2026-09-18", "842.20670000"},
		{"the day of the earlier publication", "2026-09-07", "800"},
		{"between the two", "2026-09-09", "800"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, tc.on))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := resolution.Legs[0].Observation.Value.String(); got != tc.want {
				t.Errorf("used %q, want %q", got, tc.want)
			}
		})
	}

	// Before anything was published there is nothing to use. An earlier rate is
	// not invented by extrapolating backwards.
	if _, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-01")); !errors.Is(err, fx.ErrRateUnavailable) {
		t.Errorf("error = %v, want ErrRateUnavailable before the first publication", err)
	}
}

// TestResolveOverAWeekend is the case the <= rule exists for.
//
// The BCV publishes on business days. A Sunday has no publication of its own and
// must not need one: Friday's rate is the rate that is in force.
func TestResolveOverAWeekend(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)

	// 2026-09-11 is a Friday.
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "842.20670000")

	for _, day := range []string{"2026-09-11", "2026-09-12", "2026-09-13", "2026-09-14"} {
		resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, day))
		if err != nil {
			t.Fatalf("Resolve on %s: %v", day, err)
		}
		if got := resolution.Legs[0].Observation.EffectiveDate.Format("2006-01-02"); got != "2026-09-11" {
			t.Errorf("on %s the rate used is dated %s, want Friday 2026-09-11", day, got)
		}
	}

	// And no row was invented for the weekend. Filling the gap physically would
	// confuse a published rate with a carried-forward one.
	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows exist, want 1: the weekend must be covered by the lookup, not by rows", rows)
	}
}

// TestResolveNeverUsesARateThatHasNotTakenEffect is the look-ahead guard, and it
// is not hypothetical.
//
// On 2026-09-11 the BCV's own page carried a Fecha Valor of 2026-09-15: it
// publishes forward. A rate effective next Tuesday says nothing about what today
// is worth, and reaching for it would make every figure this application
// produces about a past date depend on information that did not exist then.
//
// The correct outcome is an explicit gap, not a convenient number.
func TestResolveNeverUsesARateThatHasNotTakenEffect(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)
	seedOfficial(t, db, "USD", "VES", "2026-09-15", "842.20670000")

	for _, day := range []string{"2026-09-11", "2026-09-12", "2026-09-13", "2026-09-14"} {
		if _, err := service.Resolve(t.Context(), "USD", "VES", date(t, day)); !errors.Is(err, fx.ErrRateUnavailable) {
			t.Errorf("on %s the resolver returned %v; a rate effective 2026-09-15 must not be used before then",
				day, err)
		}
	}

	// It becomes usable on its own date and not one day earlier.
	if _, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-15")); err != nil {
		t.Errorf("on its own effective date the rate must apply: %v", err)
	}

	// The same rule governs the inverted and crossed paths, which is why it is
	// enforced in the lookup rather than in one caller.
	if _, err := service.Resolve(t.Context(), "VES", "USD", date(t, "2026-09-12")); !errors.Is(err, fx.ErrRateUnavailable) {
		t.Errorf("inverted resolution error = %v, want ErrRateUnavailable", err)
	}

	seedOfficial(t, db, "EUR", "VES", "2026-09-15", "977.87777730")
	if _, err := service.Resolve(t.Context(), "EUR", "USD", date(t, "2026-09-12")); !errors.Is(err, fx.ErrRateUnavailable) {
		t.Errorf("cross resolution error = %v, want ErrRateUnavailable", err)
	}
}

// TestPrecedenceAppliesWithinADateAndNotAcrossDates is the single most important
// rule in this package, and the one that would be silently wrong if written the
// obvious way.
func TestPrecedenceAppliesWithinADateAndNotAcrossDates(t *testing.T) {
	t.Parallel()

	t.Run("a manual rate wins on the day it names", func(t *testing.T) {
		t.Parallel()

		service, db := newService(t, nil)
		seedOfficial(t, db, "USD", "VES", "2026-09-11", "842.20670000")
		seedManual(t, service, "USD", "VES", "2026-09-11", "500")

		resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-11"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolution.Legs[0].Observation.Source != fx.SourceManual {
			t.Errorf("used the %s rate, want the manual override",
				resolution.Legs[0].Observation.Source)
		}
		if !resolution.UsesManualOverride() {
			t.Error("the resolution does not report that it used a manual override")
		}
	})

	t.Run("an older manual rate does NOT shadow a newer publication", func(t *testing.T) {
		t.Parallel()

		service, db := newService(t, nil)
		seedManual(t, service, "USD", "VES", "2026-09-11", "500")
		seedOfficial(t, db, "USD", "VES", "2026-09-14", "842.20670000")

		resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-15"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolution.Legs[0].Observation.Source != fx.SourceBCVOfficial {
			t.Error("a manual rate from an earlier day shadowed a newer publication; " +
				"precedence must apply within one effective date, never across dates")
		}
		if got := resolution.Legs[0].Observation.Value.String(); got != "842.20670000" {
			t.Errorf("used %q, want the newer published rate", got)
		}
	})

	t.Run("archiving the manual rate restores the published one", func(t *testing.T) {
		t.Parallel()

		service, db := newService(t, nil)
		seedOfficial(t, db, "USD", "VES", "2026-09-11", "842.20670000")
		override := seedManual(t, service, "USD", "VES", "2026-09-11", "500")

		if _, err := service.SetManualActive(t.Context(), override.ID, false); err != nil {
			t.Fatalf("archiving the override: %v", err)
		}

		resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-11"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolution.Legs[0].Observation.Source != fx.SourceBCVOfficial {
			t.Error("an archived override is still being used")
		}

		// Nothing was destroyed. Restoring it brings the override back.
		if _, err := service.SetManualActive(t.Context(), override.ID, true); err != nil {
			t.Fatalf("restoring the override: %v", err)
		}
		resolution, err = service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-11"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolution.Legs[0].Observation.Source != fx.SourceManual {
			t.Error("the restored override is not being used")
		}
	})
}

func TestResolveIdentityNeedsNoRate(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)

	resolution, err := service.Resolve(t.Context(), "USD", "USD", date(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Method != fx.MethodIdentity {
		t.Errorf("method = %q, want %q", resolution.Method, fx.MethodIdentity)
	}
	if len(resolution.Legs) != 0 {
		t.Errorf("identity used %d observations, want none", len(resolution.Legs))
	}
	if got := resolution.Rate().RatString(); got != "1" {
		t.Errorf("identity rate = %s, want 1", got)
	}

	// And no USD/USD row exists or is created.
	var rows int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM exchange_rates WHERE base_currency_code = quote_currency_code").Scan(&rows); err != nil {
		t.Fatalf("counting self-referencing rates: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d self-referencing rates exist, want 0", rows)
	}
}

func TestResolveInvertsExactly(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "500")

	resolution, err := service.Resolve(t.Context(), "VES", "USD", date(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Method != fx.MethodInverted {
		t.Errorf("method = %q, want %q", resolution.Method, fx.MethodInverted)
	}
	if !resolution.Legs[0].Inverted {
		t.Error("the leg is not marked as inverted")
	}
	if got := resolution.Rate().RatString(); got != "1/500" {
		t.Errorf("rate = %s, want exactly 1/500", got)
	}

	// The inverse is computed, never stored: no VES/USD row was created.
	var rows int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM exchange_rates WHERE base_currency_code = 'VES'").Scan(&rows); err != nil {
		t.Fatalf("counting reversed rates: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d reversed rows were stored, want 0", rows)
	}

	// 50,000.00 VES at 1 USD = 500 VES is exactly 100.00 USD.
	converted, err := money.ConvertMinor(5000000, 2, 2, resolution.Rate())
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	if converted != 10000 {
		t.Errorf("50,000.00 VES = %d minor units of USD, want 10000", converted)
	}
}

func TestResolveCrossesThroughAPivot(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)

	// Exactly what the BCV publishes: everything against the bolivar.
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "500")
	seedOfficial(t, db, "EUR", "VES", "2026-09-11", "550")

	resolution, err := service.Resolve(t.Context(), "EUR", "USD", date(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Method != fx.MethodCross {
		t.Errorf("method = %q, want %q", resolution.Method, fx.MethodCross)
	}
	if len(resolution.Legs) != 2 {
		t.Fatalf("the cross used %d legs, want 2", len(resolution.Legs))
	}
	// 550 / 500 = 11/10, exactly.
	if got := resolution.Rate().RatString(); got != "11/10" {
		t.Errorf("EUR/USD = %s, want 11/10", got)
	}

	// Both observations are reported, so a screen can show the derivation.
	pairs := map[string]bool{}
	for _, leg := range resolution.Legs {
		pairs[leg.Observation.Pair()] = true
	}
	if !pairs["EUR/VES"] || !pairs["USD/VES"] {
		t.Errorf("the resolution reports %v, want both EUR/VES and USD/VES", pairs)
	}

	// Nothing derived was persisted.
	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 2 {
		t.Errorf("%d rows exist, want the 2 that were published", rows)
	}
}

// TestCrossPivotChoiceIsDeterministic pins the tie-break, so that the same data
// always produces the same answer and the same explanation.
func TestCrossPivotChoiceIsDeterministic(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)

	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO currencies (code, name, symbol, minor_unit, is_active, created_at, updated_at)
		VALUES ('COP','Peso colombiano','$',2,1,'2026-09-01T00:00:00.000Z','2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seeding COP: %v", err)
	}

	// Two possible pivots between EUR and USD. VES's pair is fresher on both
	// legs, so it must win: a chain is only as current as its stalest link.
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "500")
	seedOfficial(t, db, "EUR", "VES", "2026-09-11", "550")
	seedOfficial(t, db, "USD", "COP", "2026-09-01", "4000")
	seedOfficial(t, db, "EUR", "COP", "2026-09-01", "4400")

	for range 5 {
		resolution, err := service.Resolve(t.Context(), "EUR", "USD", date(t, "2026-09-11"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		for _, leg := range resolution.Legs {
			if leg.Observation.QuoteCurrencyCode != "VES" {
				t.Fatalf("the cross went through %s, want the fresher VES pair",
					leg.Observation.QuoteCurrencyCode)
			}
		}
	}
}

func TestResolveReportsAMissingPair(t *testing.T) {
	t.Parallel()

	service, db := newService(t, nil)
	seedOfficial(t, db, "USD", "VES", "2026-09-11", "500")

	// JPY is in no pair at all, so there is no path to USD.
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO currencies (code, name, symbol, minor_unit, is_active, created_at, updated_at)
		VALUES ('JPY','Yen','¥',0,1,'2026-09-01T00:00:00.000Z','2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seeding JPY: %v", err)
	}

	_, err := service.Resolve(t.Context(), "JPY", "USD", date(t, "2026-09-11"))
	if !errors.Is(err, fx.ErrRateUnavailable) {
		t.Errorf("error = %v, want ErrRateUnavailable", err)
	}
}

// --- provider ingest ------------------------------------------------------

// fakeProvider returns whatever a test hands it, so the ingest rules can be
// exercised without a server.
//
// calls is atomic because a fetch runs on whichever goroutine started it, and
// the overlap test reads the counter from another one while that fetch is still
// in flight. A plain int here is a data race -- one the detector found, in this
// file rather than in the code under test.
type fakeProvider struct {
	publication fx.Publication
	err         error
	calls       atomic.Int64
	block       chan struct{}
}

func (p *fakeProvider) Name() fx.Source { return fx.SourceBCVOfficial }

func (p *fakeProvider) Fetch(ctx context.Context) (fx.Publication, error) {
	p.calls.Add(1)
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return fx.Publication{}, ctx.Err()
		}
	}
	if p.err != nil {
		return fx.Publication{}, p.err
	}
	return p.publication, nil
}

func publication(t *testing.T, on string, rates map[string]string) fx.Publication {
	t.Helper()

	p := fx.Publication{
		Source:        fx.SourceBCVOfficial,
		EffectiveDate: date(t, on),
		FetchedAt:     database.Now(),
	}
	for code, value := range rates {
		p.Observations = append(p.Observations, fx.Observation{
			BaseCurrencyCode:  code,
			QuoteCurrencyCode: "VES",
			Rate:              money.MustParseDecimal(value),
		})
	}
	return p
}

func TestRefreshStoresAPublication(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-15",
		map[string]string{"USD": "842.20670000", "EUR": "977.87777730"})}
	service, _ := newService(t, provider)

	result, err := service.RefreshFromProvider(t.Context())
	if err != nil {
		t.Fatalf("RefreshFromProvider: %v", err)
	}
	if result.Stored != 2 {
		t.Errorf("stored %d rates, want 2", result.Stored)
	}
	if got := result.EffectiveDate.Format("2006-01-02"); got != "2026-09-15" {
		t.Errorf("effective date = %s, want the publication's own 2026-09-15", got)
	}

	for _, rate := range result.Rates {
		if rate.Source != fx.SourceBCVOfficial {
			t.Errorf("%s stored under %q", rate.Pair(), rate.Source)
		}
		if rate.FetchedAt == nil {
			t.Errorf("%s was stored with no fetched_at", rate.Pair())
		}
	}
}

// TestRefreshSkipsCurrenciesThisApplicationDoesNotHave is the rule that stops an
// external document creating entities in this database.
func TestRefreshSkipsCurrenciesThisApplicationDoesNotHave(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-15", map[string]string{
		"USD": "842.20670000",
		"EUR": "977.87777730",
		"CNY": "125.56193812",
		"TRY": "17.32921333",
	})}
	service, db := newService(t, provider)

	result, err := service.RefreshFromProvider(t.Context())
	if err != nil {
		t.Fatalf("RefreshFromProvider: %v", err)
	}
	if result.Stored != 2 {
		t.Errorf("stored %d rates, want the 2 whose currencies exist", result.Stored)
	}
	if len(result.Skipped) != 2 {
		t.Errorf("skipped %v, want CNY and TRY", result.Skipped)
	}

	var currencies int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM currencies").Scan(&currencies); err != nil {
		t.Fatalf("counting currencies: %v", err)
	}
	if currencies != 3 {
		t.Errorf("%d currencies exist, want the 3 that were seeded; scraping must not create entities", currencies)
	}
}

// TestRefreshIsAllOrNothing is the atomicity rule.
//
// A publication is one document with one Fecha Valor. Storing half of one would
// let a later valuation combine this publication's dollar rate with a previous
// publication's euro rate and produce a total that is a blend of two days while
// looking entirely ordinary.
func TestRefreshIsAllOrNothing(t *testing.T) {
	t.Parallel()

	// An observation whose rate is zero: it passes the provider's own checks
	// only if something upstream is broken, and the database refuses it, so the
	// transaction must take the whole publication down with it.
	bad := publication(t, "2026-09-15", map[string]string{"USD": "842.20670000"})
	bad.Observations = append(bad.Observations, fx.Observation{
		BaseCurrencyCode:  "EUR",
		QuoteCurrencyCode: "EUR", // same currency: the table CHECK refuses it
		Rate:              money.MustParseDecimal("1"),
	})

	provider := &fakeProvider{publication: bad}
	service, db := newService(t, provider)

	if _, err := service.RefreshFromProvider(t.Context()); err == nil {
		t.Fatal("RefreshFromProvider succeeded, want a failure")
	}

	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d rates survived a failed publication, want 0", rows)
	}
}

// TestRefreshUpdatesRatherThanDuplicating covers the second run of the day, and
// a corrected publication for the same Fecha Valor.
func TestRefreshUpdatesRatherThanDuplicating(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-15",
		map[string]string{"USD": "842.20670000", "EUR": "977.87777730"})}
	service, db := newService(t, provider)

	if _, err := service.RefreshFromProvider(t.Context()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// The BCV corrects the dollar rate for the same Fecha Valor.
	provider.publication = publication(t, "2026-09-15",
		map[string]string{"USD": "845.00000000", "EUR": "977.87777730"})

	if _, err := service.RefreshFromProvider(t.Context()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 2 {
		t.Errorf("%d rows exist after two fetches of one publication, want 2", rows)
	}

	resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-15"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolution.Legs[0].Observation.Value.String(); got != "845.00000000" {
		t.Errorf("the stored rate is %q, want the correction 845.00000000", got)
	}
}

// TestRefreshDoesNotOverwriteAManualOverride: an upsert is keyed on the SOURCE
// as well as the pair and date, so the two observations coexist.
func TestRefreshDoesNotOverwriteAManualOverride(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-11",
		map[string]string{"USD": "842.20670000", "EUR": "977.87777730"})}
	service, _ := newService(t, provider)

	seedManual(t, service, "USD", "VES", "2026-09-11", "500")

	if _, err := service.RefreshFromProvider(t.Context()); err != nil {
		t.Fatalf("RefreshFromProvider: %v", err)
	}

	resolution, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Legs[0].Observation.Source != fx.SourceManual {
		t.Error("the refresh overwrote the manual override")
	}
	if got := resolution.Legs[0].Observation.Value.String(); got != "500" {
		t.Errorf("the override is now %q, want 500", got)
	}
}

func TestRefreshPropagatesProviderFailures(t *testing.T) {
	t.Parallel()

	for _, wantErr := range []error{fx.ErrProviderUnavailable, fx.ErrProviderInvalidResponse} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			t.Parallel()

			service, db := newService(t, &fakeProvider{err: wantErr})

			if _, err := service.RefreshFromProvider(t.Context()); !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want %v", err, wantErr)
			}

			var rows int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
				t.Fatalf("counting rates: %v", err)
			}
			if rows != 0 {
				t.Errorf("%d rates were stored despite the failure", rows)
			}
		})
	}
}

func TestRefreshWithoutAProviderIsAnError(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)

	if service.HasProvider() {
		t.Error("HasProvider is true with no provider")
	}
	if _, err := service.RefreshFromProvider(t.Context()); !errors.Is(err, fx.ErrProviderUnavailable) {
		t.Errorf("error = %v, want ErrProviderUnavailable", err)
	}
}
