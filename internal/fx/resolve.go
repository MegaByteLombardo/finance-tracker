package fx

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// Resolve produces the exact factor that converts an amount in from into an
// amount in to, as it stood on a given date, together with the observations it
// used.
//
// It tries three things, in this order, and stops at the first that works:
//
//	IDENTITY  from == to. Exactly 1, no lookup, no stored row. USD/USD does not
//	          exist in the table and must not.
//	DIRECT or an observation of the pair itself, used as published or
//	INVERTED  mathematically inverted.
//	CROSS     two observations sharing one pivot currency, divided.
//
// Every observation it may use satisfies effective_date <= on. There is no
// exception to that anywhere in this package: a rate published with a Fecha
// Valor in the future has not taken effect, and reaching for it would value
// today with tomorrow's number. That is look-ahead bias, and it would make every
// figure this application produces about the past quietly wrong.
//
// When nothing connects the two currencies it returns ErrRateUnavailable, which
// the valuation layer turns into an explicitly incomplete result. It never
// returns a factor of zero, and it never guesses.
func (s *Service) Resolve(ctx context.Context, from, to string, on time.Time) (Resolution, error) {
	from, to = normalizeCode(from), normalizeCode(to)
	if err := validateCode(from); err != nil {
		return Resolution{}, err
	}
	if err := validateCode(to); err != nil {
		return Resolution{}, err
	}

	if from == to {
		// Identity, stated rather than looked up. A currency is worth exactly
		// one of itself, and a stored row saying so could be edited into saying
		// otherwise.
		return Resolution{
			FromCurrencyCode: from,
			ToCurrencyCode:   to,
			Method:           MethodIdentity,
			rate:             big.NewRat(1, 1),
		}, nil
	}

	repo := NewRepository(s.db)

	if resolution, err := resolveDirect(ctx, repo, from, to, on); err == nil {
		return resolution, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Resolution{}, err
	}

	return resolveCross(ctx, repo, from, to, on)
}

// resolveDirect looks for an observation of the pair itself, in either
// orientation.
//
// Both orientations are considered because the application stores what a source
// published and never its mirror: the BCV publishes USD/VES, so valuing VES in
// USD is that same observation inverted. Storing the inverse instead would mean
// two rows for one fact, and 1/842.2067 does not terminate as a decimal, so the
// stored inverse would be a rounded copy that could disagree with the original.
func resolveDirect(ctx context.Context, repo *Repository, from, to string, on time.Time) (Resolution, error) {
	var candidates []directed

	if published, err := repo.LookupOn(ctx, from, to, on); err == nil {
		candidates = append(candidates, directed{observation: published, inverted: false})
	} else if !errors.Is(err, ErrNotFound) {
		return Resolution{}, err
	}

	if reversed, err := repo.LookupOn(ctx, to, from, on); err == nil {
		candidates = append(candidates, directed{observation: reversed, inverted: true})
	} else if !errors.Is(err, ErrNotFound) {
		return Resolution{}, err
	}

	if len(candidates) == 0 {
		return Resolution{}, fmt.Errorf("%s/%s: %w", from, to, ErrNotFound)
	}

	best := pickBest(candidates)

	method := MethodDirect
	if best.inverted {
		method = MethodInverted
	}
	return Resolution{
		FromCurrencyCode: from,
		ToCurrencyCode:   to,
		Method:           method,
		Legs:             []Leg{{Observation: best.observation, Inverted: best.inverted}},
		rate:             best.factor(),
	}, nil
}

// resolveCross builds a factor from two observations sharing a pivot currency:
//
//	from -> to  =  (from -> pivot) / (to -> pivot)
//
// The BCV quotes everything against VES, so this is what makes a valuation base
// other than VES work at all: with USD/VES and EUR/VES published, EUR in USD is
// 977.87777730 / 842.20670000, and no direct EUR/USD observation is needed.
//
// The chain is ONE pivot long, deliberately. A general shortest-path search over
// currencies would find longer routes, and a figure on a dashboard that was
// arrived at through four hops is one nobody can check; a chain nobody can check
// is worse than a gap that says so.
//
// Nothing derived here is stored. A cross rate is a function of two rows, so a
// persisted copy could end up contradicting both of them.
func resolveCross(ctx context.Context, repo *Repository, from, to string, on time.Time) (Resolution, error) {
	fromLegs, err := applicableByCounterpart(ctx, repo, from, on)
	if err != nil {
		return Resolution{}, err
	}
	toLegs, err := applicableByCounterpart(ctx, repo, to, on)
	if err != nil {
		return Resolution{}, err
	}

	type candidate struct {
		pivot string
		from  directed
		to    directed
	}

	var candidates []candidate
	for pivot, fromLeg := range fromLegs {
		if pivot == from || pivot == to {
			continue
		}
		toLeg, ok := toLegs[pivot]
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{pivot: pivot, from: fromLeg, to: toLeg})
	}

	if len(candidates) == 0 {
		return Resolution{}, fmt.Errorf("%s/%s on %s: %w",
			from, to, on.Format("2006-01-02"), ErrRateUnavailable)
	}

	// Deterministic choice, and one that can be defended out loud: take the
	// pivot whose STALEST leg is the freshest, because a chain is only as
	// current as its oldest link. Ties break on the pivot's code so the same
	// data always produces the same answer.
	sort.Slice(candidates, func(i, j int) bool {
		left := olderOf(candidates[i].from.date(), candidates[i].to.date())
		right := olderOf(candidates[j].from.date(), candidates[j].to.date())
		if !left.Equal(right) {
			return left.After(right)
		}
		return candidates[i].pivot < candidates[j].pivot
	})
	best := candidates[0]

	// (from -> pivot) / (to -> pivot). Exact: big.Rat division never rounds,
	// which is why the arithmetic is rational rather than decimal.
	factor := new(big.Rat).Quo(best.from.factor(), best.to.factor())

	return Resolution{
		FromCurrencyCode: from,
		ToCurrencyCode:   to,
		Method:           MethodCross,
		Legs: []Leg{
			{Observation: best.from.observation, Inverted: best.from.inverted},
			{Observation: best.to.observation, Inverted: best.to.inverted},
		},
		rate: factor,
	}, nil
}

// applicableByCounterpart indexes, for one currency, the best usable observation
// against each other currency, already oriented as code -> counterpart.
func applicableByCounterpart(
	ctx context.Context, repo *Repository, code string, on time.Time,
) (map[string]directed, error) {
	rates, err := repo.ListApplicableFor(ctx, code, on)
	if err != nil {
		return nil, err
	}

	byCounterpart := make(map[string]directed, len(rates))
	for _, rate := range rates {
		var leg directed
		switch code {
		case rate.BaseCurrencyCode:
			leg = directed{observation: rate, inverted: false}
		case rate.QuoteCurrencyCode:
			leg = directed{observation: rate, inverted: true}
		default:
			// Unreachable: the query filters on one of the two columns. Skipped
			// rather than assumed, because silently mis-orienting a rate is the
			// one mistake here that produces a plausible wrong number.
			continue
		}

		counterpart := leg.counterpart()
		if existing, ok := byCounterpart[counterpart]; ok {
			leg = pickBest([]directed{existing, leg})
		}
		byCounterpart[counterpart] = leg
	}
	return byCounterpart, nil
}

// directed is an observation together with which way round it is being read.
type directed struct {
	observation Rate
	inverted    bool
}

// factor is the multiplier this leg contributes, as a NEW *big.Rat.
//
// An inverted leg is 1/value, computed exactly. This is the only place inversion
// happens, and it happens on a rational so that 1/3 stays 1/3 instead of
// becoming 0.333333333333.
func (d directed) factor() *big.Rat {
	factor := d.observation.Value.Rat()
	if d.inverted {
		return factor.Inv(factor)
	}
	return factor
}

func (d directed) date() time.Time { return d.observation.EffectiveDate }

// counterpart is the currency on the far side of this leg.
func (d directed) counterpart() string {
	if d.inverted {
		return d.observation.BaseCurrencyCode
	}
	return d.observation.QuoteCurrencyCode
}

// pickBest chooses between observations that could each answer the same
// question.
//
// The order restates the lookup policy so the two cannot disagree:
//
//  1. the most recent effective date -- the freshest economic fact wins;
//  2. then source precedence, MANUAL over BCV_OFFICIAL, WITHIN that date;
//  3. then the orientation as published, over the inverted reading of the
//     opposite pair, because the published row is the primary evidence;
//  4. then the lower id, so the same data always resolves the same way.
//
// Step 2 being subordinate to step 1 is the load-bearing part. If precedence
// came first, a manual rate typed once would outrank every publication that came
// after it and the user's net worth would silently stop moving.
func pickBest(candidates []directed) directed {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if betterThan(candidate, best) {
			best = candidate
		}
	}
	return best
}

func betterThan(candidate, incumbent directed) bool {
	if !candidate.date().Equal(incumbent.date()) {
		return candidate.date().After(incumbent.date())
	}
	if p, q := candidate.observation.Source.Precedence(), incumbent.observation.Source.Precedence(); p != q {
		return p < q
	}
	if candidate.inverted != incumbent.inverted {
		return !candidate.inverted
	}
	return candidate.observation.ID < incumbent.observation.ID
}

func olderOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
