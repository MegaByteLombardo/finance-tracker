package money

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Errors this file returns, in addition to ErrInvalidAmount.
var (
	// ErrInvalidDecimal is returned for every decimal string this package
	// refuses. It is separate from ErrInvalidAmount because a rate is not an
	// amount: it has no currency, no minor unit, and its own bounds, so a
	// caller that wants to say "that exchange rate is malformed" must not have
	// to phrase it as "that amount is malformed".
	ErrInvalidDecimal = errors.New("invalid decimal")

	// ErrOverflow means an exact result was computed but does not fit in the
	// int64 this application stores minor units in.
	//
	// It is deliberately not folded into ErrInvalidAmount. The input was valid
	// and the arithmetic was exact; what failed is representation, and a caller
	// showing a message to a person needs to be able to tell the difference.
	ErrOverflow = errors.New("value is too large to represent")
)

const (
	// MaxScale bounds the number of decimal places a Decimal may carry.
	//
	// The BCV publishes eight. Twelve leaves room for a source that publishes
	// more without admitting the arbitrary precision that would let a single
	// pasted value make every later comparison expensive.
	MaxScale = 12

	// maxIntegerDigits bounds the part before the decimal point. Venezuelan
	// hyperinflation makes a rate in the millions entirely plausible, so this
	// is generous; it exists to stop a megabyte of digits, not to express a
	// financial rule.
	maxIntegerDigits = 24

	// maxDecimalInputLength is a cheap guard applied before any digit is walked.
	maxDecimalInputLength = 48
)

// Decimal is an exact decimal number: unscaled / 10^scale.
//
// It exists because exchange rates are decimal values that no integer minor
// unit can hold - 842.20670000 is not a number of cents of anything - and
// because binary floating point cannot represent them exactly. There is no
// float anywhere in this type: parsing walks digits into a big.Int, formatting
// is string surgery, and every arithmetic operation a caller needs is performed
// on *big.Rat, which divides exactly.
//
// # Value semantics
//
// Decimal is copied by value, and unscaled is a POINTER on purpose. Embedding a
// big.Int by value would make every copy of a Decimal share the same backing
// slice, so a method that mutated the receiver's big.Int in place - which is
// how the math/big API is normally used - would reach through every copy that
// had ever been made of it. The pointer makes that impossible to do by
// accident, because the rule it obeys is stated once and is absolute:
//
//	The big.Int behind unscaled is IMMUTABLE after the constructor returns.
//	No method mutates it. No method returns it. Every accessor that hands out a
//	big value hands out a fresh one.
//
// That is why Rat and Unscaled both allocate rather than returning storage, and
// why there is no setter anywhere on this type. A Decimal is a value, and it
// behaves like one.
//
// The zero Decimal is a valid zero at scale 0, so a Decimal that was never
// constructed does not panic; every method treats a nil unscaled as zero.
type Decimal struct {
	unscaled *big.Int
	scale    int32
}

// ParseDecimal reads a canonical decimal string exactly.
//
//	ParseDecimal("842.20670000") -> unscaled 84220670000, scale 8
//	ParseDecimal("1.1")          -> unscaled 11,          scale 1
//	ParseDecimal("500")          -> unscaled 500,         scale 0
//	ParseDecimal("-0.50")        -> unscaled -50,         scale 2
//
// Accepted: optional surrounding whitespace, an optional leading "-", one or
// more digits, and optionally a decimal point followed by one or more digits.
// Nothing else - no thousands separators, no exponent notation, no leading "+",
// no bare "12." and no bare ".5". This mirrors money.Parse's refusal to guess,
// for the same reason: a value this application cannot read unambiguously is an
// error, never a silent reinterpretation.
//
// A comma is NOT accepted, even though the BCV publishes one. Normalising a
// locale-specific representation is the provider's job and it is written
// separately, because "477,148" is unambiguous only once you already know which
// document it came from.
//
// The SCALE IS PRESERVED. "842.20670000" keeps its eight decimal places through
// a round trip, so what is stored is exactly what the source published, trailing
// zeros included. Leading zeros in the integer part are not part of the value
// and are removed, so every value has exactly one canonical spelling.
func ParseDecimal(s string) (Decimal, error) {
	original := s

	s = strings.TrimSpace(s)
	if s == "" {
		return Decimal{}, fmt.Errorf("%w: a value is required", ErrInvalidDecimal)
	}
	if len(s) > maxDecimalInputLength {
		return Decimal{}, fmt.Errorf("%w: %q is too long to be a decimal",
			ErrInvalidDecimal, truncate(original))
	}

	negative := false
	if rest, found := strings.CutPrefix(s, "-"); found {
		negative = true
		s = rest
	}

	whole, fraction, hasPoint := strings.Cut(s, ".")

	if whole == "" || !allDigits(whole) {
		return Decimal{}, fmt.Errorf("%w: %q is not a decimal number",
			ErrInvalidDecimal, truncate(original))
	}
	if hasPoint {
		// "12." and "12.3.4" both land here: the first has no digits after the
		// point, the second still holds one.
		if fraction == "" || !allDigits(fraction) {
			return Decimal{}, fmt.Errorf("%w: %q is not a decimal number",
				ErrInvalidDecimal, truncate(original))
		}
		if len(fraction) > MaxScale {
			return Decimal{}, fmt.Errorf("%w: %q has %d decimal places, and at most %d are supported",
				ErrInvalidDecimal, truncate(original), len(fraction), MaxScale)
		}
	}
	if len(strings.TrimLeft(whole, "0")) > maxIntegerDigits {
		return Decimal{}, fmt.Errorf("%w: %q has more than %d digits before the decimal point",
			ErrInvalidDecimal, truncate(original), maxIntegerDigits)
	}

	// Scaling is concatenation, not multiplication, exactly as in money.Parse:
	// the digit string IS the unscaled integer.
	unscaled, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		// Unreachable: every byte has been checked to be a digit. Handled
		// rather than ignored because this package never panics and never
		// discards an error.
		return Decimal{}, fmt.Errorf("%w: %q is not a decimal number",
			ErrInvalidDecimal, truncate(original))
	}
	if negative && unscaled.Sign() != 0 {
		unscaled.Neg(unscaled)
	}

	return Decimal{unscaled: unscaled, scale: int32(len(fraction))}, nil
}

// MustParseDecimal is ParseDecimal for values fixed in source code and in
// tests. It panics on a malformed input, which is why nothing that handles user
// input may call it.
func MustParseDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(fmt.Sprintf("money.MustParseDecimal(%q): %v", s, err))
	}
	return d
}

// String renders the canonical decimal representation, preserving the scale.
//
//	"842.20670000" -> "842.20670000"
//	"007.50"       -> "7.50"
//	"-0.00"        -> "0.00"
//
// ParseDecimal(d.String()) equals d for every Decimal, which is what lets the
// stored TEXT and the published value be compared as strings.
func (d Decimal) String() string {
	digits := "0"
	sign := ""
	if d.unscaled != nil {
		digits = d.unscaled.String()
		if rest, found := strings.CutPrefix(digits, "-"); found {
			sign, digits = "-", rest
		}
	}

	if d.scale <= 0 {
		return sign + digits
	}

	scale := int(d.scale)
	if len(digits) <= scale {
		// 5 at scale 2 needs a leading "0" as well as the padding: "005".
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	split := len(digits) - scale
	return sign + digits[:split] + "." + digits[split:]
}

// Scale is the number of decimal places this value carries.
func (d Decimal) Scale() int32 { return d.scale }

// Sign reports -1, 0 or +1.
func (d Decimal) Sign() int {
	if d.unscaled == nil {
		return 0
	}
	return d.unscaled.Sign()
}

// IsPositive reports whether the value is strictly greater than zero, which is
// the condition every exchange rate must satisfy.
func (d Decimal) IsPositive() bool { return d.Sign() > 0 }

// Unscaled returns a COPY of the unscaled integer.
//
// It allocates deliberately. Returning the field would hand a caller a mutable
// reference into a value type, and one big.Int method called on it would
// silently rewrite every copy of this Decimal in the program.
func (d Decimal) Unscaled() *big.Int {
	if d.unscaled == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.unscaled)
}

// Rat returns the exact value as a NEW *big.Rat.
//
// This is the only bridge into arithmetic, and it is exact: from here
// multiplication, division and inversion are rational operations that never
// round. The returned value is freshly allocated, so the caller may mutate it
// freely - including with the in-place math/big API - without touching this
// Decimal.
func (d Decimal) Rat() *big.Rat {
	return new(big.Rat).SetFrac(d.Unscaled(), pow10(int(d.scale)))
}

// Cmp compares two values NUMERICALLY, ignoring scale: 842.20 and 842.200 are
// equal, which string comparison would deny.
func (d Decimal) Cmp(o Decimal) int {
	if d.scale == o.scale {
		return d.Unscaled().Cmp(o.Unscaled())
	}
	// Raise the lower-scaled value to the higher scale rather than dividing, so
	// the comparison stays in integers and stays exact.
	left, right := d.Unscaled(), o.Unscaled()
	if d.scale < o.scale {
		left.Mul(left, pow10(int(o.scale-d.scale)))
	} else {
		right.Mul(right, pow10(int(d.scale-o.scale)))
	}
	return left.Cmp(right)
}

// Equal reports numeric equality. It is Cmp == 0, spelled so that call sites
// read as the question they are asking.
func (d Decimal) Equal(o Decimal) bool { return d.Cmp(o) == 0 }

// ConvertMinor converts an amount expressed in one currency's minor units into
// another currency's minor units, at an exact rate.
//
//	minor_to = amountMinor * rate * 10^(toMinorUnit - fromMinorUnit)
//
// rate is oriented as the whole application orients every rate:
// 1 <from currency> = rate <to currency>. See docs/m5-contract.md.
//
// The entire computation is performed on big integers, so it is exact until the
// single, final rounding into the destination's minor units. That rounding is
// ROUND HALF TO EVEN, and it is the only rounding that happens anywhere in a
// valuation.
//
// Half to even rather than half away from zero because this is a valuation, not
// a payment: nothing changes hands, the same balances are revalued many times a
// day, and half-up would bias every one of those revaluations upward. It is also
// symmetric about zero, so a negative balance - which this application allows -
// rounds by the same rule as a positive one rather than by its mirror image.
//
// rate must be non-nil and strictly positive: a zero or negative exchange rate
// is not a rate, and accepting one would silently annihilate or invert a
// balance.
func ConvertMinor(amountMinor int64, fromMinorUnit, toMinorUnit int, rate *big.Rat) (int64, error) {
	if fromMinorUnit < MinMinorUnit || fromMinorUnit > MaxMinorUnit {
		return 0, fmt.Errorf("%w: source minor unit %d is outside the supported range %d-%d",
			ErrInvalidAmount, fromMinorUnit, MinMinorUnit, MaxMinorUnit)
	}
	if toMinorUnit < MinMinorUnit || toMinorUnit > MaxMinorUnit {
		return 0, fmt.Errorf("%w: destination minor unit %d is outside the supported range %d-%d",
			ErrInvalidAmount, toMinorUnit, MinMinorUnit, MaxMinorUnit)
	}
	if rate == nil {
		return 0, fmt.Errorf("%w: a rate is required", ErrInvalidDecimal)
	}
	if rate.Sign() <= 0 {
		return 0, fmt.Errorf("%w: a rate must be greater than zero, got %s",
			ErrInvalidDecimal, rate.RatString())
	}

	// Copies, not the Rat's storage: Num and Denom both return pointers into
	// the receiver, and the multiplications below would rewrite the caller's
	// rate in place.
	num := new(big.Int).Set(rate.Num())
	den := new(big.Int).Set(rate.Denom())

	num.Mul(num, big.NewInt(amountMinor))

	// The scale difference is applied to whichever side keeps both integral.
	if shift := toMinorUnit - fromMinorUnit; shift > 0 {
		num.Mul(num, pow10(shift))
	} else if shift < 0 {
		den.Mul(den, pow10(-shift))
	}

	rounded := roundHalfEven(num, den)
	if !rounded.IsInt64() {
		return 0, fmt.Errorf("%w: converting %d minor units produces a value outside int64",
			ErrOverflow, amountMinor)
	}
	return rounded.Int64(), nil
}

// roundHalfEven divides num by den and rounds a tie to the even neighbour.
//
// den must be strictly positive, which every caller guarantees: a big.Rat keeps
// its sign in the numerator and its denominator always positive, and the only
// other factor applied to den here is a power of ten.
//
// It is symmetric about zero by construction. Quo truncates toward zero, so the
// remainder carries the sign of the numerator, and the adjustment is applied in
// that same direction - which makes -6172.5 round to -6172 exactly as 6172.5
// rounds to 6172.
func roundHalfEven(num, den *big.Int) *big.Int {
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	if remainder.Sign() == 0 {
		return quotient
	}

	twice := new(big.Int).Abs(remainder)
	twice.Lsh(twice, 1) // 2 * |remainder|, without a multiplication

	switch twice.Cmp(den) {
	case -1:
		// Below the halfway point: truncation was already the nearest value.
		return quotient
	case 0:
		// Exactly halfway. Keep the quotient if it is already even, otherwise
		// step one away from zero to reach the even neighbour.
		if quotient.Bit(0) == 0 {
			return quotient
		}
	}
	return quotient.Add(quotient, big.NewInt(int64(num.Sign())))
}

// pow10 returns 10^n as a new big.Int. n is never negative at any call site:
// every caller has already decided which side of a fraction the shift belongs
// on.
func pow10(n int) *big.Int {
	if n <= 0 {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// NewDecimalFromRat renders an exact rational as a Decimal.
//
// It exists because some numbers this application must SHOW are computed rather
// than parsed. The rate an exchange actually happened at is one: it is the
// source amount divided by the destination amount, an exact fraction that no
// input string ever stated. Dividing exactly is what big.Rat is for; turning
// the result into something a person can read is what this does.
//
//	85000.00 VES / 100.00 USD  ->  "850",       exact
//	84220.67 VES / 100.00 USD  ->  "842.2067",  exact
//	    100 VES /      3 USD   ->  "33.333333333333", NOT exact
//
// The scale is the SMALLEST that represents the value exactly, so a rate that
// happens to be a whole number reads as one rather than as "850.000000000000".
// A value that cannot be written in maxScale decimal places is rounded there,
// HALF TO EVEN -- the same rule and the same code as every other rounding in
// this package -- and exact reports false so that a caller can say so rather
// than presenting an approximation as a fact.
//
// maxScale must be between 0 and MaxScale. r must be non-nil.
func NewDecimalFromRat(r *big.Rat, maxScale int32) (_ Decimal, exact bool, err error) {
	if r == nil {
		return Decimal{}, false, fmt.Errorf("%w: a value is required", ErrInvalidDecimal)
	}
	if maxScale < 0 || maxScale > MaxScale {
		return Decimal{}, false, fmt.Errorf("%w: scale %d is outside the supported range 0-%d",
			ErrInvalidDecimal, maxScale, MaxScale)
	}

	// Copies, not the Rat's storage: Num and Denom both return pointers into
	// the receiver, and the multiplication below would rewrite the caller's
	// value in place.
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())

	num.Mul(num, pow10(int(maxScale)))

	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	if remainder.Sign() != 0 {
		// Not representable at this scale. roundHalfEven re-divides rather than
		// adjusting the quotient in place, so that the one implementation of the
		// tie rule stays the only one.
		return Decimal{unscaled: roundHalfEven(num, den), scale: maxScale}, false, nil
	}

	// Exact. Drop the trailing zeros the scaling introduced, so that the value
	// carries the scale it needs and no more.
	scale := maxScale
	ten := big.NewInt(10)
	for scale > 0 {
		trimmed, rest := new(big.Int).QuoRem(quotient, ten, new(big.Int))
		if rest.Sign() != 0 {
			break
		}
		quotient = trimmed
		scale--
	}
	return Decimal{unscaled: quotient, scale: scale}, true, nil
}
