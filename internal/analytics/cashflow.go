package analytics

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// converter turns an amount on a date into the base currency, exactly. It is
// valuation.Snapshot.ConvertToBaseExact, behind a function type so the cash
// flow arithmetic can be read without the loading around it.
type converter func(amountMinor int64, currencyCode string, on time.Time) (*big.Rat, bool, error)

// uncategorised names a flow whose category is missing, which the schema makes
// unreachable for income and expenses and which is shown rather than dropped.
const uncategorised = "Sin categoría"

// topCategories is how many spending categories are named before the rest are
// gathered into one line.
const topCategories = 5

// buildCashFlow totals the flows that fall inside a period.
//
// Each flow is converted at the rate of its own day (TD-068). The exact values
// are summed per category and rounded ONCE per category; the totals are the
// integer sums of those lines, so a screen's lines add up to its totals. A flow
// with no rate on its day is left out of the totals and reported in its own
// currency.
func buildCashFlow(period Period, flows []ledger.Flow, convert converter, baseMinorUnit int) (CashFlow, error) {
	type key struct {
		txType ledger.TransactionType
		id     int64
	}
	type line struct {
		name  string
		exact *big.Rat
		count int
	}

	lines := make(map[key]*line)
	var order []key
	missing := make(map[string]*MissingFlow)
	var missingOrder []string

	result := CashFlow{Period: period, Complete: true}

	for _, flow := range flows {
		if !period.Contains(flow.Date) {
			continue
		}
		// The ledger returns INCOME and EXPENSE only; the switch names both
		// rather than assuming "not income" means expense (TD-045).
		var magnitude int64
		switch flow.Type {
		case ledger.Income:
			magnitude = flow.AmountMinor
		case ledger.Expense:
			magnitude = -flow.AmountMinor
		default:
			return CashFlow{}, fmt.Errorf("a %s transaction reached the cash flow, which accepts income and expenses only",
				flow.Type)
		}
		result.TransactionCount += flow.Count

		exact, ok, err := convert(magnitude, flow.CurrencyCode, flow.Date)
		if err != nil {
			return CashFlow{}, err
		}
		if !ok {
			result.Complete = false
			entry, seen := missing[flow.CurrencyCode]
			if !seen {
				entry = &MissingFlow{CurrencyCode: flow.CurrencyCode, MinorUnit: flow.MinorUnit}
				missing[flow.CurrencyCode] = entry
				missingOrder = append(missingOrder, flow.CurrencyCode)
			}
			if flow.Type == ledger.Income {
				entry.IncomeMinor += magnitude
			} else {
				entry.ExpenseMinor += magnitude
			}
			entry.Count += flow.Count
			continue
		}

		k := key{txType: flow.Type, id: flow.CategoryID}
		entry, seen := lines[k]
		if !seen {
			name := flow.CategoryName
			if name == "" {
				name = uncategorised
			}
			entry = &line{name: name, exact: new(big.Rat)}
			lines[k] = entry
			order = append(order, k)
		}
		entry.exact.Add(entry.exact, exact)
		entry.count += flow.Count
	}

	for _, k := range order {
		entry := lines[k]
		rounded, err := money.RoundHalfEven(entry.exact)
		if err != nil {
			return CashFlow{}, fmt.Errorf("rounding the %s total of %q: %w", k.txType, entry.name, err)
		}
		amount := CategoryAmount{
			CategoryID:      k.id,
			CategoryName:    entry.name,
			AmountMinor:     rounded,
			AmountFormatted: money.Format(rounded, baseMinorUnit),
			Count:           entry.count,
		}
		if k.txType == ledger.Income {
			result.IncomeMinor += rounded
			result.IncomeCategories = append(result.IncomeCategories, amount)
		} else {
			result.ExpenseMinor += rounded
			result.ExpenseCategories = append(result.ExpenseCategories, amount)
		}
	}
	sortCategories(result.IncomeCategories)
	sortCategories(result.ExpenseCategories)

	for _, code := range missingOrder {
		entry := missing[code]
		entry.IncomeFormatted = money.Format(entry.IncomeMinor, entry.MinorUnit)
		entry.ExpenseFormatted = money.Format(entry.ExpenseMinor, entry.MinorUnit)
		result.Missing = append(result.Missing, *entry)
	}

	result.NetMinor = result.IncomeMinor - result.ExpenseMinor
	result.IncomeFormatted = money.Format(result.IncomeMinor, baseMinorUnit)
	result.ExpenseFormatted = money.Format(result.ExpenseMinor, baseMinorUnit)
	result.NetFormatted = money.Format(result.NetMinor, baseMinorUnit)

	// (income - expenses) / income. Undefined, explicitly, when there was no
	// income -- a savings rate over nothing is not 0% and not -infinity -- and
	// when a flow is missing, because a rate over a partial income would be a
	// confident number about the wrong thing.
	if result.Complete {
		bp, defined, err := money.RatioBasisPoints(result.NetMinor, result.IncomeMinor)
		if err != nil {
			return CashFlow{}, fmt.Errorf("computing the savings rate: %w", err)
		}
		result.SavingsRate = Ratio{BasisPoints: bp, Defined: defined}
	}
	return result, nil
}

// sortCategories orders lines largest first, then by name, then by id, so the
// same data always lists the same way.
func sortCategories(lines []CategoryAmount) {
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].AmountMinor != lines[j].AmountMinor {
			return lines[i].AmountMinor > lines[j].AmountMinor
		}
		if lines[i].CategoryName != lines[j].CategoryName {
			return lines[i].CategoryName < lines[j].CategoryName
		}
		return lines[i].CategoryID < lines[j].CategoryID
	})
}

// compare states the change from a previous figure to a current one.
//
// It is undefined when either figure is incomplete (TD-067) or when there is no
// previous period; its percentage is additionally undefined when the previous
// figure is zero. The percentage is relative to the ABSOLUTE previous figure,
// so a change from -100 to -50 reads as +50%, an improvement, rather than as a
// negative percentage of a negative base.
func compare(previous, current int64, bothComplete, hasPrevious bool, minorUnit int) (Change, error) {
	switch {
	case !hasPrevious:
		return Change{Reason: ReasonNoPreviousPeriod}, nil
	case !bothComplete:
		return Change{Reason: ReasonIncomplete}, nil
	}

	delta := current - previous
	base := previous
	if base < 0 {
		base = -base
	}
	bp, defined, err := money.RatioBasisPoints(delta, base)
	if err != nil {
		return Change{}, fmt.Errorf("computing a percentage change: %w", err)
	}
	return Change{
		Defined:         true,
		AmountMinor:     delta,
		AmountFormatted: money.Format(delta, minorUnit),
		Percent:         Ratio{BasisPoints: bp, Defined: defined},
	}, nil
}

// buildSpending looks at the expense side of the current period against the
// previous one.
func buildSpending(current, previous CashFlow, hasPrevious bool, baseMinorUnit int) (Spending, error) {
	result := Spending{
		TotalMinor:     current.ExpenseMinor,
		TotalFormatted: current.ExpenseFormatted,
	}

	average, err := money.RoundHalfEven(big.NewRat(current.ExpenseMinor, int64(current.Period.Days())))
	if err != nil {
		return Spending{}, fmt.Errorf("computing the daily average: %w", err)
	}
	result.DailyAverageMinor = average
	result.DailyAverageFormatted = money.Format(average, baseMinorUnit)

	bothComplete := current.Complete && (!hasPrevious || previous.Complete)
	if hasPrevious {
		result.PreviousTotalMinor = previous.ExpenseMinor
		result.PreviousTotalFormatted = previous.ExpenseFormatted
	}
	if result.Change, err = compare(previous.ExpenseMinor, current.ExpenseMinor,
		bothComplete, hasPrevious, baseMinorUnit); err != nil {
		return Spending{}, err
	}

	before := make(map[int64]int64, len(previous.ExpenseCategories))
	for _, line := range previous.ExpenseCategories {
		before[line.CategoryID] = line.AmountMinor
	}

	var other CategoryComparison
	other.Other = true
	other.CategoryName = "Otras categorías"
	shown := make(map[int64]struct{})

	for i, line := range current.ExpenseCategories {
		if i >= topCategories {
			other.CurrentMinor += line.AmountMinor
			other.PreviousMinor += before[line.CategoryID]
			shown[line.CategoryID] = struct{}{}
			continue
		}
		comparison := CategoryComparison{
			CategoryID:    line.CategoryID,
			CategoryName:  line.CategoryName,
			CurrentMinor:  line.AmountMinor,
			PreviousMinor: before[line.CategoryID],
		}
		shown[line.CategoryID] = struct{}{}
		result.Categories = append(result.Categories, comparison)
	}
	if len(current.ExpenseCategories) > topCategories {
		result.Categories = append(result.Categories, other)
	}

	// A category that was spent on before and not now is not in the current
	// list, so it is not shown -- but its previous amount still belongs to the
	// "other" comparison, or the previous column would not add up.
	if len(result.Categories) > 0 && result.Categories[len(result.Categories)-1].Other {
		for _, line := range previous.ExpenseCategories {
			if _, ok := shown[line.CategoryID]; !ok {
				result.Categories[len(result.Categories)-1].PreviousMinor += line.AmountMinor
			}
		}
	}

	// Shares of the period's spending, allocated so they add up to 100.00%.
	var shares []int64
	if current.Complete && current.ExpenseMinor > 0 {
		parts := make([]*big.Rat, len(result.Categories))
		for i, c := range result.Categories {
			parts[i] = new(big.Rat).SetFrac(
				new(big.Int).Mul(big.NewInt(c.CurrentMinor), big.NewInt(money.BasisPointsPerUnit)),
				big.NewInt(current.ExpenseMinor))
		}
		if shares, err = money.Allocate(money.BasisPointsPerUnit, parts); err != nil {
			return Spending{}, fmt.Errorf("allocating spending shares: %w", err)
		}
	}

	for i := range result.Categories {
		c := &result.Categories[i]
		c.CurrentFormatted = money.Format(c.CurrentMinor, baseMinorUnit)
		c.PreviousFormatted = money.Format(c.PreviousMinor, baseMinorUnit)
		if c.Change, err = compare(c.PreviousMinor, c.CurrentMinor, bothComplete, hasPrevious, baseMinorUnit); err != nil {
			return Spending{}, err
		}
		if shares != nil {
			c.Share = Ratio{BasisPoints: shares[i], Defined: true}
		}
	}
	return result, nil
}
