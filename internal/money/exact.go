package money

import (
	"fmt"
	"math/big"
	"sort"
)

// BasisPointsPerUnit is how many basis points make a whole: 10,000 bp is 100%.
//
// Every percentage this application computes is an integer number of basis
// points. Formatted with Format(bp, 2) it reads as a percentage with two
// decimals -- 724 bp is "7.24" -- and no float is ever involved.
const BasisPointsPerUnit = 10_000

// RoundHalfEven rounds an exact rational to the nearest integer, a tie going to
// the even neighbour -- the one rounding rule this package has, and the same code
// ConvertMinor uses.
//
// It returns ErrOverflow when the result does not fit in int64. r must be
// non-nil.
func RoundHalfEven(r *big.Rat) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("%w: a value is required", ErrInvalidDecimal)
	}
	rounded := roundHalfEven(new(big.Int).Set(r.Num()), new(big.Int).Set(r.Denom()))
	if !rounded.IsInt64() {
		return 0, fmt.Errorf("%w: %s rounds to a value outside int64", ErrOverflow, r.RatString())
	}
	return rounded.Int64(), nil
}

// RatioBasisPoints expresses num / den in basis points, rounded half to even.
//
//	RatioBasisPoints(84020, 1160000)  ->  724, true   (7.24%)
//	RatioBasisPoints(5, 0)            ->    0, false
//
// defined is false when den is zero. That is the only case it covers, and it
// is the reason the function exists: a ratio over nothing has no value, and a
// caller handed a bool cannot accidentally render NaN or Infinity -- there is no
// float here to hold either.
//
// The sign follows num and den as ordinary division does. A caller that wants a
// change relative to a possibly negative starting point passes the absolute
// value of that start as den.
func RatioBasisPoints(num, den int64) (bp int64, defined bool, err error) {
	if den == 0 {
		return 0, false, nil
	}
	ratio := new(big.Rat).SetFrac(
		new(big.Int).Mul(big.NewInt(num), big.NewInt(BasisPointsPerUnit)),
		big.NewInt(den))
	bp, err = RoundHalfEven(ratio)
	if err != nil {
		return 0, false, err
	}
	return bp, true, nil
}

// Allocate distributes an integer target across exact parts so that the
// allocations add up to the target EXACTLY, by the largest-remainder method.
//
// It is what makes a breakdown add up. Three accounts holding bolívares are
// worth, in dollars, three exact fractions; rounding each one independently
// produces lines that can disagree with the rounded total by a cent. Instead,
// each part receives the floor of its exact value, and the units still needed to
// reach the target go one each to the parts with the largest fractional
// remainders. Ties go to the earlier part, so the same input always allocates
// the same way.
//
// The target is expected to be the rounding of the parts' exact sum -- the
// figure the screen shows as the total -- and the allocation then never moves a
// part by a whole unit or more from its exact value. A target further away than
// the number of parts is refused rather than spread unevenly.
func Allocate(target int64, parts []*big.Rat) ([]int64, error) {
	out := make([]int64, len(parts))
	if len(parts) == 0 {
		if target != 0 {
			return nil, fmt.Errorf("%w: cannot allocate %d across no parts", ErrInvalidAmount, target)
		}
		return out, nil
	}

	type remainder struct {
		index int
		frac  *big.Rat
	}
	remainders := make([]remainder, len(parts))
	floorSum := new(big.Int)

	for i, part := range parts {
		if part == nil {
			return nil, fmt.Errorf("%w: part %d is missing", ErrInvalidDecimal, i)
		}
		// Floor toward negative infinity, so the remainder is always in [0, 1)
		// and a negative part is treated by the same rule as a positive one.
		floor := new(big.Int).Div(part.Num(), part.Denom()) // Euclidean: floor for a positive denominator
		if !floor.IsInt64() {
			return nil, fmt.Errorf("%w: part %s is outside int64", ErrOverflow, part.RatString())
		}
		out[i] = floor.Int64()
		floorSum.Add(floorSum, floor)
		remainders[i] = remainder{
			index: i,
			frac:  new(big.Rat).Sub(part, new(big.Rat).SetInt(floor)),
		}
	}

	missing := new(big.Int).Sub(big.NewInt(target), floorSum)
	if missing.Sign() < 0 || missing.Cmp(big.NewInt(int64(len(parts)))) > 0 {
		return nil, fmt.Errorf("%w: target %d is not within one unit per part of the parts' sum",
			ErrInvalidAmount, target)
	}

	sort.SliceStable(remainders, func(i, j int) bool {
		return remainders[i].frac.Cmp(remainders[j].frac) > 0
	})
	for k := int64(0); k < missing.Int64(); k++ {
		out[remainders[k].index]++
	}
	return out, nil
}
