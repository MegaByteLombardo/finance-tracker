package valuation_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// methodCash is the payment method migration 00008 seeds with id 3.
const methodCash int64 = 3

// history is the harness plus what recording income and expenses needs.
type history struct {
	*harness
	salary int64
	food   int64
}

func newHistory(t *testing.T) *history {
	t.Helper()
	h := newHarness(t)
	categories := category.NewService(h.db)

	salary, err := categories.Create(t.Context(), category.CreateInput{Name: "Salario", Type: category.Income})
	if err != nil {
		t.Fatalf("creating the income category: %v", err)
	}
	food, err := categories.Create(t.Context(), category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("creating the expense category: %v", err)
	}
	return &history{harness: h, salary: salary.ID, food: food.ID}
}

// open creates an account with an opening balance on a given date.
func (h *history) open(t *testing.T, name, code, amount, on string) account.Account {
	t.Helper()
	created, err := h.accounts.Create(t.Context(), account.CreateInput{
		Name: name, Type: account.Cash, CurrencyCode: code,
	})
	if err != nil {
		t.Fatalf("creating %q: %v", name, err)
	}
	if amount != "" {
		if _, err := h.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
			AccountID: created.ID, Amount: amount, EffectiveDate: on,
		}); err != nil {
			t.Fatalf("opening %q: %v", name, err)
		}
	}
	return created
}

func (h *history) income(t *testing.T, accountID int64, amount, on string) {
	t.Helper()
	if _, err := h.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: accountID, CategoryID: h.salary, Amount: amount,
		PaymentMethodID: methodCash, EffectiveDate: on,
	}); err != nil {
		t.Fatalf("recording income: %v", err)
	}
}

func (h *history) expense(t *testing.T, accountID int64, amount, on string) {
	t.Helper()
	if _, err := h.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: accountID, CategoryID: h.food, Amount: amount,
		PaymentMethodID: methodCash, EffectiveDate: on,
	}); err != nil {
		t.Fatalf("recording expense: %v", err)
	}
}

func (h *history) series(t *testing.T, from, to string) valuation.Series {
	t.Helper()
	series, err := h.valuation.GetNetWorthSeries(t.Context(), mustDate(t, from), mustDate(t, to))
	if err != nil {
		t.Fatalf("GetNetWorthSeries(%s, %s): %v", from, to, err)
	}
	return series
}

// totals reads a series as date -> total, with "gap" for an incomplete day, so
// expectations can be written as a table of exact integers.
func totals(series valuation.Series) map[string]int64 {
	out := make(map[string]int64, len(series.Points))
	for _, point := range series.Points {
		if !point.Complete {
			out[database.FormatDate(point.Date)] = gap
			continue
		}
		out[database.FormatDate(point.Date)] = point.TotalMinor
	}
	return out
}

// gap marks an incomplete day. No real total in these tests is this value.
const gap int64 = -1 << 62

func expectTotals(t *testing.T, series valuation.Series, want map[string]int64) {
	t.Helper()
	got := totals(series)
	for day, total := range want {
		if got[day] != total {
			t.Errorf("%s: total = %d, want %d", day, got[day], total)
		}
	}
}

// --- single currency -------------------------------------------------------

func TestSeriesSingleCurrencyFollowsTheLedgerDayByDay(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	cash := h.open(t, "Efectivo", "USD", "100.00", "2026-09-02")
	h.income(t, cash.ID, "50.00", "2026-09-04")
	h.expense(t, cash.ID, "20.00", "2026-09-04")
	h.expense(t, cash.ID, "5.25", "2026-09-06")

	series := h.series(t, "2026-09-01", "2026-09-07")
	if len(series.Points) != 7 {
		t.Fatalf("%d points, want one per day: 7", len(series.Points))
	}
	if series.IncompleteDays != 0 {
		t.Errorf("%d incomplete days in a single-currency ledger", series.IncompleteDays)
	}
	expectTotals(t, series, map[string]int64{
		"2026-09-01": 0,     // before the account had anything
		"2026-09-02": 10000, // the opening balance, on its own date
		"2026-09-03": 10000, // no activity: the balance carries
		"2026-09-04": 13000, // income raises it, expense lowers it
		"2026-09-05": 13000,
		"2026-09-06": 12475,
		"2026-09-07": 12475,
	})
}

// TestSeriesBoundariesIncludeBothEnds: a movement exactly on `from` and one
// exactly on `to` both count, and the balance before `from` is carried in.
func TestSeriesBoundariesIncludeBothEnds(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	cash := h.open(t, "Efectivo", "USD", "100.00", "2026-08-01")
	h.income(t, cash.ID, "1.00", "2026-09-01")
	h.income(t, cash.ID, "2.00", "2026-09-03")

	series := h.series(t, "2026-09-01", "2026-09-03")
	expectTotals(t, series, map[string]int64{
		"2026-09-01": 10100,
		"2026-09-02": 10100,
		"2026-09-03": 10300,
	})
	if first := series.Points[0].Date; database.FormatDate(first) != "2026-09-01" {
		t.Errorf("the first point is %s, want 2026-09-01", database.FormatDate(first))
	}
	if last := series.Points[len(series.Points)-1].Date; database.FormatDate(last) != "2026-09-03" {
		t.Errorf("the last point is %s, want 2026-09-03", database.FormatDate(last))
	}
}

// --- multiple currencies and historical FX --------------------------------

// TestTheSameBolivarsAreWorthWhatTheRateOfEachDaySays: nothing moves in the
// ledger, and the valuation still changes, because the rate did.
//
//	50,000.00 VES at 500 = 100.00 USD
//	50,000.00 VES at 400 = 125.00 USD
func TestTheSameBolivarsAreWorthWhatTheRateOfEachDaySays(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Mercantil USD", "USD", "1000.00", "2026-09-01")
	h.open(t, "Banesco VES", "VES", "50000.00", "2026-09-01")
	h.seedOfficial(t, "USD", "VES", "2026-09-01", "500")
	h.seedOfficial(t, "USD", "VES", "2026-09-04", "400")

	series := h.series(t, "2026-09-01", "2026-09-05")
	expectTotals(t, series, map[string]int64{
		"2026-09-01": 110000,
		"2026-09-02": 110000, // no publication: the 1st's rate holds
		"2026-09-03": 110000,
		"2026-09-04": 112500,
		"2026-09-05": 112500,
	})
}

// TestNoFutureLeakage: a rate that takes effect on the 15th is invisible on the
// 14th, even though it is in the database and the series runs past it.
func TestNoFutureLeakage(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Banesco VES", "VES", "50000.00", "2026-09-01")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")
	h.seedOfficial(t, "USD", "VES", "2026-09-15", "250")

	series := h.series(t, "2026-09-10", "2026-09-16")
	expectTotals(t, series, map[string]int64{
		"2026-09-10": gap,   // before any rate: a gap, never the first rate looking forward
		"2026-09-11": 10000, //
		"2026-09-14": 10000, // the 15th has not taken effect
		"2026-09-15": 20000,
		"2026-09-16": 20000,
	})

	// And a single-date valuation agrees, including when it is asked before
	// the later rate exists at all as far as its own load is concerned.
	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-14"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 10000 {
		t.Errorf("NetWorthAt(2026-09-14) = %d, want 10000", worth.TotalMinor)
	}
}

// TestMissingRateIsAGapWithItsReason is TD-067: an incomplete day says which
// currency and how much, and is never presented as a total.
func TestMissingRateIsAGapWithItsReason(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Mercantil USD", "USD", "100.00", "2026-09-01")
	h.open(t, "Banesco VES", "VES", "8500.00", "2026-09-02")
	h.seedOfficial(t, "USD", "VES", "2026-09-04", "850")

	series := h.series(t, "2026-09-01", "2026-09-04")
	if series.IncompleteDays != 2 {
		t.Errorf("IncompleteDays = %d, want 2 (the 2nd and the 3rd)", series.IncompleteDays)
	}

	byDay := map[string]valuation.Point{}
	for _, point := range series.Points {
		byDay[database.FormatDate(point.Date)] = point
	}

	// The 1st: bolívares are held by an account, but the balance is zero. Zero
	// needs no rate, so the day is complete.
	if p := byDay["2026-09-01"]; !p.Complete || p.TotalMinor != 10000 {
		t.Errorf("2026-09-01 = %+v, want complete at 10000: a zero balance needs no rate", p)
	}
	// The 2nd: 8,500.00 bolívares and no rate.
	p := byDay["2026-09-02"]
	if p.Complete {
		t.Error("2026-09-02 is complete, but nothing values its bolívares")
	}
	if len(p.Missing) != 1 || p.Missing[0].FromCurrencyCode != "VES" ||
		p.Missing[0].BalanceMinor != 850000 || p.Missing[0].ToCurrencyCode != "USD" {
		t.Errorf("2026-09-02 missing = %+v, want VES 850000 into USD", p.Missing)
	}
	// The 4th: the rate arrives.
	if p := byDay["2026-09-04"]; !p.Complete || p.TotalMinor != 11000 {
		t.Errorf("2026-09-04 = %+v, want complete at 11000", p)
	}
}

// --- what is and is not income ---------------------------------------------

func TestATransferDoesNotCreateNetWorth(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	bank := h.open(t, "Mercantil USD", "USD", "1000.00", "2026-09-01")
	cash := h.open(t, "Efectivo USD", "USD", "", "")

	if _, err := h.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: bank.ID, DestinationAccountID: cash.ID,
		Amount: "300.00", EffectiveDate: "2026-09-03",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}

	expectTotals(t, h.series(t, "2026-09-01", "2026-09-04"), map[string]int64{
		"2026-09-02": 100000,
		"2026-09-03": 100000,
		"2026-09-04": 100000,
	})

	// Both accounts moved, in opposite directions.
	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-03"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	lines := map[int64]int64{}
	for _, line := range worth.Accounts {
		lines[line.AccountID] = line.BalanceMinor
	}
	if lines[bank.ID] != 70000 || lines[cash.ID] != 30000 {
		t.Errorf("balances = %v, want bank 70000 and cash 30000", lines)
	}
}

// TestArchivingAfterATransferRewritesNoHistory is the case that decided TD-064.
// Account A held 100, transferred everything to B, and was archived empty. If
// archived accounts dropped out of history, the 100 would vanish before the
// transfer and reappear in B on its date -- a transfer creating net worth.
func TestArchivingAfterATransferRewritesNoHistory(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	a := h.open(t, "Cuenta A", "USD", "100.00", "2026-09-01")
	b := h.open(t, "Cuenta B", "USD", "", "")

	if _, err := h.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: a.ID, DestinationAccountID: b.ID,
		Amount: "100.00", EffectiveDate: "2026-09-05",
	}); err != nil {
		t.Fatalf("RecordTransfer: %v", err)
	}

	before := h.series(t, "2026-09-01", "2026-09-07")
	if _, err := h.accounts.SetActive(t.Context(), a.ID, false); err != nil {
		t.Fatalf("archiving A: %v", err)
	}
	after := h.series(t, "2026-09-01", "2026-09-07")

	for i := range before.Points {
		if before.Points[i].TotalMinor != after.Points[i].TotalMinor {
			t.Errorf("%s: %d before archiving, %d after; archiving must not rewrite history",
				database.FormatDate(before.Points[i].Date), before.Points[i].TotalMinor, after.Points[i].TotalMinor)
		}
		if after.Points[i].TotalMinor != 10000 {
			t.Errorf("%s: total = %d, want 10000 every day", database.FormatDate(after.Points[i].Date), after.Points[i].TotalMinor)
		}
	}

	// A is archived AND empty now, so it is not listed; it has nothing to warn about.
	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-07"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	for _, line := range worth.Accounts {
		if line.AccountID == a.ID {
			t.Errorf("the archived, empty account is listed: %+v", line)
		}
	}
	if worth.ArchivedWithBalance != 0 {
		t.Errorf("ArchivedWithBalance = %d, want 0", worth.ArchivedWithBalance)
	}

	// On the 4th it still held the money, and it is listed with it.
	worth, err = h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-04"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.ArchivedWithBalance != 1 {
		t.Errorf("on the 4th ArchivedWithBalance = %d, want 1", worth.ArchivedWithBalance)
	}
}

// TestAnExchangeMovesValueBetweenCurrencies: at the reference rate the net worth
// does not move on the day of the exchange; the movements change currency.
func TestAnExchangeMovesValueBetweenCurrencies(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	ves := h.open(t, "Banesco VES", "VES", "85000.00", "2026-09-01")
	usd := h.open(t, "Mercantil USD", "USD", "", "")
	h.seedOfficial(t, "USD", "VES", "2026-09-01", "850")

	if _, err := h.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID: ves.ID, SourceAmount: "85000.00",
		DestinationAccountID: usd.ID, DestinationAmount: "100.00",
		EffectiveDate: "2026-09-03",
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	expectTotals(t, h.series(t, "2026-09-01", "2026-09-04"), map[string]int64{
		"2026-09-02": 10000,
		"2026-09-03": 10000,
		"2026-09-04": 10000,
	})
}

// --- the point and the series are one computation --------------------------

func TestEveryPointEqualsTheNetWorthAtItsDate(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	usd := h.open(t, "Mercantil USD", "USD", "1000.00", "2026-09-01")
	ves := h.open(t, "Banesco VES", "VES", "33333.33", "2026-09-02")
	h.open(t, "Efectivo EUR", "EUR", "200.00", "2026-09-03")
	h.seedOfficial(t, "USD", "VES", "2026-09-02", "333.33")
	h.seedOfficial(t, "EUR", "VES", "2026-09-03", "377.77")
	h.seedOfficial(t, "USD", "VES", "2026-09-06", "345.67")
	h.expense(t, usd.ID, "12.34", "2026-09-04")
	h.income(t, ves.ID, "1000.01", "2026-09-05")

	series := h.series(t, "2026-09-01", "2026-09-08")
	for _, point := range series.Points {
		worth, err := h.valuation.GetNetWorthAt(t.Context(), point.Date)
		if err != nil {
			t.Fatalf("GetNetWorthAt(%s): %v", database.FormatDate(point.Date), err)
		}
		if worth.TotalMinor != point.TotalMinor || worth.Complete != point.Complete {
			t.Errorf("%s: series %d (complete %v), point valuation %d (complete %v)",
				database.FormatDate(point.Date), point.TotalMinor, point.Complete,
				worth.TotalMinor, worth.Complete)
		}
	}
}

// TestTheBreakdownAddsUp is TD-070: accounts to their currency, currencies to
// the total, shares to exactly 100.00%.
func TestTheBreakdownAddsUp(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Mercantil USD", "USD", "1000.00", "2026-09-01")
	// Three bolívar accounts whose dollar values are each a third of a cent
	// off a whole number: rounded one by one, they would not add up.
	h.open(t, "Banesco", "VES", "100.00", "2026-09-01")
	h.open(t, "Provincial", "VES", "100.00", "2026-09-01")
	h.open(t, "Venezuela", "VES", "100.00", "2026-09-01")
	h.seedOfficial(t, "USD", "VES", "2026-09-01", "3")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-01"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}

	// 300.00 VES / 3 = 100.00 USD exactly at the currency level; 33.333... per
	// account.
	perCurrency := map[string]int64{}
	for _, line := range worth.Accounts {
		perCurrency[line.CurrencyCode] += line.ConvertedMinor
	}
	var componentSum, componentShares, accountShares int64
	for _, component := range worth.Components {
		componentSum += component.ConvertedMinor
		if perCurrency[component.CurrencyCode] != component.ConvertedMinor {
			t.Errorf("%s: accounts add up to %d, the currency line says %d",
				component.CurrencyCode, perCurrency[component.CurrencyCode], component.ConvertedMinor)
		}
		if !component.Share.Defined {
			t.Errorf("%s: share undefined on a complete, positive total", component.CurrencyCode)
		}
		componentShares += component.Share.BasisPoints
	}
	for _, line := range worth.Accounts {
		accountShares += line.Share.BasisPoints
	}
	if componentSum != worth.TotalMinor || worth.TotalMinor != 110000 {
		t.Errorf("components sum to %d, total %d, want 110000 both", componentSum, worth.TotalMinor)
	}
	if componentShares != money.BasisPointsPerUnit || accountShares != money.BasisPointsPerUnit {
		t.Errorf("shares sum to %d (currencies) and %d (accounts), want 10000 each",
			componentShares, accountShares)
	}
}

func TestSharesAreUndefinedForAnIncompleteTotal(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Mercantil USD", "USD", "100.00", "2026-09-01")
	h.open(t, "Banesco VES", "VES", "100.00", "2026-09-01")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-01"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.Complete {
		t.Fatal("the valuation is complete without a VES rate")
	}
	for _, component := range worth.Components {
		if component.Share.Defined {
			t.Errorf("%s has a share of a partial total", component.CurrencyCode)
		}
	}
}

// TestFutureMovementsAreNotPartOfAValuation is TD-063 at the valuation's level.
func TestFutureMovementsAreNotPartOfAValuation(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	cash := h.open(t, "Efectivo", "USD", "100.00", "2026-09-01")
	h.income(t, cash.ID, "50.00", "2026-09-10")
	h.expense(t, cash.ID, "1.00", "2026-09-11")

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-09"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 10000 || worth.FutureMovementCount != 2 {
		t.Errorf("on the 9th: total %d with %d future movements, want 10000 and 2",
			worth.TotalMinor, worth.FutureMovementCount)
	}
}

// TestAnAccountWithNoActivityKeepsItsBalanceAcrossTheSeries: three months of
// silence, and the balance on the last day is still the opening balance.
func TestAnAccountWithNoActivityKeepsItsBalanceAcrossTheSeries(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	h.open(t, "Caja fuerte", "USD", "777.77", "2026-06-01")

	series := h.series(t, "2026-06-01", "2026-09-01")
	for _, point := range series.Points {
		if point.TotalMinor != 77777 || !point.Complete {
			t.Fatalf("%s: total %d, want 77777", database.FormatDate(point.Date), point.TotalMinor)
		}
	}
}

func TestAnEmptyLedgerIsAFlatCompleteSeries(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	series := h.series(t, "2026-09-01", "2026-09-30")
	if len(series.Points) != 30 || series.IncompleteDays != 0 {
		t.Fatalf("%d points, %d incomplete; want 30 and 0", len(series.Points), series.IncompleteDays)
	}
	for _, point := range series.Points {
		if point.TotalMinor != 0 || point.TotalFormatted != "0.00" {
			t.Errorf("%s: %+v, want 0.00", database.FormatDate(point.Date), point)
		}
	}
}

func TestSeriesRefusesAnInvalidRange(t *testing.T) {
	t.Parallel()

	h := newHistory(t)
	if _, err := h.valuation.GetNetWorthSeries(t.Context(),
		mustDate(t, "2026-09-10"), mustDate(t, "2026-09-01")); !errors.Is(err, valuation.ErrInvalidRange) {
		t.Errorf("a reversed range returned %v, want ErrInvalidRange", err)
	}

	// A snapshot loaded through one date cannot answer for a later one: it
	// never read the rates that take effect after it.
	snapshot, err := h.valuation.Load(t.Context(), mustDate(t, "2026-09-10"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := snapshot.NetWorthAt(mustDate(t, "2026-09-11")); !errors.Is(err, valuation.ErrInvalidRange) {
		t.Errorf("a date past the load returned %v, want ErrInvalidRange", err)
	}
}
