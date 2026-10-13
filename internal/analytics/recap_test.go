package analytics_test

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

func (w *world) recap(g analytics.Granularity, anchor string) analytics.Recap {
	w.t.Helper()
	req := analytics.RecapRequest{Granularity: g}
	if anchor != "" {
		req.Anchor, req.HasAnchor = mustDate(w.t, anchor), true
	}
	r, err := w.analytics.Recap(w.t.Context(), req)
	if err != nil {
		w.t.Fatalf("Recap(%s, %q): %v", g, anchor, err)
	}
	return r
}

// recapWorld is August and September 2026, with "today" on 2026-10-05. Every
// figure below is worked out by hand:
//
//	                      AUGUST (31 d)   SEPTEMBER (30 d)
//	Salario                    2000.00          2000.00
//	Comida   (USD)              300.00           450.00
//	Comida   (10,000 VES @100)       -           100.00
//	Alquiler                    500.00           500.00
//	Ocio                        100.00                -
//	Viajes                           -            50.00
//	Expenses                    900.00          1100.00
//	Net                        1100.00           900.00
//	Savings rate                55.00%           45.00%
//	Daily average     900/31 =   29.03   1100/30 = 36.67
//
//	Net worth: 0 (Jul 31) -> 2300.00 (Aug 31) -> 3200.00 (Sep 30)
type recapWorld struct {
	*world
	usd, ves                int64
	food, rent, fun, travel int64
}

func newRecapWorld(t *testing.T) *recapWorld {
	t.Helper()
	w := &recapWorld{world: newWorld(t)}
	w.food = w.category("Comida", category.Expense)
	w.rent = w.category("Alquiler", category.Expense)
	w.fun = w.category("Ocio", category.Expense)
	w.travel = w.category("Viajes", category.Expense)
	w.usd = w.open("Mercantil USD", "USD", "1000.00", "2026-08-01")
	w.ves = w.open("Banesco VES", "VES", "20000.00", "2026-08-01")
	w.rate("USD", "VES", "2026-08-01", "100")

	w.income(w.usd, "2000.00", "2026-08-05")
	w.expense(w.usd, w.food, "300.00", "2026-08-10")
	w.expense(w.usd, w.rent, "500.00", "2026-08-11")
	w.expense(w.usd, w.fun, "100.00", "2026-08-12")

	w.income(w.usd, "2000.00", "2026-09-05")
	w.expense(w.usd, w.food, "450.00", "2026-09-10")
	w.expense(w.ves, w.food, "10000.00", "2026-09-12")
	w.expense(w.usd, w.rent, "500.00", "2026-09-11")
	w.expense(w.usd, w.travel, "50.00", "2026-09-20")
	return w
}

func TestTheDefaultRecapIsTheLastClosedMonth(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "")

	if got := formatPeriod(r.Period); got != "2026-09-01 -> 2026-09-30" {
		t.Fatalf("period = %s, want September", got)
	}
	if got := formatPeriod(r.PreviousPeriod); got != "2026-08-01 -> 2026-08-31" {
		t.Errorf("previous = %s, want the whole of August", got)
	}
	if r.InProgress || !r.HasPrevious {
		t.Errorf("in progress %v, has previous %v; want false, true", r.InProgress, r.HasPrevious)
	}
	if !r.HasEarlier || database.FormatDate(r.EarlierAnchor) != "2026-08-01" {
		t.Errorf("earlier = %v %s, want August", r.HasEarlier, database.FormatDate(r.EarlierAnchor))
	}
	if !r.HasLater || database.FormatDate(r.LaterAnchor) != "2026-10-01" {
		t.Errorf("later = %v %s, want October", r.HasLater, database.FormatDate(r.LaterAnchor))
	}
}

func TestRecapNetWorthAndCashFlow(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "2026-09-15")

	if r.Opening.TotalMinor != 230000 || r.Closing.TotalMinor != 320000 {
		t.Errorf("net worth %d -> %d, want 230000 -> 320000", r.Opening.TotalMinor, r.Closing.TotalMinor)
	}
	if !r.Change.Defined || r.Change.AmountMinor != 90000 {
		t.Errorf("change = %+v, want +90000", r.Change)
	}
	if !r.PreviousChange.Defined || r.PreviousChange.AmountMinor != 230000 {
		t.Errorf("previous change = %+v, want +230000", r.PreviousChange)
	}

	if r.CashFlow.ExpenseMinor != 110000 || r.PreviousCashFlow.ExpenseMinor != 90000 {
		t.Errorf("expenses %d vs %d, want 110000 vs 90000", r.CashFlow.ExpenseMinor, r.PreviousCashFlow.ExpenseMinor)
	}
	if !r.NetCashFlowChange.Defined || r.NetCashFlowChange.AmountMinor != -20000 {
		t.Errorf("net cash flow change = %+v, want -20000", r.NetCashFlowChange)
	}
	if !r.SavingsRateChange.Defined || r.SavingsRateChange.BasisPoints != -1000 {
		t.Errorf("savings rate change = %+v, want -1000 bp", r.SavingsRateChange)
	}
	if r.ExpenseDailyAverageMinor != 3667 || r.PreviousExpenseDailyAverageMinor != 2903 {
		t.Errorf("daily averages %d vs %d, want 3667 vs 2903",
			r.ExpenseDailyAverageMinor, r.PreviousExpenseDailyAverageMinor)
	}
	if !r.ExpenseDailyAverageChange.Defined || r.ExpenseDailyAverageChange.AmountMinor != 764 {
		t.Errorf("daily average change = %+v, want +764", r.ExpenseDailyAverageChange)
	}
}

func TestRecapCategoriesSayWhichWayEachMoved(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "2026-09-15")

	type want struct {
		name              string
		current, previous int64
		direction         analytics.Direction
	}
	// Largest movement first, whichever way.
	wants := []want{
		{"Comida", 55000, 30000, analytics.DirectionUp},
		{"Ocio", 0, 10000, analytics.DirectionGone},
		{"Viajes", 5000, 0, analytics.DirectionNew},
		{"Alquiler", 50000, 50000, analytics.DirectionSame},
	}
	if len(r.ExpenseCategories) != len(wants) {
		t.Fatalf("%d expense categories, want %d: %+v", len(r.ExpenseCategories), len(wants), r.ExpenseCategories)
	}
	var shares int64
	for i, wanted := range wants {
		got := r.ExpenseCategories[i]
		if got.CategoryName != wanted.name || got.CurrentMinor != wanted.current ||
			got.PreviousMinor != wanted.previous || got.Direction != wanted.direction {
			t.Errorf("line %d = %s %d/%d %s, want %+v", i, got.CategoryName,
				got.CurrentMinor, got.PreviousMinor, got.Direction, wanted)
		}
		shares += got.Share.BasisPoints
	}
	if shares != 10000 {
		t.Errorf("shares add up to %d, want 10000", shares)
	}
	if len(r.IncomeCategories) != 1 || r.IncomeCategories[0].Direction != analytics.DirectionSame {
		t.Errorf("income categories = %+v, want Salario unchanged", r.IncomeCategories)
	}
}

func TestRecapTopExpensesAreRankedInTheBase(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "2026-09-15")

	wantBase := []int64{50000, 45000, 10000, 5000}
	if len(r.TopExpenses) != len(wantBase) {
		t.Fatalf("%d top expenses, want %d", len(r.TopExpenses), len(wantBase))
	}
	for i, want := range wantBase {
		if r.TopExpenses[i].BaseMinor != want {
			t.Errorf("expense %d valued at %d, want %d", i, r.TopExpenses[i].BaseMinor, want)
		}
	}
	ves := r.TopExpenses[2]
	if ves.CurrencyCode != "VES" || ves.AmountMinor != 1000000 || ves.TransactionID == 0 {
		t.Errorf("the bolívar expense = %+v, want 10,000.00 VES with its id", ves)
	}
	if r.UnvaluedExpenseCount != 0 {
		t.Errorf("%d unvalued expenses, want 0", r.UnvaluedExpenseCount)
	}
}

// TestRecapAccountsIncludeArchived: archiving is visibility, not a financial
// event (TD-064).
func TestRecapAccountsIncludeArchived(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	if _, err := w.accounts.SetActive(t.Context(), w.ves, false); err != nil {
		t.Fatalf("archiving: %v", err)
	}
	r := w.recap(analytics.GranularityMonth, "2026-09-15")

	byID := map[int64]analytics.AccountChange{}
	for _, a := range r.Accounts {
		byID[a.AccountID] = a
	}
	usd, ves := byID[w.usd], byID[w.ves]
	if usd.DeltaMinor != 100000 || !usd.BaseChange.Defined || usd.BaseChange.AmountMinor != 100000 {
		t.Errorf("USD account = %+v, want +1000.00 in both", usd)
	}
	if ves.IsActive || ves.DeltaMinor != -1000000 || ves.BaseChange.AmountMinor != -10000 {
		t.Errorf("VES account = %+v, want archived, -10,000.00 VES, -100.00 USD", ves)
	}
}

func TestRecapIndicators(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "2026-09-15")

	want := map[analytics.IndicatorKey]analytics.Verdict{
		analytics.IndicatorNetWorth:    analytics.VerdictImproved,
		analytics.IndicatorNetCashFlow: analytics.VerdictWorsened,
		analytics.IndicatorExpenses:    analytics.VerdictWorsened,
		analytics.IndicatorSavingsRate: analytics.VerdictWorsened,
	}
	for _, indicator := range r.Indicators {
		if want[indicator.Key] != indicator.Verdict {
			t.Errorf("%s = %s, want %s", indicator.Key, indicator.Verdict, want[indicator.Key])
		}
	}
	if r.Summary != (analytics.IndicatorSummary{Improved: 1, Worsened: 3, Total: 4}) {
		t.Errorf("summary = %+v", r.Summary)
	}
}

// TestTheFirstPeriodHasNothingToCompareWith: August holds the first movement.
func TestTheFirstPeriodHasNothingToCompareWith(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityMonth, "2026-08-20")

	if r.HasPrevious || r.HasEarlier {
		t.Errorf("has previous %v, has earlier %v; want neither", r.HasPrevious, r.HasEarlier)
	}
	if r.NetCashFlowChange.Reason != analytics.ReasonNoPreviousPeriod {
		t.Errorf("net cash flow change = %+v, want NO_PREVIOUS_PERIOD", r.NetCashFlowChange)
	}
	// The net worth still moved: from nothing to 2300.00.
	if !r.Change.Defined || r.Change.AmountMinor != 230000 || r.Change.Percent.Defined {
		t.Errorf("change = %+v, want +230000 with no percentage over zero", r.Change)
	}
	for _, c := range r.ExpenseCategories {
		if c.Direction != analytics.DirectionUnknown {
			t.Errorf("%s has direction %s with no previous period", c.CategoryName, c.Direction)
		}
	}
	if r.Summary.Unavailable != 3 || r.Summary.Improved != 1 {
		t.Errorf("summary = %+v, want 1 improved and 3 unavailable", r.Summary)
	}
}

func TestARecapAnchorIsClampedToWhatExists(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)

	future := w.recap(analytics.GranularityMonth, "2027-06-01")
	if got := formatPeriod(future.Period); got != "2026-10-01 -> 2026-10-05" {
		t.Errorf("a future anchor gave %s, want the month in progress", got)
	}
	if !future.InProgress || future.HasLater {
		t.Errorf("in progress %v, has later %v; want true, false", future.InProgress, future.HasLater)
	}
	if got := formatPeriod(future.PreviousPeriod); got != "2026-09-01 -> 2026-09-05" {
		t.Errorf("previous = %s, want the same five days of September", got)
	}

	past := w.recap(analytics.GranularityMonth, "2020-01-01")
	if got := formatPeriod(past.Period); got != "2026-08-01 -> 2026-08-31" {
		t.Errorf("an anchor before everything gave %s, want August", got)
	}
}

func TestTheDefaultWeeklyRecap(t *testing.T) {
	t.Parallel()

	w := newRecapWorld(t)
	r := w.recap(analytics.GranularityWeek, "")
	if got := formatPeriod(r.Period); got != "2026-09-28 -> 2026-10-04" {
		t.Errorf("period = %s, want the week before 2026-10-05", got)
	}
	if got := formatPeriod(r.PreviousPeriod); got != "2026-09-21 -> 2026-09-27" {
		t.Errorf("previous = %s", got)
	}
}

// TestAMissingRateIsReportedNeverZero is TD-067 in a recap: no rate for the
// bolívar, so nothing that depends on one is stated.
func TestAMissingRateIsReportedNeverZero(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "1000.00", "2026-08-01")
	ves := w.open("Banesco VES", "VES", "20000.00", "2026-08-01")
	w.expense(usd, food, "10.00", "2026-08-10")
	w.expense(usd, food, "20.00", "2026-09-10")
	w.expense(ves, food, "500.00", "2026-09-11")

	r := w.recap(analytics.GranularityMonth, "2026-09-15")
	if r.Change.Defined || r.Change.Reason != analytics.ReasonIncomplete {
		t.Errorf("change = %+v, want INCOMPLETE", r.Change)
	}
	if r.ExpenseDailyAverageChange.Defined {
		t.Errorf("daily average change = %+v, want undefined", r.ExpenseDailyAverageChange)
	}
	if r.UnvaluedExpenseCount != 1 || len(r.TopExpenses) != 1 || r.TopExpenses[0].BaseMinor != 2000 {
		t.Errorf("top %+v with %d unvalued, want the 20.00 expense and 1 unvalued", r.TopExpenses, r.UnvaluedExpenseCount)
	}
	for _, c := range r.ExpenseCategories {
		if c.Direction != analytics.DirectionUnknown {
			t.Errorf("%s has direction %s with an unvalued expense", c.CategoryName, c.Direction)
		}
	}
	for _, indicator := range r.Indicators {
		if indicator.Verdict != analytics.VerdictUnavailable {
			t.Errorf("%s = %s, want UNAVAILABLE", indicator.Key, indicator.Verdict)
		}
	}
}

func TestAnUnknownGranularityIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	if _, err := w.analytics.Recap(t.Context(), analytics.RecapRequest{Granularity: "YEAR"}); err == nil {
		t.Error("YEAR was accepted")
	}
}
