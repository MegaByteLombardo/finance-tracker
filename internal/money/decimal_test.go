package money_test

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

func TestParseDecimalExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in        string
		want      string // canonical form, which preserves the scale
		wantScale int32
	}{
		// What the BCV actually publishes, at the precision it publishes it.
		{"842.20670000", "842.20670000", 8},
		{"977.87777730", "977.87777730", 8},
		{"17.32921333", "17.32921333", 8},

		{"500", "500", 0},
		{"1.1", "1.1", 1},
		{"0", "0", 0},
		{"0.00", "0.00", 2},

		// Leading zeros are not part of the value, so they are dropped: every
		// value has exactly one spelling.
		{"007.50", "7.50", 2},
		{"000", "0", 0},
		{"0000.1", "0.1", 1},

		// Trailing zeros ARE part of the value's precision and survive.
		{"1.000000", "1.000000", 6},

		{"-0.50", "-0.50", 2},
		{"-1", "-1", 0},

		// Negative zero is still zero, and is spelled without a sign.
		{"-0", "0", 0},
		{"-0.00", "0.00", 2},

		{"  842.20670000  ", "842.20670000", 8},

		// The bounds themselves must be representable, not merely "large".
		{"0.000000000001", "0.000000000001", 12},
		{strings.Repeat("9", 24), strings.Repeat("9", 24), 0},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := money.ParseDecimal(tc.in)
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Errorf("ParseDecimal(%q).String() = %q, want %q", tc.in, got.String(), tc.want)
			}
			if got.Scale() != tc.wantScale {
				t.Errorf("ParseDecimal(%q).Scale() = %d, want %d", tc.in, got.Scale(), tc.wantScale)
			}

			// Round trip: the canonical form must parse back to the same value
			// at the same scale, which is what lets the stored TEXT be compared
			// with a published string.
			again, err := money.ParseDecimal(got.String())
			if err != nil {
				t.Fatalf("re-parsing %q: %v", got.String(), err)
			}
			if again.String() != got.String() || again.Scale() != got.Scale() {
				t.Errorf("round trip of %q produced %q at scale %d",
					got.String(), again.String(), again.Scale())
			}
		})
	}
}

func TestParseDecimalRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"a comma decimal separator, which is the provider's job to normalise", "477,14880000"},
		{"a thousands separator", "1,000.50"},
		{"a space as a thousands separator", "1 000.50"},
		{"an underscore", "1_000"},
		{"a leading plus", "+1.5"},
		{"two points", "1.2.3"},
		{"a trailing point", "12."},
		{"a leading point", ".5"},
		{"exponent notation", "1e3"},
		{"exponent notation, upper case", "1E3"},
		{"letters", "abc"},
		{"a currency symbol", "$1.50"},
		{"a percent sign", "10%"},
		{"a bare minus", "-"},
		{"a doubled minus", "--1"},
		{"a trailing minus", "1-"},
		{"a scale beyond the supported maximum", "1.0000000000001"},
		{"more integer digits than supported", strings.Repeat("9", 25)},
		{"an absurdly long input", strings.Repeat("1", 200)},
		{"an internal space", "84 2.20"},
		{"a non-breaking space", "842 .20"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.ParseDecimal(tc.in)
			if err == nil {
				t.Fatalf("ParseDecimal(%q) = %s, want an error", tc.in, got)
			}
			if !errors.Is(err, money.ErrInvalidDecimal) {
				t.Errorf("ParseDecimal(%q) error = %v, want it to wrap ErrInvalidDecimal", tc.in, err)
			}
		})
	}
}

func TestDecimalZeroValueIsUsableZero(t *testing.T) {
	t.Parallel()

	var d money.Decimal

	if got := d.String(); got != "0" {
		t.Errorf("zero Decimal String() = %q, want %q", got, "0")
	}
	if d.Sign() != 0 {
		t.Errorf("zero Decimal Sign() = %d, want 0", d.Sign())
	}
	if d.IsPositive() {
		t.Error("zero Decimal reported itself as positive")
	}
	if d.Scale() != 0 {
		t.Errorf("zero Decimal Scale() = %d, want 0", d.Scale())
	}
	if got := d.Rat(); got.Sign() != 0 {
		t.Errorf("zero Decimal Rat() = %s, want 0", got.RatString())
	}
	if got := d.Unscaled(); got.Sign() != 0 {
		t.Errorf("zero Decimal Unscaled() = %s, want 0", got.String())
	}
	if !d.Equal(money.MustParseDecimal("0.000")) {
		t.Error("zero Decimal is not equal to 0.000")
	}
}

func TestDecimalSignAndIsPositive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in           string
		wantSign     int
		wantPositive bool
	}{
		{"842.20670000", 1, true},
		{"0.00000001", 1, true},
		{"0", 0, false},
		{"0.00", 0, false},
		{"-0.00000001", -1, false},
		{"-500", -1, false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			d := money.MustParseDecimal(tc.in)
			if d.Sign() != tc.wantSign {
				t.Errorf("%s.Sign() = %d, want %d", tc.in, d.Sign(), tc.wantSign)
			}
			if d.IsPositive() != tc.wantPositive {
				t.Errorf("%s.IsPositive() = %t, want %t", tc.in, d.IsPositive(), tc.wantPositive)
			}
		})
	}
}

// TestDecimalCmpIsNumericNotTextual is the reason Cmp exists at all: the stored
// representation keeps its scale, so two spellings of the same number must
// still compare equal.
func TestDecimalCmpIsNumericNotTextual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		left, right string
		want        int
	}{
		{"842.20", "842.200", 0},
		{"842.20", "842.2", 0},
		{"1", "1.0000", 0},
		{"0", "-0.00", 0},
		{"842.20670000", "842.20670001", -1},
		{"842.20670001", "842.20670000", 1},
		{"2", "10", -1},
		{"10", "2", 1},
		{"-1", "1", -1},
		{"-2", "-1", -1},
		{"-1.5", "-1.50", 0},
	}

	for _, tc := range tests {
		t.Run(tc.left+" vs "+tc.right, func(t *testing.T) {
			t.Parallel()

			left, right := money.MustParseDecimal(tc.left), money.MustParseDecimal(tc.right)
			if got := left.Cmp(right); got != tc.want {
				t.Errorf("%s.Cmp(%s) = %d, want %d", tc.left, tc.right, got, tc.want)
			}
			if got := left.Equal(right); got != (tc.want == 0) {
				t.Errorf("%s.Equal(%s) = %t, want %t", tc.left, tc.right, got, tc.want == 0)
			}
		})
	}
}

func TestDecimalRatIsExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string // big.Rat's canonical a/b form
	}{
		{"842.20670000", "8422067/10000"},
		{"0.5", "1/2"},
		{"1.1", "11/10"},
		{"500", "500"},
		{"-0.25", "-1/4"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got := money.MustParseDecimal(tc.in).Rat()
			if got.RatString() != tc.want {
				t.Errorf("%s.Rat() = %s, want %s", tc.in, got.RatString(), tc.want)
			}
		})
	}
}

// TestDecimalIsImmutable is the whole point of holding the unscaled value behind
// a pointer that nothing hands out.
//
// A Decimal is copied by value all over this application. If an accessor
// returned the storage rather than a copy, one in-place math/big operation on
// the returned value would silently rewrite every copy of that Decimal in the
// program, including ones already written into a struct that had been passed
// somewhere else.
func TestDecimalIsImmutable(t *testing.T) {
	t.Parallel()

	original := money.MustParseDecimal("842.20670000")
	copied := original

	t.Run("mutating the value returned by Rat", func(t *testing.T) {
		r := original.Rat()
		r.Mul(r, big.NewRat(1000000, 1))
		r.Add(r, big.NewRat(7, 3))

		if got := original.String(); got != "842.20670000" {
			t.Errorf("original changed to %q after its Rat was mutated", got)
		}
		if got := original.Rat().RatString(); got != "8422067/10000" {
			t.Errorf("a second Rat() returned %s, want 8422067/10000", got)
		}
	})

	t.Run("mutating the value returned by Unscaled", func(t *testing.T) {
		u := original.Unscaled()
		u.Mul(u, big.NewInt(-999))

		if got := original.String(); got != "842.20670000" {
			t.Errorf("original changed to %q after its Unscaled was mutated", got)
		}
	})

	t.Run("a copy made before the mutations is untouched", func(t *testing.T) {
		if got := copied.String(); got != "842.20670000" {
			t.Errorf("copy changed to %q", got)
		}
		if !copied.Equal(original) {
			t.Error("copy and original diverged")
		}
	})

	t.Run("deriving from a copy does not touch the original", func(t *testing.T) {
		derived := copied.Rat()
		derived.Inv(derived)

		if !copied.Equal(original) {
			t.Error("copy changed after a derived Rat was inverted")
		}
		if got := original.String(); got != "842.20670000" {
			t.Errorf("original changed to %q", got)
		}
	})
}

func TestConvertMinorExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   int64
		fromUnit int
		toUnit   int
		rate     string // 1 from = rate to
		want     int64
	}{
		// The acceptance scenario, leg by leg. 1 USD = 500 VES, so valuing VES
		// in USD uses the inverted rate 1 VES = 1/500 USD.
		{"50,000.00 VES at 1 VES = 1/500 USD", 5000000, 2, 2, "0.002", 10000},
		{"200.00 EUR at the EUR/USD cross of 1.1", 20000, 2, 2, "1.1", 22000},
		{"1,000.00 USD at 1 USD = 500 VES", 100000, 2, 2, "500", 50000000},

		// Different minor units on the two sides.
		{"100.00 USD into a 0-decimal currency", 10000, 2, 0, "150", 15000},
		{"1000 JPY-like into a 2-decimal currency", 1000, 0, 2, "0.0067", 670},
		{"scale 2 to scale 4", 12345, 2, 4, "1", 1234500},
		{"scale 4 to scale 2", 1234500, 4, 2, "1", 12345},

		// Exactness where a float would not be exact.
		{"0.1 + 0.2 territory", 10, 2, 2, "0.3", 3},
		{"a rate with eight decimals", 100000, 2, 2, "842.20670000", 84220670},

		// Zero and negatives.
		{"zero converts to zero", 0, 2, 2, "842.2067", 0},
		{"a negative balance converts by the same rule", -5000000, 2, 2, "0.002", -10000},
		{"a negative balance at a large rate", -100000, 2, 2, "500", -50000000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rate := money.MustParseDecimal(tc.rate).Rat()
			got, err := money.ConvertMinor(tc.amount, tc.fromUnit, tc.toUnit, rate)
			if err != nil {
				t.Fatalf("ConvertMinor(%d, %d, %d, %s): %v",
					tc.amount, tc.fromUnit, tc.toUnit, tc.rate, err)
			}
			if got != tc.want {
				t.Errorf("ConvertMinor(%d, %d, %d, %s) = %d, want %d",
					tc.amount, tc.fromUnit, tc.toUnit, tc.rate, got, tc.want)
			}
		})
	}
}

// TestConvertMinorRoundsHalfToEven covers both sides of zero, because balances
// in this application are legitimately negative and a rounding rule that is not
// symmetric would value a debt differently from the credit that cancels it.
func TestConvertMinorRoundsHalfToEven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		amount int64
		rate   string
		want   int64
	}{
		// Exact ties. Half to even keeps an already-even quotient and steps the
		// odd one away from zero to reach the even neighbour.
		{"12345 / 2 = 6172.5 -> 6172 (even)", 12345, "0.5", 6172},
		{"12347 / 2 = 6173.5 -> 6174 (even)", 12347, "0.5", 6174},
		{"-12345 / 2 = -6172.5 -> -6172 (even)", -12345, "0.5", -6172},
		{"-12347 / 2 = -6173.5 -> -6174 (even)", -12347, "0.5", -6174},
		{"1 / 2 = 0.5 -> 0 (even)", 1, "0.5", 0},
		{"-1 / 2 = -0.5 -> 0 (even)", -1, "0.5", 0},
		{"3 / 2 = 1.5 -> 2 (even)", 3, "0.5", 2},
		{"-3 / 2 = -1.5 -> -2 (even)", -3, "0.5", -2},
		{"5 / 2 = 2.5 -> 2 (even)", 5, "0.5", 2},
		{"-5 / 2 = -2.5 -> -2 (even)", -5, "0.5", -2},

		// Not ties: ordinary nearest, in both directions.
		{"below the halfway point rounds down", 1001, "0.001", 1},
		{"above the halfway point rounds up", 1999, "0.001", 2},
		{"below the halfway point, negative", -1001, "0.001", -1},
		{"above the halfway point, negative", -1999, "0.001", -2},

		// A realistic valuation: 50,001.23 VES at 1 USD = 500 VES.
		{"5000123 VES inverted at 500", 5000123, "0.002", 10000},
		{"-5000123 VES inverted at 500", -5000123, "0.002", -10000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rate := money.MustParseDecimal(tc.rate).Rat()
			got, err := money.ConvertMinor(tc.amount, 2, 2, rate)
			if err != nil {
				t.Fatalf("ConvertMinor: %v", err)
			}
			if got != tc.want {
				t.Errorf("ConvertMinor(%d, 2, 2, %s) = %d, want %d", tc.amount, tc.rate, got, tc.want)
			}
		})
	}
}

// TestConvertMinorRoundingIsSymmetric states the property the table above spot
// checks: negating the input negates the output, for every input.
func TestConvertMinorRoundingIsSymmetric(t *testing.T) {
	t.Parallel()

	rates := []string{"0.5", "0.002", "1.1", "842.20670000", "0.333333333333"}

	for _, r := range rates {
		t.Run(r, func(t *testing.T) {
			t.Parallel()

			for amount := int64(-5000); amount <= 5000; amount += 7 {
				positive, err := money.ConvertMinor(amount, 2, 2, money.MustParseDecimal(r).Rat())
				if err != nil {
					t.Fatalf("ConvertMinor(%d): %v", amount, err)
				}
				negative, err := money.ConvertMinor(-amount, 2, 2, money.MustParseDecimal(r).Rat())
				if err != nil {
					t.Fatalf("ConvertMinor(%d): %v", -amount, err)
				}
				if negative != -positive {
					t.Fatalf("rate %s: ConvertMinor(%d) = %d but ConvertMinor(%d) = %d",
						r, amount, positive, -amount, negative)
				}
			}
		})
	}
}

// TestConvertMinorDoesNotMutateTheRate guards the copies made of Num and Denom.
// A rate resolved once and applied to several balances must apply the same value
// every time.
func TestConvertMinorDoesNotMutateTheRate(t *testing.T) {
	t.Parallel()

	rate := money.MustParseDecimal("842.20670000").Rat()
	before := rate.RatString()

	for _, amount := range []int64{100000, -250000, 0, 7} {
		if _, err := money.ConvertMinor(amount, 2, 2, rate); err != nil {
			t.Fatalf("ConvertMinor(%d): %v", amount, err)
		}
		if got := rate.RatString(); got != before {
			t.Fatalf("the rate changed to %s after converting %d; it was %s", got, amount, before)
		}
	}

	// And the same rate still produces the same answer as a fresh one.
	first, err := money.ConvertMinor(100000, 2, 2, rate)
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	second, err := money.ConvertMinor(100000, 2, 2, money.MustParseDecimal("842.20670000").Rat())
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	if first != second {
		t.Errorf("a reused rate produced %d but a fresh one produced %d", first, second)
	}
}

func TestConvertMinorOverflows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		amount int64
		rate   string
	}{
		{"the largest balance at a hyperinflationary rate", math.MaxInt64, "842.20670000"},
		{"the smallest balance at a hyperinflationary rate", math.MinInt64, "842.20670000"},
		{"a large balance scaled up by a factor of ten", math.MaxInt64 / 5, "10"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.ConvertMinor(tc.amount, 2, 2, money.MustParseDecimal(tc.rate).Rat())
			if err == nil {
				t.Fatalf("ConvertMinor(%d, 2, 2, %s) = %d, want an overflow error",
					tc.amount, tc.rate, got)
			}
			if !errors.Is(err, money.ErrOverflow) {
				t.Errorf("error = %v, want it to wrap ErrOverflow", err)
			}
		})
	}
}

// TestConvertMinorRepresentsTheInt64Boundaries is the counterpart to the test
// above: the edge of the range must be reachable, not merely "large".
func TestConvertMinorRepresentsTheInt64Boundaries(t *testing.T) {
	t.Parallel()

	one := money.MustParseDecimal("1").Rat()

	for _, amount := range []int64{math.MaxInt64, math.MinInt64} {
		got, err := money.ConvertMinor(amount, 2, 2, one)
		if err != nil {
			t.Fatalf("ConvertMinor(%d, 2, 2, 1): %v", amount, err)
		}
		if got != amount {
			t.Errorf("ConvertMinor(%d, 2, 2, 1) = %d, want %d", amount, got, amount)
		}
	}
}

func TestConvertMinorRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	one := money.MustParseDecimal("1").Rat()

	tests := []struct {
		name     string
		fromUnit int
		toUnit   int
		rate     *big.Rat
		wantErr  error
	}{
		{"a negative source minor unit", -1, 2, one, money.ErrInvalidAmount},
		{"a source minor unit beyond ISO 4217", 5, 2, one, money.ErrInvalidAmount},
		{"a negative destination minor unit", 2, -1, one, money.ErrInvalidAmount},
		{"a destination minor unit beyond ISO 4217", 2, 5, one, money.ErrInvalidAmount},
		{"no rate at all", 2, 2, nil, money.ErrInvalidDecimal},
		{"a zero rate", 2, 2, new(big.Rat), money.ErrInvalidDecimal},
		{"a negative rate", 2, 2, big.NewRat(-1, 2), money.ErrInvalidDecimal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.ConvertMinor(100, tc.fromUnit, tc.toUnit, tc.rate)
			if err == nil {
				t.Fatalf("ConvertMinor = %d, want an error", got)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}

// TestRateInversionIsExact states the property the valuation path depends on:
// inverting a rate and inverting it back must return the original value, which
// is only true because the inversion never passes through a decimal.
func TestRateInversionIsExact(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"842.20670000", "977.87777730", "3", "0.000000000001", "1.1"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			original := money.MustParseDecimal(in).Rat()

			inverted := new(big.Rat).Inv(money.MustParseDecimal(in).Rat())
			back := new(big.Rat).Inv(inverted)

			if back.Cmp(original) != 0 {
				t.Errorf("inverting %s twice produced %s", in, back.RatString())
			}

			// 1/3 is not a terminating decimal, so this is the case a decimal
			// representation of the inverse would have had to round.
			product := new(big.Rat).Mul(original, inverted)
			if product.Cmp(big.NewRat(1, 1)) != 0 {
				t.Errorf("%s times its inverse = %s, want exactly 1", in, product.RatString())
			}
		})
	}
}

// TestCrossRateIsExact covers the other half of the arithmetic: a cross rate is
// one rational divided by another, and it must stay exact even when neither
// operand nor the result is a terminating decimal.
func TestCrossRateIsExact(t *testing.T) {
	t.Parallel()

	// 1 EUR = 550 VES and 1 USD = 500 VES, so 1 EUR = 1.1 USD.
	eurVES := money.MustParseDecimal("550").Rat()
	usdVES := money.MustParseDecimal("500").Rat()

	cross := new(big.Rat).Quo(eurVES, usdVES)
	if cross.RatString() != "11/10" {
		t.Fatalf("EUR/USD = %s, want 11/10", cross.RatString())
	}

	got, err := money.ConvertMinor(20000, 2, 2, cross)
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	if got != 22000 {
		t.Errorf("200.00 EUR in USD = %d minor units, want 22000", got)
	}

	// A cross that is not a terminating decimal: 1 EUR = 977.8777773 VES and
	// 1 USD = 842.2067 VES. The result is irrational as a decimal and must
	// still round to an exact integer number of cents.
	messy := new(big.Rat).Quo(
		money.MustParseDecimal("977.87777730").Rat(),
		money.MustParseDecimal("842.20670000").Rat(),
	)
	converted, err := money.ConvertMinor(20000, 2, 2, messy)
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	// The exact value is 195575555460/8422067 = 23221.79999992876..., which is
	// a hair under 23221.8 and rounds to 23222. It is deliberately this close
	// to a tie: it is exactly the kind of value a float would place on the
	// wrong side of the boundary, and the rational arithmetic above does not.
	if converted != 23222 {
		t.Errorf("200.00 EUR at the real BCV cross = %d, want 23222", converted)
	}
}
