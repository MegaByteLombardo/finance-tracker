package fx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// observation builds an in-memory rate. The Book never touches the database, so
// these tests state the rules without one; service_test.go proves the same
// rules hold end to end against SQLite.
func observation(t *testing.T, id int64, base, quote, on, value string, source fx.Source) fx.Rate {
	t.Helper()
	day, err := database.ParseDate(on)
	if err != nil {
		t.Fatalf("parsing %q: %v", on, err)
	}
	return fx.Rate{
		ID:                id,
		BaseCurrencyCode:  base,
		QuoteCurrencyCode: quote,
		EffectiveDate:     day,
		Value:             money.MustParseDecimal(value),
		Source:            source,
		IsActive:          true,
	}
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := database.ParseDate(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return parsed
}

func TestBookNeverReachesForward(t *testing.T) {
	t.Parallel()

	book := fx.NewBook([]fx.Rate{
		observation(t, 1, "USD", "VES", "2026-09-11", "500", fx.SourceBCVOfficial),
		observation(t, 2, "USD", "VES", "2026-09-15", "600", fx.SourceBCVOfficial),
	})

	tests := []struct {
		on   string
		want string // the rate as published, or "" for unavailable
	}{
		{"2026-09-10", ""},    // before any observation: a gap, not the first rate
		{"2026-09-11", "500"}, // the day itself
		{"2026-09-13", "500"}, // a Sunday: Friday's rate holds
		{"2026-09-14", "500"}, // the 15th has not taken effect
		{"2026-09-15", "600"},
		{"2027-01-01", "600"}, // no maximum age (TD-066)
	}
	for _, tc := range tests {
		resolution, err := book.Resolve("USD", "VES", day(t, tc.on))
		if tc.want == "" {
			if !errors.Is(err, fx.ErrRateUnavailable) {
				t.Errorf("%s: err = %v, want ErrRateUnavailable", tc.on, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.on, err)
		}
		if got := resolution.Rate().RatString(); got != tc.want {
			t.Errorf("%s: rate = %s, want %s", tc.on, got, tc.want)
		}
	}
}

func TestBookPrecedenceIsScopedToOneDate(t *testing.T) {
	t.Parallel()

	book := fx.NewBook([]fx.Rate{
		observation(t, 1, "USD", "VES", "2026-09-11", "500", fx.SourceBCVOfficial),
		observation(t, 2, "USD", "VES", "2026-09-11", "400", fx.SourceManual),
		observation(t, 3, "USD", "VES", "2026-09-14", "520", fx.SourceBCVOfficial),
	})

	// Same day: the manual rate wins.
	resolution, err := book.Resolve("USD", "VES", day(t, "2026-09-12"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolution.Rate().RatString(); got != "400" || !resolution.UsesManualOverride() {
		t.Errorf("2026-09-12: rate = %s (manual %v), want the manual 400",
			got, resolution.UsesManualOverride())
	}

	// A later publication: the manual rate does not freeze anything.
	resolution, err = book.Resolve("USD", "VES", day(t, "2026-09-14"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolution.Rate().RatString(); got != "520" {
		t.Errorf("2026-09-14: rate = %s, want the later publication, 520", got)
	}
}

func TestBookIgnoresArchivedObservations(t *testing.T) {
	t.Parallel()

	archived := observation(t, 2, "USD", "VES", "2026-09-11", "400", fx.SourceManual)
	archived.IsActive = false

	book := fx.NewBook([]fx.Rate{
		observation(t, 1, "USD", "VES", "2026-09-11", "500", fx.SourceBCVOfficial),
		archived,
	})
	resolution, err := book.Resolve("USD", "VES", day(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolution.Rate().RatString(); got != "500" {
		t.Errorf("rate = %s, want the official 500: an archived override must not apply", got)
	}
}

func TestBookInvertsAndCrossesExactly(t *testing.T) {
	t.Parallel()

	book := fx.NewBook([]fx.Rate{
		observation(t, 1, "USD", "VES", "2026-09-11", "500", fx.SourceBCVOfficial),
		observation(t, 2, "EUR", "VES", "2026-09-10", "550", fx.SourceBCVOfficial),
	})
	on := day(t, "2026-09-11")

	inverted, err := book.Resolve("VES", "USD", on)
	if err != nil {
		t.Fatalf("VES->USD: %v", err)
	}
	if inverted.Method != fx.MethodInverted || inverted.Rate().RatString() != "1/500" {
		t.Errorf("VES->USD = %s by %s, want 1/500 inverted", inverted.Rate().RatString(), inverted.Method)
	}

	cross, err := book.Resolve("EUR", "USD", on)
	if err != nil {
		t.Fatalf("EUR->USD: %v", err)
	}
	if cross.Method != fx.MethodCross || cross.Rate().RatString() != "11/10" {
		t.Errorf("EUR->USD = %s by %s, want 11/10 as a cross", cross.Rate().RatString(), cross.Method)
	}
	// A chain is only as current as its stalest link.
	if got := database.FormatDate(cross.EffectiveDate()); got != "2026-09-10" {
		t.Errorf("cross effective date = %s, want the older leg, 2026-09-10", got)
	}

	identity, err := book.Resolve("USD", "USD", on)
	if err != nil || identity.Method != fx.MethodIdentity || identity.Rate().RatString() != "1" {
		t.Errorf("USD->USD = %v, %v; want identity 1", identity, err)
	}

	// The cross legs must both exist on the date: the EUR leg alone is not a
	// path to anything on the 9th.
	if _, err := book.Resolve("EUR", "USD", day(t, "2026-09-09")); !errors.Is(err, fx.ErrRateUnavailable) {
		t.Errorf("EUR->USD before either leg: err = %v, want ErrRateUnavailable", err)
	}
}

func TestBookReportsTheDaysObservationsTakeEffect(t *testing.T) {
	t.Parallel()

	book := fx.NewBook([]fx.Rate{
		observation(t, 1, "USD", "VES", "2026-09-11", "500", fx.SourceBCVOfficial),
	})
	if !book.HasObservationsOn(day(t, "2026-09-11")) {
		t.Error("2026-09-11 has an observation and the book says it does not")
	}
	if book.HasObservationsOn(day(t, "2026-09-12")) {
		t.Error("2026-09-12 has no observation and the book says it does")
	}
}
