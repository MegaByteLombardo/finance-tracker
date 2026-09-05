// Package money converts between the decimal strings a person types and the
// exact integer minor units this application stores.
//
// It exists because binary floating point cannot represent ordinary decimal
// amounts exactly. The error per operation is tiny and compounds silently
// across summation and conversion, which is precisely what a finance
// application does constantly; a balance off by a cent cannot be reconciled
// against a statement and gives no signal, it just quietly disagrees.
//
// There is no float anywhere below this line: parsing walks the digits and
// accumulates into an integer, and formatting is string surgery on the decimal
// representation. Nothing here converts through float64 "just for a moment".
//
// It is deliberately two functions and a sentinel error rather than a Money
// type. An amount is never separated from its currency in this codebase — the
// scale always arrives from the currency's minor_unit at the call site — so a
// wrapper struct would carry no invariant the surrounding code does not already
// hold. When one is genuinely needed, it is added then.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidAmount is returned for every input this package refuses. Callers
// distinguish it with errors.Is and usually translate it into their own error.
var ErrInvalidAmount = errors.New("invalid amount")

const (
	// MinMinorUnit and MaxMinorUnit mirror the range the currencies table
	// enforces: the real span of ISO 4217 exponents. They are duplicated here
	// rather than imported so that this package stays independent of the
	// currency catalogue; currency_test asserts the two agree.
	MinMinorUnit = 0
	MaxMinorUnit = 4

	// maxInputLength is a cheap guard against absurd input. The longest
	// meaningful amount is 19 digits plus a sign and a decimal point; anything
	// beyond this cannot be representable, so rejecting it early avoids walking
	// a megabyte of digits to reach the same conclusion.
	maxInputLength = 32
)

// Parse converts a decimal string to minor units at the given scale.
//
//	Parse("1000.50", 2) ->  100050
//	Parse("25.99",   2) ->    2599
//	Parse("-40",     2) ->   -4000
//	Parse("1234",    0) ->    1234
//
// Accepted: optional surrounding whitespace, an optional leading "-", one or
// more digits, and optionally a decimal point followed by at most minorUnit
// digits. Nothing else — no thousands separators, no currency symbols, no
// exponent notation, no leading "+".
//
// An amount with more decimal places than the currency has is an error rather
// than a rounding: silently turning 12.345 into 12.34 or 12.35 would discard
// money the user typed, and which of the two it discarded would depend on a
// rule they never agreed to.
func Parse(s string, minorUnit int) (int64, error) {
	if minorUnit < MinMinorUnit || minorUnit > MaxMinorUnit {
		return 0, fmt.Errorf("%w: minor unit %d is outside the supported range %d-%d",
			ErrInvalidAmount, minorUnit, MinMinorUnit, MaxMinorUnit)
	}

	original := s

	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: an amount is required", ErrInvalidAmount)
	}
	if len(s) > maxInputLength {
		return 0, fmt.Errorf("%w: %q is too long to be an amount", ErrInvalidAmount, truncate(original))
	}

	negative := false
	if rest, found := strings.CutPrefix(s, "-"); found {
		negative = true
		s = rest
	}

	whole, fraction, hasPoint := strings.Cut(s, ".")

	if whole == "" || !allDigits(whole) {
		return 0, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, truncate(original))
	}
	if hasPoint {
		// "12." and "12.3.4" both land here: the first has no digits after the
		// point, the second still holds one.
		if fraction == "" || !allDigits(fraction) {
			return 0, fmt.Errorf("%w: %q is not a decimal amount", ErrInvalidAmount, truncate(original))
		}
		if len(fraction) > minorUnit {
			return 0, fmt.Errorf("%w: %q has %d decimal places, but this currency has %d",
				ErrInvalidAmount, truncate(original), len(fraction), minorUnit)
		}
	}

	// Scaling is concatenation, not multiplication: "12" + "3" padded to two
	// places is the digit string "1230", which is 12.30 in minor units.
	digits := whole + fraction + strings.Repeat("0", minorUnit-len(fraction))

	minor, err := toInt64(digits, negative)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is larger than this application can represent",
			ErrInvalidAmount, truncate(original))
	}
	return minor, nil
}

// Format renders minor units back to a decimal string at the given scale.
//
//	Format( 100050, 2) -> "1000.50"
//	Format(      5, 2) -> "0.05"
//	Format(     -5, 2) -> "-0.05"
//	Format(   1234, 0) -> "1234"
//
// The result always has exactly minorUnit decimal places, so it round-trips:
// Parse(Format(v, u), u) == v for every v.
//
// There are no thousands separators and no currency symbol. Both are
// presentation decisions that depend on locale and on where the symbol sits,
// and neither can be made correctly here; the currency travels alongside every
// amount so the caller has what it needs.
func Format(minor int64, minorUnit int) string {
	if minorUnit < MinMinorUnit {
		// Unreachable through a currency, whose minor_unit the database bounds
		// to 0-4. Guarded because the alternative is a panic on a negative
		// slice index, and this package never panics.
		minorUnit = MinMinorUnit
	}

	// Taking the digits from FormatInt rather than negating the value avoids
	// the one input where negation overflows: -math.MinInt64 is not an int64.
	digits := strconv.FormatInt(minor, 10)
	sign := ""
	if rest, found := strings.CutPrefix(digits, "-"); found {
		sign, digits = "-", rest
	}

	if minorUnit == 0 {
		return sign + digits
	}
	if len(digits) <= minorUnit {
		// 5 at scale 2 needs a leading "0" as well as the padding: "005".
		digits = strings.Repeat("0", minorUnit-len(digits)+1) + digits
	}

	split := len(digits) - minorUnit
	return sign + digits[:split] + "." + digits[split:]
}

// toInt64 accumulates a digit string, refusing to wrap.
//
// The accumulator is unsigned so that math.MinInt64, whose magnitude is one
// greater than math.MaxInt64, is representable while it is being built.
func toInt64(digits string, negative bool) (int64, error) {
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}

	var acc uint64
	for i := range len(digits) {
		d := uint64(digits[i] - '0')
		// acc*10+d > limit, rearranged so the multiplication never happens.
		if acc > (limit-d)/10 {
			return 0, errors.New("overflow")
		}
		acc = acc*10 + d
	}

	if !negative {
		return int64(acc), nil
	}
	if acc == uint64(math.MaxInt64)+1 {
		return math.MinInt64, nil
	}
	return -int64(acc), nil
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// truncate keeps a rejected value short enough to be safe in an error message.
func truncate(s string) string {
	const limit = 40
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
