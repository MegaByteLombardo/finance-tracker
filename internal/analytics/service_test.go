package analytics_test

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

const methodCash int64 = 3

// world is the whole read path over real SQLite, with "today" pinned to
// 2026-10-05 in Caracas.
type world struct {
	t          *testing.T
	accounts   *account.Service
	ledger     *ledger.Service
	categories *category.Service
	rates      *fx.Repository
	analytics  *analytics.Service

	salary int64
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db := dbtest.New(t)
	ledgerService := ledger.NewService(db)
	rateService := fx.NewService(db, nil, slog.New(slog.DiscardHandler))
	valuationService := valuation.NewService(ledgerService, rateService,
		settings.NewService(db), currency.NewService(db))

	// Noon in Caracas: the same calendar date in UTC, so the pin cannot slip.
	now := func() time.Time { return mustDate(t, "2026-10-05").Add(16 * time.Hour) }

	w := &world{
		t:          t,
		accounts:   account.NewService(db),
		ledger:     ledgerService,
		categories: category.NewService(db),
		rates:      fx.NewRepository(db),
		analytics:  analytics.NewService(valuationService, ledgerService, now),
	}
	w.salary = w.category("Salario", category.Income)
	return w
}

func (w *world) category(name string, kind category.Type) int64 {
	w.t.Helper()
	created, err := w.categories.Create(w.t.Context(), category.CreateInput{Name: name, Type: kind})
	if err != nil {
		w.t.Fatalf("creating %q: %v", name, err)
	}
	return created.ID
}

func (w *world) open(name, code, amount, on string) int64 {
	w.t.Helper()
	created, err := w.accounts.Create(w.t.Context(), account.CreateInput{Name: name, Type: account.Cash, CurrencyCode: code})
	if err != nil {
		w.t.Fatalf("creating %q: %v", name, err)
	}
	if amount != "" {
		if _, err := w.ledger.RecordOpeningBalance(w.t.Context(), ledger.RecordOpeningBalanceInput{
			AccountID: created.ID, Amount: amount, EffectiveDate: on,
		}); err != nil {
			w.t.Fatalf("opening %q: %v", name, err)
		}
	}
	return created.ID
}

func (w *world) income(accountID int64, amount, on string) {
	w.t.Helper()
	if _, err := w.ledger.RecordIncome(w.t.Context(), ledger.RecordIncomeInput{
		AccountID: accountID, CategoryID: w.salary, Amount: amount,
		PaymentMethodID: methodCash, EffectiveDate: on,
	}); err != nil {
		w.t.Fatalf("recording income: %v", err)
	}
}

func (w *world) expense(accountID, categoryID int64, amount, on string) {
	w.t.Helper()
	if _, err := w.ledger.RecordExpense(w.t.Context(), ledger.RecordExpenseInput{
		AccountID: accountID, CategoryID: categoryID, Amount: amount,
		PaymentMethodID: methodCash, EffectiveDate: on,
	}); err != nil {
		w.t.Fatalf("recording expense: %v", err)
	}
}

func (w *world) rate(base, quote, on, value string) {
	w.t.Helper()
	fetched := database.Now()
	if _, err := w.rates.Insert(w.t.Context(), fx.Rate{
		BaseCurrencyCode: base, QuoteCurrencyCode: quote,
		EffectiveDate: mustDate(w.t, on), Value: money.MustParseDecimal(value),
		Source: fx.SourceBCVOfficial, IsActive: true,
		FetchedAt: &fetched, CreatedAt: fetched, UpdatedAt: fetched,
	}); err != nil {
		w.t.Fatalf("seeding %s/%s: %v", base, quote, err)
	}
}

func (w *world) dashboard(preset analytics.Preset) analytics.Dashboard {
	w.t.Helper()
	d, err := w.analytics.Dashboard(w.t.Context(), preset)
	if err != nil {
		w.t.Fatalf("Dashboard(%s): %v", preset, err)
	}
	return d
}

// --- empty -------------------------------------------------------------------

func TestAnEmptyApplicationHasAValidDashboard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	for _, preset := range analytics.Presets() {
		d := w.dashboard(preset)

		if d.NetWorth.TotalMinor != 0 || !d.NetWorth.Complete {
			t.Errorf("%s: net worth %+v, want a complete zero", preset, d.NetWorth)
		}
		if len(d.Series.Points) != d.Period.Days() {
			t.Errorf("%s: %d points for %d days", preset, len(d.Series.Points), d.Period.Days())
		}
		if !d.Change.Defined || d.Change.AmountMinor != 0 || d.Change.Percent.Defined {
			t.Errorf("%s: change %+v, want 0 with an undefined percentage", preset, d.Change)
		}
		if d.CashFlow.SavingsRate.Defined {
			t.Errorf("%s: a savings rate exists with no income", preset)
		}
		if d.Spending.TotalMinor != 0 || d.Spending.DailyAverageMinor != 0 || len(d.Spending.Categories) != 0 {
			t.Errorf("%s: spending %+v, want nothing", preset, d.Spending)
		}
	}
}

// --- cash flow -----------------------------------------------------------------

// TestCashFlowCountsIncomeAndExpensesOnly: an opening balance, a transfer and an
// exchange all move money, and none of them is income or an expense.
func TestCashFlowCountsIncomeAndExpensesOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "5000.00", "2026-09-20")
	cash := w.open("Efectivo USD", "USD", "", "")
	ves := w.open("Banesco VES", "VES", "85000.00", "2026-09-20")
	w.rate("USD", "VES", "2026-09-01", "850")

	w.income(usd, "1000.00", "2026-10-01")
	w.expense(usd, food, "200.00", "2026-10-02")
	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: usd, DestinationAccountID: cash, Amount: "300.00", EffectiveDate: "2026-10-03",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}
	if _, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID: ves, SourceAmount: "85000.00",
		DestinationAccountID: usd, DestinationAmount: "100.00", EffectiveDate: "2026-10-04",
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	cf := w.dashboard(analytics.Preset30D).CashFlow
	if cf.IncomeMinor != 100000 || cf.ExpenseMinor != 20000 || cf.NetMinor != 80000 {
		t.Errorf("income %d, expenses %d, net %d; want 100000, 20000, 80000",
			cf.IncomeMinor, cf.ExpenseMinor, cf.NetMinor)
	}
	if !cf.SavingsRate.Defined || cf.SavingsRate.BasisPoints != 8000 {
		t.Errorf("savings rate = %+v, want 80.00%%", cf.SavingsRate)
	}
	if cf.TransactionCount != 2 {
		t.Errorf("%d transactions in the cash flow, want 2", cf.TransactionCount)
	}
}

// TestEachFlowIsValuedAtItsOwnDaysRate is TD-068, with the arithmetic by hand:
//
//	2026-09-28  8,500.00 VES at 850  =  10.00 USD
//	2026-10-02  8,500.00 VES at 425  =  20.00 USD
//	                       FOOD TOTAL =  30.00 USD
//
// At the period-end rate it would have been 40.00.
func TestEachFlowIsValuedAtItsOwnDaysRate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	ves := w.open("Banesco VES", "VES", "100000.00", "2026-09-01")
	w.rate("USD", "VES", "2026-09-01", "850")
	w.rate("USD", "VES", "2026-10-01", "425")

	w.expense(ves, food, "8500.00", "2026-09-28")
	w.expense(ves, food, "8500.00", "2026-10-02")

	cf := w.dashboard(analytics.Preset30D).CashFlow
	if cf.ExpenseMinor != 3000 {
		t.Errorf("expenses = %d, want 3000: each flow at the rate of its own day", cf.ExpenseMinor)
	}
	if len(cf.ExpenseCategories) != 1 || cf.ExpenseCategories[0].AmountMinor != 3000 || cf.ExpenseCategories[0].Count != 2 {
		t.Errorf("categories = %+v", cf.ExpenseCategories)
	}
}

// TestFlowAmountsAreRoundedOncePerCategory: three thirds of a cent, rounded each,
// would be lost; summed exactly, they are one cent.
func TestFlowAmountsAreRoundedOncePerCategory(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	ves := w.open("Banesco VES", "VES", "100.00", "2026-09-01")
	w.rate("USD", "VES", "2026-09-01", "3")

	for _, day := range []string{"2026-10-01", "2026-10-02", "2026-10-03"} {
		w.expense(ves, food, "0.01", day) // 0.01 VES at 3 per dollar: 1/3 of a cent
	}

	cf := w.dashboard(analytics.Preset7D).CashFlow
	if cf.ExpenseMinor != 1 {
		t.Errorf("expenses = %d, want exactly 1 cent: rounded one by one, the thirds would be 0", cf.ExpenseMinor)
	}
}

func TestAMissingRateMakesTheCashFlowIncompleteAndSaysWhat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "100.00", "2026-09-01")
	ves := w.open("Banesco VES", "VES", "1000.00", "2026-09-01")

	w.income(usd, "50.00", "2026-10-01")
	w.expense(ves, food, "120.50", "2026-10-02") // no VES rate exists at all

	cf := w.dashboard(analytics.Preset30D).CashFlow
	if cf.Complete {
		t.Fatal("the cash flow is complete with an unconvertible expense")
	}
	if cf.SavingsRate.Defined {
		t.Error("a savings rate was computed from incomplete figures")
	}
	if cf.IncomeMinor != 5000 || cf.ExpenseMinor != 0 {
		t.Errorf("totals = %d / %d, want what IS known: 5000 / 0", cf.IncomeMinor, cf.ExpenseMinor)
	}
	if len(cf.Missing) != 1 || cf.Missing[0].CurrencyCode != "VES" ||
		cf.Missing[0].ExpenseMinor != 12050 || cf.Missing[0].ExpenseFormatted != "120.50" {
		t.Errorf("missing = %+v, want VES 120.50 of expenses", cf.Missing)
	}
}

func TestSavingsRateWithNoIncomeIsUndefinedNotInfinite(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "100.00", "2026-09-01")
	w.expense(usd, food, "10.00", "2026-10-01")

	cf := w.dashboard(analytics.Preset30D).CashFlow
	if cf.SavingsRate.Defined {
		t.Errorf("savings rate = %+v with zero income, want undefined", cf.SavingsRate)
	}
	if cf.NetMinor != -1000 {
		t.Errorf("net = %d, want -1000", cf.NetMinor)
	}
}

// --- periods and comparison ----------------------------------------------------

// TestBoundariesSplitTheTwoPeriodsExactly: 30D on 2026-10-05 is
// 2026-09-06..2026-10-05, and the previous is 2026-08-07..2026-09-05.
func TestBoundariesSplitTheTwoPeriodsExactly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "10000.00", "2026-08-01")

	w.expense(usd, food, "1.00", "2026-08-06")  // before both periods
	w.expense(usd, food, "2.00", "2026-08-07")  // first day of the previous
	w.expense(usd, food, "4.00", "2026-09-05")  // last day of the previous
	w.expense(usd, food, "8.00", "2026-09-06")  // first day of the current
	w.expense(usd, food, "16.00", "2026-10-05") // today: last day of the current

	d := w.dashboard(analytics.Preset30D)
	if d.CashFlow.ExpenseMinor != 2400 {
		t.Errorf("current expenses = %d, want 2400 (8 + 16)", d.CashFlow.ExpenseMinor)
	}
	if d.PreviousCashFlow.ExpenseMinor != 600 {
		t.Errorf("previous expenses = %d, want 600 (2 + 4)", d.PreviousCashFlow.ExpenseMinor)
	}
	// +18.00 on 6.00 is +300%.
	if !d.Spending.Change.Defined || d.Spending.Change.AmountMinor != 1800 || d.Spending.Change.Percent.BasisPoints != 30000 {
		t.Errorf("spending change = %+v, want +1800 and +300.00%%", d.Spending.Change)
	}
	// 24.00 over 30 days is 0.80 a day.
	if d.Spending.DailyAverageMinor != 80 {
		t.Errorf("daily average = %d, want 80", d.Spending.DailyAverageMinor)
	}
}

// TestNetWorthChangeIsMeasuredFromTheCloseBeforeThePeriod is TD-069:
// NW(to) - NW(from - 1).
func TestNetWorthChangeIsMeasuredFromTheCloseBeforeThePeriod(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	usd := w.open("Mercantil USD", "USD", "1000.00", "2026-08-01")
	w.income(usd, "250.00", "2026-09-28") // the baseline day: before the period
	w.income(usd, "100.00", "2026-09-30") // inside 7D, 2026-09-29..2026-10-05

	d := w.dashboard(analytics.Preset7D)
	if got := database.FormatDate(d.Opening.Date); got != "2026-09-28" {
		t.Errorf("the baseline is %s, want 2026-09-28, the day before the period", got)
	}
	if d.Opening.TotalMinor != 125000 || d.NetWorth.TotalMinor != 135000 {
		t.Errorf("opening %d, today %d; want 125000 and 135000", d.Opening.TotalMinor, d.NetWorth.TotalMinor)
	}
	// +100.00 on 1,250.00 is +8.00%.
	if !d.Change.Defined || d.Change.AmountMinor != 10000 || d.Change.Percent.BasisPoints != 800 {
		t.Errorf("change = %+v, want +10000 and +8.00%%", d.Change)
	}
	// The headline and the last point of the chart are the same figure.
	last := d.Series.Points[len(d.Series.Points)-1]
	if last.TotalMinor != d.NetWorth.TotalMinor || !last.Date.Equal(d.Today) {
		t.Errorf("last point %+v disagrees with the net worth %d", last, d.NetWorth.TotalMinor)
	}
	if len(d.Series.Points) != 7 || !d.Series.Points[0].Date.Equal(d.Period.From) {
		t.Errorf("%d points from %s, want 7 from the period's start", len(d.Series.Points),
			database.FormatDate(d.Series.Points[0].Date))
	}
}

func TestAnIncompleteBaselineMakesTheChangeUnavailable(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.open("Banesco VES", "VES", "85000.00", "2026-08-01")
	w.rate("USD", "VES", "2026-10-01", "850") // nothing before the 1st

	d := w.dashboard(analytics.Preset30D)
	if d.Change.Defined || d.Change.Reason != analytics.ReasonIncomplete {
		t.Errorf("change = %+v, want unavailable because the baseline is incomplete", d.Change)
	}
	if d.Series.IncompleteDays != 25 { // 2026-09-06 .. 2026-09-30
		t.Errorf("IncompleteDays = %d, want 25", d.Series.IncompleteDays)
	}
	if !d.NetWorth.Complete || d.NetWorth.TotalMinor != 10000 {
		t.Errorf("today's net worth = %+v, want a complete 10000", d.NetWorth)
	}
}

func TestAllHasNoComparison(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "100.00", "2026-01-10")
	w.expense(usd, food, "10.00", "2026-05-01")

	d := w.dashboard(analytics.PresetAll)
	if d.HasPrevious {
		t.Error("ALL has a previous period")
	}
	if got := database.FormatDate(d.Period.From); got != "2026-01-10" {
		t.Errorf("ALL starts on %s, want the first movement, 2026-01-10", got)
	}
	if d.Spending.Change.Defined || d.Spending.Change.Reason != analytics.ReasonNoPreviousPeriod {
		t.Errorf("spending change = %+v, want no comparison", d.Spending.Change)
	}
	// From nothing to 90.00: an absolute change, and no percentage of zero.
	if !d.Change.Defined || d.Change.AmountMinor != 9000 || d.Change.Percent.Defined {
		t.Errorf("change = %+v, want +9000 with no percentage", d.Change)
	}
}

func TestTopCategoriesAddUpToTheTotal(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	usd := w.open("Mercantil USD", "USD", "10000.00", "2026-08-01")
	for i := 1; i <= 7; i++ {
		id := w.category(fmt.Sprintf("Gasto %d", i), category.Expense)
		w.expense(usd, id, fmt.Sprintf("%d.00", i*10), "2026-10-01")
		w.expense(usd, id, "1.00", "2026-09-01") // previous period
	}

	s := w.dashboard(analytics.Preset30D).Spending
	if len(s.Categories) != 6 || !s.Categories[5].Other {
		t.Fatalf("%d lines, want the top 5 plus one 'other'", len(s.Categories))
	}
	if s.Categories[0].CategoryName != "Gasto 7" || s.Categories[0].CurrentMinor != 7000 {
		t.Errorf("the largest line is %+v, want Gasto 7 at 7000", s.Categories[0])
	}
	var current, previous, shares int64
	for _, c := range s.Categories {
		current += c.CurrentMinor
		previous += c.PreviousMinor
		shares += c.Share.BasisPoints
	}
	if current != s.TotalMinor || s.TotalMinor != 28000 {
		t.Errorf("lines add up to %d, total %d, want 28000", current, s.TotalMinor)
	}
	if previous != s.PreviousTotalMinor || previous != 700 {
		t.Errorf("previous lines add up to %d, previous total %d, want 700", previous, s.PreviousTotalMinor)
	}
	if shares != money.BasisPointsPerUnit {
		t.Errorf("shares add up to %d, want 10000", shares)
	}
}

// TestTheMonthsCashFlowIsTheMonthToDateByTheDashboardsRules: what the mobile
// movements screen shows under the month's name. Before M6's follow-up it summed
// the last hundred rows in its handler, future-dated ones included.
func TestTheMonthsCashFlowIsTheMonthToDateByTheDashboardsRules(t *testing.T) {
	t.Parallel()

	w := newWorld(t) // today is 2026-10-05
	food := w.category("Comida", category.Expense)
	usd := w.open("Mercantil USD", "USD", "1000.00", "2026-09-01")
	cash := w.open("Efectivo USD", "USD", "", "")
	ves := w.open("Banesco VES", "VES", "100000.00", "2026-09-01")
	w.rate("USD", "VES", "2026-09-01", "500")

	w.income(usd, "300.00", "2026-09-30")         // last month: out
	w.income(usd, "100.00", "2026-10-01")         // the first: in
	w.expense(ves, food, "5000.00", "2026-10-03") // 10.00 USD at its own day's rate
	w.expense(usd, food, "999.00", "2026-10-20")  // future: out
	if _, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: usd, DestinationAccountID: cash, Amount: "50.00", EffectiveDate: "2026-10-04",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}

	cf, err := w.analytics.MonthToDateCashFlow(t.Context())
	if err != nil {
		t.Fatalf("MonthToDateCashFlow: %v", err)
	}
	if got := formatPeriod(cf.Period); got != "2026-10-01 -> 2026-10-05" {
		t.Errorf("period = %s", got)
	}
	if cf.BaseCurrencyCode != "USD" || cf.IncomeMinor != 10000 || cf.ExpenseMinor != 1000 || !cf.Complete {
		t.Errorf("cash flow = %s income %d, expenses %d, complete %v; want USD 10000, 1000, true",
			cf.BaseCurrencyCode, cf.IncomeMinor, cf.ExpenseMinor, cf.Complete)
	}
}
