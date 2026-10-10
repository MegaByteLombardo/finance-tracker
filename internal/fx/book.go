package fx

import (
	"errors"
	"math/big"
	"sort"
	"time"
)

// Book is a set of observations held in memory, and the ONE implementation of
// the resolution rules in this package (TD-066).
//
// Service.Resolve loads the observations that apply to one date and asks a Book.
// The historical net worth loads every observation up to the end of its range
// once, builds one Book, and asks it once per currency per day. The rules -- the
// latest effective_date <= the date, never later; MANUAL over BCV_OFFICIAL
// within one date and never across dates; identity, direct, inverted and one
// pivot -- are therefore the same rules, executed by the same code, whether the
// question is about today or about every day of the last five years.
//
// A Book holds ACTIVE observations only. Archived ones are dropped when it is
// built, which is the same thing the SQL used to do with is_active = 1.
//
// It is immutable after NewBook returns and safe to share.
type Book struct {
	// byPair holds each published pair's observations, oldest effective date
	// first. The key is the pair as published: USD/VES, never its mirror.
	byPair map[pairKey][]Rate

	// byCurrency lists, for each currency, every pair it takes part in on
	// either side. It is what a cross rate searches.
	byCurrency map[string][]pairKey

	// effectiveDays is every effective date any observation has, so a caller
	// walking a calendar can tell when a resolution it already holds might
	// have changed.
	effectiveDays map[string]struct{}
}

type pairKey struct{ base, quote string }

// NewBook indexes observations. Archived observations are ignored; the order of
// the input does not matter.
func NewBook(rates []Rate) *Book {
	b := &Book{
		byPair:        make(map[pairKey][]Rate),
		byCurrency:    make(map[string][]pairKey),
		effectiveDays: make(map[string]struct{}),
	}
	for _, rate := range rates {
		if !rate.IsActive {
			continue
		}
		key := pairKey{base: rate.BaseCurrencyCode, quote: rate.QuoteCurrencyCode}
		if _, known := b.byPair[key]; !known {
			b.byCurrency[key.base] = append(b.byCurrency[key.base], key)
			b.byCurrency[key.quote] = append(b.byCurrency[key.quote], key)
		}
		b.byPair[key] = append(b.byPair[key], rate)
		b.effectiveDays[rate.EffectiveDate.Format(dayLayout)] = struct{}{}
	}
	for key := range b.byPair {
		observations := b.byPair[key]
		sort.SliceStable(observations, func(i, j int) bool {
			return observations[i].EffectiveDate.Before(observations[j].EffectiveDate)
		})
	}
	for code := range b.byCurrency {
		pairs := b.byCurrency[code]
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].base != pairs[j].base {
				return pairs[i].base < pairs[j].base
			}
			return pairs[i].quote < pairs[j].quote
		})
	}
	return b
}

const dayLayout = "2006-01-02"

// HasObservationsOn reports whether any observation takes effect on the given
// date. A resolution made the day before is still the right answer on any day
// for which this is false, because nothing new could have become applicable.
func (b *Book) HasObservationsOn(on time.Time) bool {
	_, ok := b.effectiveDays[on.Format(dayLayout)]
	return ok
}

// Resolve is Service.Resolve over the observations in this Book. See there for
// the rules; they are implemented here and only here.
func (b *Book) Resolve(from, to string, on time.Time) (Resolution, error) {
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

	if resolution, err := b.resolveDirect(from, to, on); err == nil {
		return resolution, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Resolution{}, err
	}
	return b.resolveCross(from, to, on)
}

// lookupOn returns the observation that applies to a pair, AS PUBLISHED, on a
// date: the most recent one whose effective_date is on or before it, and among
// those the one from the preferred source.
//
// The two rules in that order are the whole lookup policy, and both halves
// matter:
//
//   - effective_date <= the valuation date, never = it. A publication holds until
//     the next one, so a Sunday is valued with Friday's rate and no row has to be
//     invented for the weekend.
//
//   - effective_date <= the valuation date, never > it. An observation whose
//     Fecha Valor is still in the future has not taken effect, and using it would
//     value today with tomorrow's rate -- look-ahead bias, which would make every
//     historical figure this application ever produces subtly wrong.
func (b *Book) lookupOn(base, quote string, on time.Time) (Rate, bool) {
	observations := b.byPair[pairKey{base: base, quote: quote}]

	// The first observation dated AFTER the date; everything before it is a
	// candidate, and the latest of those carries the winning date.
	after := sort.Search(len(observations), func(i int) bool {
		return observations[i].EffectiveDate.After(on)
	})
	if after == 0 {
		return Rate{}, false
	}

	latest := observations[after-1].EffectiveDate
	best := directed{observation: observations[after-1]}
	for i := after - 2; i >= 0 && observations[i].EffectiveDate.Equal(latest); i-- {
		if candidate := (directed{observation: observations[i]}); betterThan(candidate, best) {
			best = candidate
		}
	}
	return best.observation, true
}

// applicableFor returns the winning observation, as published, for every pair
// that involves the given currency on the given date, in either orientation.
//
// It is what a cross rate is built from: to convert X into Y through a pivot,
// both halves of the chain have to be found, and neither is known in advance.
func (b *Book) applicableFor(code string, on time.Time) []Rate {
	applicable := make([]Rate, 0, len(b.byCurrency[code]))
	for _, key := range b.byCurrency[code] {
		if rate, ok := b.lookupOn(key.base, key.quote, on); ok {
			applicable = append(applicable, rate)
		}
	}
	return applicable
}
