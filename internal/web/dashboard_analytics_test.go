package web

import (
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// sampleAnalytics is a dashboard as the backend would compute it: every figure
// already formatted. The handler and the templates must only show these.
func sampleAnalytics() DashboardAnalytics {
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	points := make([]NetWorthPoint, 0, 30)
	for i := 0; i < 30; i++ {
		points = append(points, NetWorthPoint{
			Date: from.AddDate(0, 0, i), TotalMinor: int64(1_161_012 + i*2800),
			TotalFormatted: "11610.12", Complete: i < 5 || i > 7, // three gap days
		})
	}

	return DashboardAnalytics{
		Preset:       DashboardPreset30D,
		Today:        to,
		PeriodFrom:   from,
		PeriodTo:     to,
		HasPrevious:  true,
		PreviousFrom: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
		PreviousTo:   time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		NetWorth: NetWorth{
			BaseCurrencyCode: "USD", BaseMinorUnit: 2,
			TotalMinor: 1_245_032, TotalFormatted: "12450.32", Complete: true,
			Components: []NetWorthComponent{{
				CurrencyCode: "USD", BalanceFormatted: "12450.32", ConvertedFormatted: "12450.32",
				Valued: true, Share: Ratio{BasisPoints: 10000, Defined: true, Formatted: "100.00"},
			}},
			Accounts: []NetWorthAccount{
				{AccountName: "Mercantil USD", AccountType: "BANK", IsActive: true, CurrencyCode: "USD",
					BalanceMinor: 1_200_032, BalanceFormatted: "12000.32", ConvertedFormatted: "12000.32", Valued: true,
					Share: Ratio{BasisPoints: 9639, Defined: true, Formatted: "96.39"}},
				{AccountName: "Cuenta vieja", AccountType: "CASH", IsActive: false, CurrencyCode: "USD",
					BalanceMinor: 45000, BalanceFormatted: "450.00", ConvertedFormatted: "450.00", Valued: true,
					Share: Ratio{BasisPoints: 361, Defined: true, Formatted: "3.61"}},
			},
			ArchivedWithBalance: 1,
			FutureMovementCount: 2,
		},
		Points:         points,
		IncompleteDays: 3,
		Gaps: []RateGap{{
			CurrencyCode: "VES", BaseCurrencyCode: "USD",
			From: from.AddDate(0, 0, 5), To: from.AddDate(0, 0, 7), Days: 3,
		}},
		Opening: NetWorthPoint{Date: from.AddDate(0, 0, -1), TotalMinor: 1_161_012, TotalFormatted: "11610.12", Complete: true},
		Change: Change{
			Defined: true, AmountMinor: 84020, AmountFormatted: "840.20",
			Percent: Ratio{BasisPoints: 724, Defined: true, Formatted: "7.24"},
		},
		CashFlow: CashFlow{
			From: from, To: to, Complete: true,
			IncomeMinor: 300000, IncomeFormatted: "3000.00",
			ExpenseMinor: 215980, ExpenseFormatted: "2159.80",
			NetMinor: 84020, NetFormatted: "840.20",
			SavingsRate: Ratio{BasisPoints: 2801, Defined: true, Formatted: "28.01"},
		},
		Spending: Spending{
			TotalFormatted: "2159.80", DailyAverageFormatted: "71.99", PreviousTotalFormatted: "1900.00",
			Change: Change{Defined: true, AmountMinor: 25980, AmountFormatted: "259.80",
				Percent: Ratio{BasisPoints: 1367, Defined: true, Formatted: "13.67"}},
			Categories: []CategoryComparison{{
				CategoryName: "Supermercado", CurrentFormatted: "900.00", PreviousFormatted: "800.00",
				Change: Change{Defined: true, AmountMinor: 10000, Percent: Ratio{BasisPoints: 1250, Defined: true, Formatted: "12.50"}},
				Share:  Ratio{BasisPoints: 4167, Defined: true, Formatted: "41.67"},
			}},
		},
	}
}

func analyticsRouter(t *testing.T, d *DashboardAnalytics, err error) (*FakeServices, func(path string) (int, string)) {
	t.Helper()

	fs := NewFakeServices()
	svcs := fs.BuildServices()
	if d != nil {
		fs.Analytics.Set(*d)
	}
	if err != nil {
		fs.Analytics.SetError(err)
	}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), svcs, DefaultConfig())
	return fs, func(path string) (int, string) {
		rec := get(t, router, path)
		// html/template writes "+" as "&#43;"; a browser shows "+". Compare
		// against what the user reads.
		return rec.Code, html.UnescapeString(body(t, rec))
	}
}

func TestTheDashboardShowsTheAnalyticsAsComputed(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	_, fetch := analyticsRouter(t, &d, nil)
	status, page := fetch("/")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}

	for _, phrase := range []string{
		"12,450.32",            // net worth
		"+840.20",              // absolute change, signed by the template
		"+7.24%",               // percentage change
		"últimos 30 días",      // the period, in words
		"3,000.00", "2,159.80", // income and expenses
		"28.01%",                 // savings rate
		"71.99",                  // daily average
		"Supermercado", "41.67%", // top category and its share
		"+12.50%",                        // its change against the previous period
		"Evolución del patrimonio",       // the chart
		"<svg",                           //
		"3 días",                         // the gap note
		"USD/VES",                        // which rate is missing,
		"del 11 sep 2026 al 13 sep 2026", // over which days,
		"/settings/exchange-rates?base=USD&date=2026-09-11&quote=VES#manual-rate", // and the form that fills it
		"2 movimientos futuros", // TD-063
		"archivada con saldo",   // TD-064
		"6 sep 2026",            // the period's dates
		"7 ago 2026",            // and the previous period's
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the dashboard does not show %q", phrase)
		}
	}
}

func TestTheDashboardPeriodSelectorIsServerRenderedAndHtmxEnhanced(t *testing.T) {
	t.Parallel()

	fs, fetch := analyticsRouter(t, nil, nil)

	_, page := fetch("/?period=90D")
	if got := fs.Analytics.Requested; len(got) != 1 || got[0] != DashboardPreset90D {
		t.Fatalf("the handler asked for %v, want [90D]", got)
	}
	if !strings.Contains(page, `href="/?period=90D" class="dash-period is-active"`) {
		t.Error("the 90D button is not marked active")
	}
	for _, attr := range []string{`hx-get="/?period=7D"`, `hx-select="#dashboard-analytics"`, `hx-push-url="true"`, `id="dashboard-analytics"`} {
		if !strings.Contains(page, attr) {
			t.Errorf("the selector is missing %s", attr)
		}
	}

	// An unknown period is the default, not an error page.
	status, _ := fetch("/?period=forever")
	if status != 200 {
		t.Errorf("an unknown period returned %d", status)
	}
	if got := fs.Analytics.Requested[1]; got != DefaultDashboardPreset {
		t.Errorf("an unknown period asked for %q, want the default", got)
	}
}

func TestTheDashboardSaysWhyAFigureIsUnavailable(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	d.Change = Change{Reason: ChangeReasonIncomplete}
	d.CashFlow.SavingsRate = Ratio{}
	d.CashFlow.Complete = false
	d.CashFlow.Missing = []MissingFlow{{CurrencyCode: "VES", ExpenseMinor: 12050, ExpenseFormatted: "120.50"}}
	_, fetch := analyticsRouter(t, &d, nil)

	_, page := fetch("/")
	for _, phrase := range []string{
		"faltan tasas de cambio para alguno de los extremos", // change unavailable, with its reason
		"El flujo de caja está incompleto",
		"120.50 VES de egresos",
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the dashboard does not say %q", phrase)
		}
	}
	if strings.Contains(page, "NaN") || strings.Contains(page, "Inf") {
		t.Error("an undefined figure leaked as NaN or Inf")
	}
}

func TestTheDashboardWithNoIncomeShowsNoSavingsRate(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	d.CashFlow.IncomeMinor, d.CashFlow.IncomeFormatted = 0, "0.00"
	d.CashFlow.SavingsRate = Ratio{}
	_, fetch := analyticsRouter(t, &d, nil)

	_, page := fetch("/")
	if !strings.Contains(page, "(sin ingresos)") {
		t.Error("an undefined savings rate is not explained")
	}
}

func TestTheDashboardSurvivesAnAnalyticsFailure(t *testing.T) {
	t.Parallel()

	_, fetch := analyticsRouter(t, nil, errors.New("boom"))
	status, page := fetch("/")
	if status != 200 || !strings.Contains(page, "No se pudo calcular el patrimonio") {
		t.Errorf("status %d; the failure must be a message on the page, not a 500", status)
	}
	status, page = fetch("/mobile")
	if status != 200 || !strings.Contains(page, "No se pudo calcular el patrimonio") {
		t.Errorf("mobile status %d; the failure must be a message on the page, not a 500", status)
	}
}

// TestAnExchangeIsNotLabelledAnExpenseOnTheDashboard is TD-045 in the
// presentation layer: the recent-activity table used to read "if TRANSFER ...
// else Expense", and an FX_EXCHANGE fell through to "Expense".
func TestAnExchangeIsNotLabelledAnExpenseOnTheDashboard(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := context.Background()
	usd := mustOpen(t, fs, "Mercantil USD", "USD", "100.00")
	ves := mustOpen(t, fs, "Banesco VES", "VES", "85000.00")
	if _, err := fs.RecordExchange(ctx, RecordExchangeInput{
		SourceAccountID: ves, SourceAmount: "85000.00",
		DestinationAccountID: usd, DestinationAmount: "100.00",
		EffectiveDate: "2026-09-10",
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())
	page := body(t, get(t, router, "/"))

	if !strings.Contains(page, `<span class="badge badge-transfer">Exchange</span>`) {
		t.Error("the exchange is not labelled as one")
	}
	if strings.Contains(page, `<span class="badge badge-expense">Expense</span>`) {
		t.Error("an exchange is labelled as an expense")
	}
}

func mustOpen(t *testing.T, fs *FakeServices, name, code, amount string) int64 {
	t.Helper()
	res, err := fs.OpenAccount(context.Background(), OpenAccountInput{
		Account:            CreateAccountInput{Name: name, Type: AccountTypeCash, CurrencyCode: code},
		OpeningBalance:     &amount,
		OpeningBalanceDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("opening %q: %v", name, err)
	}
	return res.Account.ID
}

func TestTheMobileDashboardShowsTheSameAnalytics(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	fs, fetch := analyticsRouter(t, &d, nil)

	status, page := fetch("/mobile?period=7d")
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if got := fs.Analytics.Requested; len(got) != 1 || got[0] != DashboardPreset7D {
		t.Errorf("the mobile handler asked for %v, want [7D]", got)
	}
	for _, phrase := range []string{
		"+840.20", "+7.24%", "Evolución", "<svg", "Flujo de caja",
		"3,000.00", "2,159.80", "28.01%", "Supermercado", "41.67%",
		`hx-select="#mobile-analytics"`, `id="mobile-analytics"`,
		"movimientos futuros",
		"USD/VES: 11 sep 2026 – 13 sep 2026", "date=2026-09-11", // the missing rate, and its form
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the mobile dashboard does not show %q", phrase)
		}
	}
	if strings.Contains(page, "Presupuesto semanal") {
		t.Error("the budget placeholder is back; budgets are not a feature of this application")
	}
}

func TestTheAccountScreensMarkFutureMovementsAndArchivedBalances(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := context.Background()
	id := mustOpen(t, fs, "Efectivo", "USD", "100.00")
	future := fakeToday().AddDate(0, 0, 10).Format("2006-01-02")
	if _, err := fs.RecordIncome(ctx, RecordIncomeInput{
		AccountID: id, CategoryID: firstCategory(t, fs, CategoryTypeIncome), Amount: "50.00",
		PaymentMethodID: 1, EffectiveDate: future,
	}); err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}

	balance, err := fs.GetAccountBalance(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != 10000 || balance.FutureMovementCount != 1 {
		t.Errorf("balance = %d with %d future, want 10000 with 1", balance.BalanceMinor, balance.FutureMovementCount)
	}

	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())
	if page := body(t, get(t, router, "/accounts")); !strings.Contains(page, "1 movimiento futuro") {
		t.Error("the accounts screen does not mark the future movement")
	}
	if page := body(t, get(t, router, "/mobile/cuentas")); !strings.Contains(page, "1 movimiento futuro") {
		t.Error("the mobile accounts screen does not mark the future movement")
	}

	if _, err := (fakeAccountService{fs}).SetActive(ctx, id, false); err != nil {
		t.Fatalf("archiving: %v", err)
	}
	if page := body(t, get(t, router, "/accounts")); !strings.Contains(page, "Archived · con saldo") {
		t.Error("an archived account holding money is not marked")
	}
}

func firstCategory(t *testing.T, fs *FakeServices, kind CategoryType) int64 {
	t.Helper()
	created, err := fakeCategoryService{fs}.Create(context.Background(), CreateCategoryInput{Name: "Salario", Type: kind})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}
	return created.ID
}

// TestAMissingBaselineRateIsListedEvenWithNoHoleInTheChart is the case where
// every day of the period has its rate but the close before it does not: the
// chart has no hole, the change is unavailable, and the page must still say
// which rate to load.
func TestAMissingBaselineRateIsListedEvenWithNoHoleInTheChart(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	for i := range d.Points {
		d.Points[i].Complete = true
	}
	d.IncompleteDays = 0
	d.Opening.Complete = false
	d.Change = Change{Reason: ChangeReasonIncomplete}
	d.Gaps = []RateGap{{
		CurrencyCode: "VES", BaseCurrencyCode: "USD",
		From: d.Opening.Date, To: d.Opening.Date, Days: 1,
	}}

	for _, path := range []string{"/", "/mobile"} {
		_, fetch := analyticsRouter(t, &d, nil)
		_, page := fetch(path)
		for _, phrase := range []string{"USD/VES", "5 sep 2026", "date=2026-09-05"} {
			if !strings.Contains(page, phrase) {
				t.Errorf("%s does not show %q", path, phrase)
			}
		}
	}
}

// TestAnUnavailableSpendingChangeSaysWhichExpensesLackARate: the message is
// about expenses, not the ends of the period, and each unvalued group of
// expenses is listed with the rate that would value it.
func TestAnUnavailableSpendingChangeSaysWhichExpensesLackARate(t *testing.T) {
	t.Parallel()

	d := sampleAnalytics()
	d.Spending.Change = Change{Reason: ChangeReasonIncomplete}
	d.PreviousCashFlow.Missing = []MissingFlow{{
		CurrencyCode: "VES", ExpenseMinor: 15000, ExpenseFormatted: "150.00",
		ExpenseFrom: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
		ExpenseTo:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	}}

	for path, phrases := range map[string][]string{
		"/":       {"hay gastos sin tasa de cambio en su fecha", "150.00 VES de gastos", "del período anterior", "12 ago 2026", "30 ago 2026", "date=2026-08-12"},
		"/mobile": {"hay gastos sin tasa en su fecha", "150.00 VES", "período anterior", "date=2026-08-12"},
	} {
		_, fetch := analyticsRouter(t, &d, nil)
		_, page := fetch(path)
		// The net worth's change is defined here, so this phrase could only
		// come from the spending block.
		if strings.Contains(page, "extremos del período") {
			t.Errorf("%s explains spending with the net worth's message", path)
		}
		for _, phrase := range phrases {
			if !strings.Contains(page, phrase) {
				t.Errorf("%s does not show %q", path, phrase)
			}
		}
	}
}
