package valuation

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// maxSeriesDays bounds a series. A century of daily points is far beyond any
// ledger this application will hold; the bound exists so that a malformed range
// is refused rather than allocated.
const maxSeriesDays = 36_600

// Snapshot is everything a valuation up to one date needs, read once.
//
// It is the engine of this package (TD-065). Service.Load fills it with a
// constant number of queries; every figure after that -- the net worth on a
// date, a daily series, a flow converted at the rate of its own day -- is
// computed from it in memory, by the same code, so the headline figure, the last
// point of the chart and the cash flow cannot be computed by different rules.
//
// It holds no cache of results and is discarded at the end of the request that
// loaded it. It is not safe for concurrent use only in the sense that nothing
// needs it to be: it is never shared.
type Snapshot struct {
	base       currency.Currency
	currencies map[string]currency.Currency

	// accounts is every account, archived or not, ordered by currency code,
	// then name, then id -- the order the ledger lists them in.
	accounts     []ledger.AccountBalance
	accountIndex map[int64]int

	// held lists, in code order, every currency at least one account is
	// denominated in. A series values exactly these.
	held []string

	// deltas is the whole ledger as daily nets per account, ordered by date.
	deltas []ledger.DailyDelta

	book    *fx.Book
	through time.Time
}

func newSnapshot(
	base currency.Currency,
	currencies map[string]currency.Currency,
	accounts []ledger.AccountBalance,
	deltas []ledger.DailyDelta,
	book *fx.Book,
	through time.Time,
) (*Snapshot, error) {
	s := &Snapshot{
		base:         base,
		currencies:   currencies,
		accounts:     accounts,
		accountIndex: make(map[int64]int, len(accounts)),
		deltas:       deltas,
		book:         book,
		through:      through,
	}

	seen := make(map[string]struct{})
	for i, acct := range accounts {
		if _, ok := currencies[acct.CurrencyCode]; !ok {
			// Unreachable through the schema's foreign key. Reported rather
			// than skipped: an account silently missing from a net worth is
			// exactly the failure this package exists to prevent.
			return nil, fmt.Errorf("account %d is denominated in %q, which is not a currency",
				acct.AccountID, acct.CurrencyCode)
		}
		s.accountIndex[acct.AccountID] = i
		if _, ok := seen[acct.CurrencyCode]; !ok {
			seen[acct.CurrencyCode] = struct{}{}
			s.held = append(s.held, acct.CurrencyCode)
		}
	}
	sort.Strings(s.held)

	for i, delta := range deltas {
		if _, ok := s.accountIndex[delta.AccountID]; !ok {
			return nil, fmt.Errorf("a movement names account %d, which does not exist", delta.AccountID)
		}
		// The order is the repository's contract, and the series walk depends
		// on it. Checked rather than assumed, because a mis-ordered input would
		// produce a plausible wrong chart rather than an error.
		if i > 0 && delta.Date.Before(deltas[i-1].Date) {
			return nil, fmt.Errorf("the ledger's daily deltas are not in date order")
		}
	}
	return s, nil
}

// Base is the valuation base every figure in this snapshot is expressed in.
func (s *Snapshot) Base() currency.Currency { return s.base }

// EarliestActivity is the effective date of the first movement in the ledger,
// future-dated ones included, and false when the ledger is empty.
func (s *Snapshot) EarliestActivity() (time.Time, bool) {
	if len(s.deltas) == 0 {
		return time.Time{}, false
	}
	return s.deltas[0].Date, true
}

// checkDate refuses a date the snapshot cannot answer for: the rates that take
// effect after `through` were never read, so a valuation after it would
// silently use stale ones.
func (s *Snapshot) checkDate(on time.Time) error {
	if on.After(s.through) {
		return fmt.Errorf("%w: %s is after %s, the date this valuation's rates were read for",
			ErrInvalidRange, database.FormatDate(on), database.FormatDate(s.through))
	}
	return nil
}

// resolve finds the factor from a currency into the base on a date. ok is false
// when no rate applies, which is a gap and not an error (TD-067).
func (s *Snapshot) resolve(code string, on time.Time) (_ fx.Resolution, ok bool, _ error) {
	resolution, err := s.book.Resolve(code, s.base.Code, on)
	switch {
	case err == nil:
		return resolution, true, nil
	case errors.Is(err, fx.ErrRateUnavailable):
		return fx.Resolution{}, false, nil
	default:
		return fx.Resolution{}, false, fmt.Errorf("resolving %s to %s on %s: %w",
			code, s.base.Code, database.FormatDate(on), err)
	}
}

// ConvertToBaseExact converts an amount in one currency's minor units into the
// base currency's, at the reference rate in effect on a date, WITHOUT rounding.
//
// It is how a flow is valued at the rate of its own day (TD-068): the caller sums
// these exact values per category and rounds once. ok is false when no rate
// applies on that date.
func (s *Snapshot) ConvertToBaseExact(amountMinor int64, currencyCode string, on time.Time) (_ *big.Rat, ok bool, _ error) {
	if err := s.checkDate(on); err != nil {
		return nil, false, err
	}
	from, known := s.currencies[currencyCode]
	if !known {
		return nil, false, fmt.Errorf("currency %q does not exist", currencyCode)
	}
	if amountMinor == 0 {
		return new(big.Rat), true, nil
	}
	resolution, ok, err := s.resolve(currencyCode, on)
	if err != nil || !ok {
		return nil, false, err
	}
	exact, err := money.ConvertMinorExact(amountMinor, from.MinorUnit, s.base.MinorUnit, resolution.Rate())
	if err != nil {
		return nil, false, fmt.Errorf("converting %s %s into %s: %w",
			money.Format(amountMinor, from.MinorUnit), currencyCode, s.base.Code, err)
	}
	return exact, true, nil
}

// NetWorthAt values everything held at the close of a date, with the breakdown
// by currency and by account.
func (s *Snapshot) NetWorthAt(on time.Time) (NetWorth, error) {
	if err := s.checkDate(on); err != nil {
		return NetWorth{}, err
	}

	result := NetWorth{
		BaseCurrencyCode: s.base.Code,
		BaseCurrencyName: s.base.Name,
		BaseSymbol:       s.base.Symbol,
		BaseMinorUnit:    s.base.MinorUnit,
		ValuationDate:    on,
		Complete:         true,
		Components:       make([]Component, 0, len(s.held)),
		Accounts:         make([]AccountComponent, 0, len(s.accounts)),
	}

	// Balances as of the date: the deltas dated on or before it, which is the
	// ledger's own rule (TD-063). The ones after it are only counted.
	balances := make(map[int64]int64, len(s.accounts))
	for _, delta := range s.deltas {
		if delta.Date.After(on) {
			result.FutureMovementCount += delta.MovementCount
			continue
		}
		balances[delta.AccountID] += delta.AmountMinor
	}

	// An account is listed when it is active or holds something. Archived
	// accounts are valued like any other (TD-064); one that is archived and
	// empty contributes nothing and would only be noise.
	groups := make(map[string][]ledger.AccountBalance)
	var order []string
	for _, acct := range s.accounts {
		balance := balances[acct.AccountID]
		if !acct.IsActive && balance == 0 {
			continue
		}
		if _, ok := groups[acct.CurrencyCode]; !ok {
			order = append(order, acct.CurrencyCode)
		}
		groups[acct.CurrencyCode] = append(groups[acct.CurrencyCode], acct)
	}

	for _, code := range order {
		group := groups[code]
		cur := s.currencies[code]

		var total int64
		for _, acct := range group {
			total += balances[acct.AccountID]
		}

		component := Component{
			CurrencyCode:     cur.Code,
			CurrencyName:     cur.Name,
			CurrencySymbol:   cur.Symbol,
			MinorUnit:        cur.MinorUnit,
			AccountCount:     len(group),
			BalanceMinor:     total,
			BalanceFormatted: money.Format(total, cur.MinorUnit),
		}

		resolution, resolved, err := s.resolve(code, on)
		if err != nil {
			return NetWorth{}, err
		}

		accountLines := make([]AccountComponent, len(group))
		for i, acct := range group {
			balance := balances[acct.AccountID]
			accountLines[i] = AccountComponent{
				AccountID:        acct.AccountID,
				AccountName:      acct.AccountName,
				AccountType:      string(acct.AccountType),
				Nature:           acct.AccountType.Nature(),
				Institution:      acct.Institution,
				IsActive:         acct.IsActive,
				CurrencyCode:     cur.Code,
				CurrencySymbol:   cur.Symbol,
				MinorUnit:        cur.MinorUnit,
				BalanceMinor:     balance,
				BalanceFormatted: money.Format(balance, cur.MinorUnit),
			}
			if !acct.IsActive && balance != 0 {
				result.ArchivedWithBalance++
			}
		}

		switch {
		case resolved:
			converted, err := s.allocate(&component, accountLines, resolution)
			if err != nil {
				return NetWorth{}, err
			}
			component.Valued = true
			component.ConvertedMinor = converted
			component.ConvertedFormatted = money.Format(converted, s.base.MinorUnit)
			component.Resolution = &resolution
			result.TotalMinor += converted

		case total == 0:
			// Zero is zero at any rate, exactly; no rate is needed to say so
			// (TD-067). The accounts are all at zero too, except where they
			// cancel out -- and a currency whose accounts cancel out but that
			// has no rate cannot allocate anything to them honestly, so it is
			// treated as unvalued at the account level only when it must be.
			component.Valued = true
			component.ConvertedFormatted = money.Format(0, s.base.MinorUnit)
			for i := range accountLines {
				if accountLines[i].BalanceMinor == 0 {
					accountLines[i].Valued = true
					accountLines[i].ConvertedFormatted = money.Format(0, s.base.MinorUnit)
				}
			}

		default:
			result.Complete = false
			result.Missing = append(result.Missing, MissingRate{
				FromCurrencyCode: cur.Code,
				ToCurrencyCode:   s.base.Code,
				BalanceMinor:     total,
				BalanceFormatted: component.BalanceFormatted,
			})
		}

		result.Components = append(result.Components, component)
		result.Accounts = append(result.Accounts, accountLines...)
	}

	result.TotalFormatted = money.Format(result.TotalMinor, s.base.MinorUnit)

	// The split by nature names both natures it sums (TD-045): an account whose
	// type has neither would be in the total and in neither line, and that is
	// refused rather than hidden.
	for _, line := range result.Accounts {
		if !line.Valued {
			continue
		}
		switch line.Nature {
		case account.Asset:
			result.AssetsMinor += line.ConvertedMinor
		case account.Liability:
			result.LiabilitiesMinor -= line.ConvertedMinor
		default:
			return NetWorth{}, fmt.Errorf("account %d is of type %s, which is neither an asset nor a liability",
				line.AccountID, line.AccountType)
		}
	}
	result.AssetsFormatted = money.Format(result.AssetsMinor, s.base.MinorUnit)
	result.LiabilitiesFormatted = money.Format(result.LiabilitiesMinor, s.base.MinorUnit)

	if err := assignShares(&result); err != nil {
		return NetWorth{}, err
	}
	return result, nil
}

// allocate converts a currency's total once and distributes the rounded figure
// across its accounts by largest remainder, so the account lines add up to the
// currency line exactly (TD-070). It returns the currency's converted total.
func (s *Snapshot) allocate(component *Component, lines []AccountComponent, resolution fx.Resolution) (int64, error) {
	rate := resolution.Rate()

	exactTotal, err := money.ConvertMinorExact(component.BalanceMinor, component.MinorUnit, s.base.MinorUnit, rate)
	if err != nil {
		return 0, fmt.Errorf("converting %s %s into %s: %w",
			component.BalanceFormatted, component.CurrencyCode, s.base.Code, err)
	}
	converted, err := money.RoundHalfEven(exactTotal)
	if err != nil {
		// Overflow, and nothing else: the rate is positive by construction and
		// both minor units come from the currencies table. A balance this large
		// is a real failure and is reported as one rather than being folded in
		// with "no rate", which would send the user to a form that cannot help.
		return 0, fmt.Errorf("converting %s %s into %s: %w",
			component.BalanceFormatted, component.CurrencyCode, s.base.Code, err)
	}

	parts := make([]*big.Rat, len(lines))
	for i, line := range lines {
		parts[i], err = money.ConvertMinorExact(line.BalanceMinor, line.MinorUnit, s.base.MinorUnit, rate)
		if err != nil {
			return 0, fmt.Errorf("converting the balance of %q: %w", line.AccountName, err)
		}
	}
	allocated, err := money.Allocate(converted, parts)
	if err != nil {
		return 0, fmt.Errorf("allocating %s across its accounts: %w", component.CurrencyCode, err)
	}
	for i := range lines {
		lines[i].Valued = true
		lines[i].ConvertedMinor = allocated[i]
		lines[i].ConvertedFormatted = money.Format(allocated[i], s.base.MinorUnit)
	}
	return converted, nil
}

// assignShares fills in each component's part of the total and each asset
// account's part of the assets, in basis points, when the total is complete
// and each whole is positive (see Share).
func assignShares(worth *NetWorth) error {
	if !worth.Complete {
		return nil
	}

	shares := func(values []int64, whole int64) ([]int64, error) {
		parts := make([]*big.Rat, len(values))
		for i, v := range values {
			parts[i] = new(big.Rat).SetFrac(
				new(big.Int).Mul(big.NewInt(v), big.NewInt(money.BasisPointsPerUnit)),
				big.NewInt(whole))
		}
		return money.Allocate(money.BasisPointsPerUnit, parts)
	}

	if worth.TotalMinor > 0 {
		values := make([]int64, len(worth.Components))
		for i, component := range worth.Components {
			values[i] = component.ConvertedMinor
		}
		componentShares, err := shares(values, worth.TotalMinor)
		if err != nil {
			return fmt.Errorf("computing currency shares: %w", err)
		}
		for i := range worth.Components {
			worth.Components[i].Share = Share{BasisPoints: componentShares[i], Defined: true}
		}
	}

	// An account's share is of what is HELD. A debt is not a part of it: with a
	// card in the denominator, a bank account could hold 130% of a net worth.
	if worth.AssetsMinor > 0 {
		var (
			values  []int64
			indexes []int
		)
		for i, line := range worth.Accounts {
			if line.Nature == account.Asset {
				values = append(values, line.ConvertedMinor)
				indexes = append(indexes, i)
			}
		}
		accountShares, err := shares(values, worth.AssetsMinor)
		if err != nil {
			return fmt.Errorf("computing account shares: %w", err)
		}
		for k, i := range indexes {
			worth.Accounts[i].Share = Share{BasisPoints: accountShares[k], Defined: true}
		}
	}
	return nil
}

// Series values the holdings at the close of every day from one date to another,
// inclusive, in one pass.
//
// It walks the calendar once, adding each day's deltas to running per-currency
// balances, and converts each non-zero balance with the resolution in effect
// that day. A resolution is reused until a day on which some observation takes
// effect, because before then nothing could have changed it. Each day's total is
// the same per-currency, round-once sum NetWorthAt computes, so the point for a
// date and the net worth at that date are equal by construction.
func (s *Snapshot) Series(from, to time.Time) (Series, error) {
	if to.Before(from) {
		return Series{}, fmt.Errorf("%w: the range ends on %s, before it starts on %s",
			ErrInvalidRange, database.FormatDate(to), database.FormatDate(from))
	}
	if err := s.checkDate(to); err != nil {
		return Series{}, err
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > maxSeriesDays {
		return Series{}, fmt.Errorf("%w: %d days is longer than any series this application draws",
			ErrInvalidRange, days)
	}

	series := Series{
		BaseCurrencyCode: s.base.Code,
		BaseCurrencyName: s.base.Name,
		BaseSymbol:       s.base.Symbol,
		BaseMinorUnit:    s.base.MinorUnit,
		From:             from,
		To:               to,
		Points:           make([]Point, 0, days),
	}

	balances := make(map[string]int64, len(s.held))
	next := 0
	apply := func(through time.Time) {
		for next < len(s.deltas) && !s.deltas[next].Date.After(through) {
			delta := s.deltas[next]
			balances[s.accounts[s.accountIndex[delta.AccountID]].CurrencyCode] += delta.AmountMinor
			next++
		}
	}

	type cached struct {
		resolution fx.Resolution
		ok         bool
	}
	cache := make(map[string]cached, len(s.held))

	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		apply(day)
		if s.book.HasObservationsOn(day) {
			clear(cache)
		}

		point := Point{Date: day, Complete: true}
		for _, code := range s.held {
			balance := balances[code]
			if balance == 0 {
				continue
			}
			cur := s.currencies[code]

			entry, hit := cache[code]
			if !hit {
				resolution, ok, err := s.resolve(code, day)
				if err != nil {
					return Series{}, err
				}
				entry = cached{resolution: resolution, ok: ok}
				cache[code] = entry
			}
			if !entry.ok {
				point.Complete = false
				point.Missing = append(point.Missing, MissingRate{
					FromCurrencyCode: code,
					ToCurrencyCode:   s.base.Code,
					BalanceMinor:     balance,
					BalanceFormatted: money.Format(balance, cur.MinorUnit),
				})
				continue
			}

			converted, err := money.ConvertMinor(balance, cur.MinorUnit, s.base.MinorUnit, entry.resolution.Rate())
			if err != nil {
				return Series{}, fmt.Errorf("converting %s %s into %s on %s: %w",
					money.Format(balance, cur.MinorUnit), code, s.base.Code, database.FormatDate(day), err)
			}
			point.TotalMinor += converted
		}

		point.TotalFormatted = money.Format(point.TotalMinor, s.base.MinorUnit)
		if !point.Complete {
			series.IncompleteDays++
		}
		series.Points = append(series.Points, point)
	}
	return series, nil
}
