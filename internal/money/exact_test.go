package money_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

func TestRoundHalfEven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		num, den int64
		want     int64
	}{
		{5, 2, 2},    // 2.5 -> 2, even
		{7, 2, 4},    // 3.5 -> 4, even
		{-5, 2, -2},  // symmetric about zero
		{-7, 2, -4},  //
		{10, 3, 3},   // 3.33 -> 3
		{20, 3, 7},   // 6.67 -> 7
		{-20, 3, -7}, //
		{6, 1, 6},    // already whole
		{0, 9, 0},    //
	}
	for _, tc := range tests {
		got, err := money.RoundHalfEven(big.NewRat(tc.num, tc.den))
		if err != nil {
			t.Fatalf("RoundHalfEven(%d/%d): %v", tc.num, tc.den, err)
		}
		if got != tc.want {
			t.Errorf("RoundHalfEven(%d/%d) = %d, want %d", tc.num, tc.den, got, tc.want)
		}
	}

	huge := new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 70))
	if _, err := money.RoundHalfEven(huge); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("an out-of-range value returned %v, want ErrOverflow", err)
	}
	if _, err := money.RoundHalfEven(nil); err == nil {
		t.Error("a nil value was accepted")
	}
}

func TestConvertMinorIsRoundedConvertMinorExact(t *testing.T) {
	t.Parallel()

	rate := big.NewRat(1, 3)
	exact, err := money.ConvertMinorExact(100, 2, 2, rate)
	if err != nil {
		t.Fatalf("ConvertMinorExact: %v", err)
	}
	if exact.RatString() != "100/3" {
		t.Errorf("exact = %s, want 100/3", exact.RatString())
	}
	rounded, err := money.ConvertMinor(100, 2, 2, rate)
	if err != nil {
		t.Fatalf("ConvertMinor: %v", err)
	}
	if rounded != 33 {
		t.Errorf("rounded = %d, want 33", rounded)
	}

	// The scale shift: 1 USD (100 cents) at 500 VES per USD, into a currency
	// with no minor unit, is exactly 500.
	exact, err = money.ConvertMinorExact(100, 2, 0, big.NewRat(500, 1))
	if err != nil {
		t.Fatalf("ConvertMinorExact: %v", err)
	}
	if exact.RatString() != "500" {
		t.Errorf("exact = %s, want 500", exact.RatString())
	}

	if _, err := money.ConvertMinorExact(1, 2, 2, big.NewRat(0, 1)); !errors.Is(err, money.ErrInvalidDecimal) {
		t.Errorf("a zero rate returned %v, want ErrInvalidDecimal", err)
	}
}

func TestRatioBasisPoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		num, den int64
		want     int64
		defined  bool
	}{
		{84020, 1160000, 724, true},  // 7.2431% -> 7.24%
		{1, 3, 3333, true},           // 33.33%
		{2, 3, 6667, true},           // 66.67%
		{-1, 4, -2500, true},         // -25%
		{1, 20000, 0, true},          // 0.5 bp -> 0, even
		{3, 20000, 2, true},          // 1.5 bp -> 2, even
		{100, 100, 10000, true},      // 100%
		{5, 0, 0, false},             // undefined, not infinite
		{0, 0, 0, false},             // undefined, not NaN
		{math.MaxInt64, 1, 0, false}, // overflow is an error, checked below
	}
	for _, tc := range tests[:len(tests)-1] {
		got, defined, err := money.RatioBasisPoints(tc.num, tc.den)
		if err != nil {
			t.Fatalf("RatioBasisPoints(%d, %d): %v", tc.num, tc.den, err)
		}
		if defined != tc.defined || got != tc.want {
			t.Errorf("RatioBasisPoints(%d, %d) = %d, %v; want %d, %v",
				tc.num, tc.den, got, defined, tc.want, tc.defined)
		}
	}

	if _, _, err := money.RatioBasisPoints(math.MaxInt64, 1); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("an overflowing ratio returned %v, want ErrOverflow", err)
	}
}

func TestAllocateAddsUpExactly(t *testing.T) {
	t.Parallel()

	// Three equal thirds of 100: independent rounding gives 33+33+33 = 99.
	parts := []*big.Rat{big.NewRat(100, 3), big.NewRat(100, 3), big.NewRat(100, 3)}
	got, err := money.Allocate(100, parts)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got[0] != 34 || got[1] != 33 || got[2] != 33 {
		t.Errorf("Allocate = %v, want [34 33 33]: the tie goes to the first part", got)
	}

	// The largest remainder wins, not the first part.
	parts = []*big.Rat{big.NewRat(101, 10), big.NewRat(209, 10), big.NewRat(690, 10)}
	got, err = money.Allocate(100, parts)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got[0] != 10 || got[1] != 21 || got[2] != 69 {
		t.Errorf("Allocate = %v, want [10 21 69]", got)
	}

	// Negative parts follow the same rule: floor, then the remainder.
	parts = []*big.Rat{big.NewRat(-25, 2), big.NewRat(55, 2)}
	got, err = money.Allocate(15, parts)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got[0]+got[1] != 15 {
		t.Errorf("Allocate = %v, which sums to %d, want 15", got, got[0]+got[1])
	}

	// Nothing to allocate across nothing.
	got, err = money.Allocate(0, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("Allocate(0, nil) = %v, %v", got, err)
	}

	// A target that is not the rounding of the parts is refused, not smeared.
	if _, err := money.Allocate(500, []*big.Rat{big.NewRat(1, 1)}); !errors.Is(err, money.ErrInvalidAmount) {
		t.Errorf("an unreachable target returned %v, want ErrInvalidAmount", err)
	}
	if _, err := money.Allocate(1, nil); !errors.Is(err, money.ErrInvalidAmount) {
		t.Errorf("a target across no parts returned %v, want ErrInvalidAmount", err)
	}
}
