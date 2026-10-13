package web

import (
	"errors"
	"html"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func recapDay(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// sampleRecap is September 2026 against August, every figure already
// formatted, as internal/analytics would hand it over.
func sampleRecap() Recap {
	pct := func(bp int64, f string) Ratio { return Ratio{BasisPoints: bp, Defined: true, Formatted: f} }
	change := func(minor int64, f string) Change {
		return Change{Defined: true, AmountMinor: minor, AmountFormatted: f}
	}
	desc := "Supermercado del mes"

	return Recap{
		Granularity: RecapMonth,
		Today:       recapDay("2026-10-05"),
		PeriodFrom:  recapDay("2026-09-01"), PeriodTo: recapDay("2026-09-30"), FullTo: recapDay("2026-09-30"),
		PreviousFrom: recapDay("2026-08-01"), PreviousTo: recapDay("2026-08-31"),
		HasPrevious:   true,
		EarlierAnchor: recapDay("2026-08-01"), HasEarlier: true,
		LaterAnchor: recapDay("2026-10-01"), HasLater: true,

		BaseCurrencyCode: "USD",
		Opening:          NetWorth{BaseCurrencyCode: "USD", TotalFormatted: "2300.00", Complete: true, ValuationDate: recapDay("2026-08-31")},
		Closing:          NetWorth{BaseCurrencyCode: "USD", TotalFormatted: "3200.00", Complete: true, ValuationDate: recapDay("2026-09-30")},
		Change: Change{Defined: true, AmountMinor: 90000, AmountFormatted: "900.00",
			Percent: pct(3913, "39.13")},
		PreviousChange: change(230000, "2300.00"),

		CashFlow: CashFlow{Complete: true, IncomeFormatted: "2000.00", ExpenseFormatted: "1100.00",
			NetMinor: 90000, NetFormatted: "900.00", SavingsRate: pct(4500, "45.00"), TransactionCount: 6},
		PreviousCashFlow: CashFlow{Complete: true, IncomeFormatted: "2000.00", ExpenseFormatted: "900.00",
			NetMinor: 110000, NetFormatted: "1100.00", SavingsRate: pct(5500, "55.00")},
		IncomeChange:      change(0, "0.00"),
		NetCashFlowChange: change(-20000, "-200.00"),
		SavingsRateChange: pct(-1000, "-10.00"),
		ExpenseChange:     change(20000, "200.00"),

		ExpenseDailyAverageFormatted:         "36.67",
		PreviousExpenseDailyAverageFormatted: "29.03",
		ExpenseDailyAverageChange:            change(764, "7.64"),

		ExpenseCategories: []RecapCategory{
			{CategoryName: "Comida", CurrentFormatted: "550.00", PreviousFormatted: "300.00",
				Change: change(25000, "250.00"), Direction: DirectionUp},
			{CategoryName: "Ocio", CurrentFormatted: "0.00", PreviousFormatted: "100.00",
				Change: change(-10000, "-100.00"), Direction: DirectionGone},
			{CategoryName: "Viajes", CurrentFormatted: "50.00", PreviousFormatted: "0.00",
				Change: change(5000, "50.00"), Direction: DirectionNew},
			{CategoryName: "Alquiler", CurrentFormatted: "500.00", PreviousFormatted: "500.00",
				Change: change(0, "0.00"), Direction: DirectionSame},
		},
		IncomeCategories: []RecapCategory{
			{CategoryName: "Salario", CurrentFormatted: "2000.00", PreviousFormatted: "2000.00",
				Change: change(0, "0.00"), Direction: DirectionSame},
		},
		TopExpenses: []RecapExpense{
			{TransactionID: 42, Date: recapDay("2026-09-10"), Description: &desc, CategoryName: "Comida",
				AccountName: "Mercantil USD", CurrencyCode: "USD", AmountFormatted: "450.00", BaseFormatted: "450.00"},
			{TransactionID: 43, Date: recapDay("2026-09-12"), CategoryName: "Comida",
				AccountName: "Banesco VES", CurrencyCode: "VES", AmountFormatted: "10000.00", BaseFormatted: "100.00"},
		},
		UnvaluedExpenseCount: 1,
		Accounts: []RecapAccount{
			{AccountName: "Banesco VES", CurrencyCode: "VES", OpeningFormatted: "20000.00", ClosingFormatted: "10000.00",
				DeltaMinor: -1000000, DeltaFormatted: "-10000.00", BaseChange: change(-10000, "-100.00")},
		},
		Indicators: []Indicator{
			{Key: IndicatorNetWorth, Verdict: VerdictImproved, Change: change(90000, "900.00")},
			{Key: IndicatorNetCashFlow, Verdict: VerdictWorsened, Change: change(-20000, "-200.00")},
			{Key: IndicatorExpenses, Verdict: VerdictWorsened, Change: change(764, "7.64")},
			{Key: IndicatorSavingsRate, Verdict: VerdictWorsened, Points: pct(-1000, "-10.00")},
		},
		Summary: IndicatorSummary{Improved: 1, Worsened: 3, Total: 4},
	}
}

func recapRouter(t *testing.T, r *Recap, err error) (*FakeServices, func(path string) (int, string)) {
	t.Helper()

	fs := NewFakeServices()
	svcs := fs.BuildServices()
	if r != nil {
		fs.Analytics.SetRecap(*r)
	}
	if err != nil {
		fs.Analytics.SetError(err)
	}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), svcs, DefaultConfig())
	return fs, func(path string) (int, string) {
		rec := get(t, router, path)
		return rec.Code, html.UnescapeString(body(t, rec))
	}
}

func TestTheRecapShowsWhatWasComputed(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	_, fetch := recapRouter(t, &r, nil)

	for _, path := range []string{"/recap", "/mobile/recap"} {
		status, page := fetch(path)
		if status != 200 {
			t.Fatalf("%s: status = %d", path, status)
		}
		for _, phrase := range []string{
			"Septiembre 2026",
			"3,200.00",          // closing net worth
			"+900.00",           // its change, signed by the template
			"+39.13%",           //
			"1 de 4",            // the summary
			"Mejoró", "Empeoró", // verdicts
			"-10.00 pts",        // savings rate, in points
			"36.67",             // daily average
			"Comida", "+250.00", // a category that rose
			"Ocio", "-100.00", // one that fell
			"Viajes",                // a new one
			"Supermercado del mes",  // the largest expense
			"/transactions/42/edit", // and its link
			"10,000.00",             // in its own currency
			"Banesco VES",           // an account
		} {
			if !strings.Contains(page, phrase) {
				t.Errorf("%s does not show %q", path, phrase)
			}
		}
		if strings.Contains(page, "NaN") || strings.Contains(page, "Inf") {
			t.Errorf("%s: an undefined figure leaked as NaN or Inf", path)
		}
	}
}

func TestTheRecapSplitsCategoriesByDirection(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	_, fetch := recapRouter(t, &r, nil)
	_, page := fetch("/recap")

	up := page[strings.Index(page, "Gastos que subieron"):strings.Index(page, "Gastos que bajaron")]
	down := page[strings.Index(page, "Gastos que bajaron"):strings.Index(page, "Ingresos por categoría")]
	if !strings.Contains(up, "Comida") || !strings.Contains(up, "Viajes") || strings.Contains(up, "Ocio") {
		t.Errorf("the rising column is wrong: %s", up)
	}
	if !strings.Contains(down, "Ocio") || strings.Contains(down, "Comida") || strings.Contains(down, "Alquiler") {
		t.Errorf("the falling column is wrong: %s", down)
	}
}

func TestTheRecapNavigationIsServerRendered(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	fs, fetch := recapRouter(t, &r, nil)
	_, page := fetch("/recap?period=week&start=2026-09-14")

	got := fs.Analytics.RecapRequests
	if len(got) != 1 || got[0].Granularity != RecapWeek || !got[0].HasAnchor ||
		got[0].Anchor.Format(time.DateOnly) != "2026-09-14" {
		t.Fatalf("the handler asked for %+v", got)
	}
	for _, link := range []string{
		`href="/recap?period=month&start=2026-08-01"`, // earlier
		`href="/recap?period=month&start=2026-10-01"`, // later
		`hx-select="#recap"`,
		`id="recap"`,
	} {
		if !strings.Contains(page, link) {
			t.Errorf("the recap is missing %s", link)
		}
	}
}

func TestARecapInProgressSaysSo(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	r.InProgress, r.HasLater = true, false
	_, fetch := recapRouter(t, &r, nil)

	_, page := fetch("/recap")
	if !strings.Contains(page, "en curso · parcial") {
		t.Error("a period in progress is not marked partial")
	}
	if !strings.Contains(page, "mismos días") {
		t.Error("the like-for-like comparison is not explained")
	}
}

func TestARecapThatCannotBeComputedDegradesToAMessage(t *testing.T) {
	t.Parallel()

	_, fetch := recapRouter(t, nil, errors.New("boom"))
	for _, path := range []string{"/recap", "/mobile/recap"} {
		status, page := fetch(path)
		if status != 200 || !strings.Contains(page, "No se pudo calcular el recap.") {
			t.Errorf("%s: status %d, want 200 with a message", path, status)
		}
	}
}

func TestARecapWithAMissingRateSaysWhich(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	r.Change = Change{Reason: ChangeReasonIncomplete}
	r.Closing.Complete = false
	r.Closing.Missing = []MissingRateView{{FromCurrencyCode: "VES", ToCurrencyCode: "USD", BalanceFormatted: "10000.00"}}
	_, fetch := recapRouter(t, &r, nil)

	_, page := fetch("/recap")
	for _, phrase := range []string{
		"faltan tasas de cambio para alguno de los extremos",
		"USD/VES",
		"/settings/exchange-rates?base=USD&date=2026-09-30&quote=VES#manual-rate",
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the recap does not say %q", phrase)
		}
	}
}

func TestParseRecapRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query     string
		g         RecapGranularity
		hasAnchor bool
	}{
		{"", RecapMonth, false},
		{"period=WEEK", RecapWeek, false},
		{"period=year&start=nonsense", RecapMonth, false},
		{"period=month&start=2026-09-14", RecapMonth, true},
	}
	for _, c := range cases {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		got := ParseRecapRequest(q)
		if got.Granularity != c.g || got.HasAnchor != c.hasAnchor {
			t.Errorf("%q = %+v, want %s with anchor %v", c.query, got, c.g, c.hasAnchor)
		}
	}
}

func TestTheRecapIsInTheNavigation(t *testing.T) {
	t.Parallel()

	_, fetch := recapRouter(t, nil, nil)
	_, page := fetch("/")
	if !strings.Contains(page, `href="/recap"`) {
		t.Error("the dashboard does not link to the recap")
	}
	_, mobile := fetch("/mobile")
	if !strings.Contains(mobile, `href="/mobile/recap"`) {
		t.Error("the mobile Inicio does not link to the recap")
	}
}

func TestEveryRecapCardExplainsItsTerms(t *testing.T) {
	t.Parallel()

	r := sampleRecap()
	_, fetch := recapRouter(t, &r, nil)
	target := regexp.MustCompile(`popovertarget="([^"]+)"`)

	for path, want := range map[string]int{"/recap": 17, "/mobile/recap": 15} {
		_, page := fetch(path)
		keys := target.FindAllStringSubmatch(page, -1)
		if len(keys) != want {
			t.Errorf("%s has %d help keys, want %d", path, len(keys), want)
		}
		for _, k := range keys {
			if n := strings.Count(page, `id="`+k[1]+`"`); n != 1 {
				t.Errorf("%s: help %q has %d plates, want exactly one", path, k[1], n)
			}
		}
		for _, phrase := range []string{
			"Ingresos menos egresos",       // net cash flow
			"flujo neto ÷ ingresos",        // savings rate
			"divididos entre sus días",     // daily average
			"convertida a USD con la tasa", // net worth, in the base currency
		} {
			if !strings.Contains(page, phrase) {
				t.Errorf("%s does not explain %q", path, phrase)
			}
		}
	}
}

func TestAnUnknownHelpKeyRendersNothing(t *testing.T) {
	t.Parallel()

	if h := sampleRecap().Help("nope"); h != (RecapHelp{}) {
		t.Errorf("Help(nope) = %+v, want empty", h)
	}
}
