package money_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

func TestParseExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in        string
		minorUnit int
		want      int64
	}{
		// The cases from the contract, verbatim.
		{"1000", 2, 100000},
		{"1000.50", 2, 100050},
		{"25.99", 2, 2599},
		{"0.05", 2, 5},
		{"-40", 2, -4000},
		{"  12.34  ", 2, 1234},
		{"007.50", 2, 750},
		{"1234", 0, 1234},

		// Fewer decimals than the currency has is a shorthand, not an error.
		{"12.5", 2, 1250},
		{"12", 2, 1200},
		{"12.5", 3, 12500},
		{"0", 2, 0},
		{"0.00", 2, 0},
		{"-0", 2, 0},
		{"-0.01", 2, -1},

		// Scales other than 2.
		{"1234", 3, 1234000},
		{"1.2345", 4, 12345},
		{"0.0001", 4, 1},

		// The exact boundaries of int64, which must be representable rather
		// than merely "large".
		{"92233720368547758.07", 2, math.MaxInt64},
		{"-92233720368547758.08", 2, math.MinInt64},
		{"9223372036854775807", 0, math.MaxInt64},
		{"-9223372036854775808", 0, math.MinInt64},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := money.Parse(tc.in, tc.minorUnit)
			if err != nil {
				t.Fatalf("Parse(%q, %d): %v", tc.in, tc.minorUnit, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q, %d) = %d, want %d", tc.in, tc.minorUnit, got, tc.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in        string
		minorUnit int
	}{
		"empty":                 {"", 2},
		"blank":                 {"   ", 2},
		"too many decimals":     {"12.345", 2},
		"one decimal too many":  {"12.345", 2},
		"decimals on a 0 scale": {"12.3", 0},
		"thousands separator":   {"1,000.50", 2},
		"currency symbol":       {"$100", 2},
		"trailing symbol":       {"100$", 2},
		"exponent":              {"1e3", 2},
		"trailing point":        {"12.", 2},
		"leading point":         {".5", 2},
		"two points":            {"1.2.3", 2},
		"letters":               {"abc", 2},
		"embedded sign":         {"1-2", 2},
		"trailing sign":         {"12-", 2},
		"leading plus":          {"+12.00", 2},
		"double negative":       {"--12", 2},
		"lone sign":             {"-", 2},
		"internal space":        {"1 000", 2},
		"nbsp":                  {"1 000", 2},
		"tab inside":            {"1\t0", 2},
		"unicode digits":        {"１２", 2},
		"overflow positive":     {"92233720368547758.08", 2},
		"overflow negative":     {"-92233720368547758.09", 2},
		"overflow by many":      {"99999999999999999999", 0},
		"absurdly long":         {strings.Repeat("9", 400), 2},
		"minor unit too large":  {"1.00", 5},
		"minor unit negative":   {"1.00", -1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := money.Parse(tc.in, tc.minorUnit)
			if err == nil {
				t.Fatalf("Parse(%q, %d) = %d, want an error", tc.in, tc.minorUnit, got)
			}
			if !errors.Is(err, money.ErrInvalidAmount) {
				t.Errorf("Parse(%q, %d) returned %v, which is not ErrInvalidAmount", tc.in, tc.minorUnit, err)
			}
		})
	}
}

// An overflowing amount must be reported, never silently wrapped into a
// plausible-looking negative number.
func TestParseDoesNotWrapOnOverflow(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"92233720368547758.08", "9223372036854775808", "18446744073709551616"} {
		got, err := money.Parse(in, 2)
		if err == nil {
			t.Errorf("Parse(%q) = %d with no error; it wrapped", in, got)
		}
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		minor     int64
		minorUnit int
		want      string
	}{
		{100050, 2, "1000.50"},
		{5, 2, "0.05"},
		{-5, 2, "-0.05"},
		{0, 2, "0.00"},
		{0, 0, "0"},
		{1234, 0, "1234"},
		{-1234, 0, "-1234"},
		{100, 2, "1.00"},
		{99, 2, "0.99"},
		{-99, 2, "-0.99"},
		{1, 4, "0.0001"},
		{-1, 4, "-0.0001"},
		{12345, 4, "1.2345"},
		{math.MaxInt64, 2, "92233720368547758.07"},
		// The one value whose negation is not an int64.
		{math.MinInt64, 2, "-92233720368547758.08"},
		{math.MinInt64, 0, "-9223372036854775808"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if got := money.Format(tc.minor, tc.minorUnit); got != tc.want {
				t.Errorf("Format(%d, %d) = %q, want %q", tc.minor, tc.minorUnit, got, tc.want)
			}
		})
	}
}

// Format always emits exactly minorUnit decimals, which is what makes the
// output parseable again.
func TestFormatAlwaysHasTheFullScale(t *testing.T) {
	t.Parallel()

	for _, minorUnit := range []int{0, 1, 2, 3, 4} {
		for _, v := range []int64{0, 1, -1, 7, 12345, -98765, math.MaxInt64, math.MinInt64} {
			got := money.Format(v, minorUnit)
			_, fraction, hasPoint := strings.Cut(got, ".")

			switch {
			case minorUnit == 0 && hasPoint:
				t.Errorf("Format(%d, 0) = %q, want no decimal point", v, got)
			case minorUnit > 0 && !hasPoint:
				t.Errorf("Format(%d, %d) = %q, want a decimal point", v, minorUnit, got)
			case minorUnit > 0 && len(fraction) != minorUnit:
				t.Errorf("Format(%d, %d) = %q, want %d decimals", v, minorUnit, got, minorUnit)
			}
		}
	}
}

// The property the presentation layer depends on: an amount rendered for
// display can be read back without loss.
func TestParseFormatRoundTrip(t *testing.T) {
	t.Parallel()

	values := []int64{
		0, 1, -1, 5, -5, 99, 100, 101, -100, 999, 1000, 100050, -100050,
		123456789, -123456789, math.MaxInt64, math.MinInt64,
	}

	for _, minorUnit := range []int{0, 1, 2, 3, 4} {
		for _, want := range values {
			formatted := money.Format(want, minorUnit)

			got, err := money.Parse(formatted, minorUnit)
			if err != nil {
				t.Errorf("Parse(Format(%d, %d)) = %q: %v", want, minorUnit, formatted, err)
				continue
			}
			if got != want {
				t.Errorf("round trip at scale %d: %d -> %q -> %d", minorUnit, want, formatted, got)
			}
		}
	}
}

// Negative zero has one representation once it has been through the parser.
func TestNegativeZeroNormalises(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"-0", "-0.00", "0", "0.00"} {
		got, err := money.Parse(in, 2)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got != 0 {
			t.Errorf("Parse(%q) = %d, want 0", in, got)
		}
	}
	if got := money.Format(0, 2); got != "0.00" {
		t.Errorf("Format(0, 2) = %q, want %q", got, "0.00")
	}
}
