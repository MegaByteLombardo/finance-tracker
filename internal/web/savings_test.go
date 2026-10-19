package web

import (
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func savingsDay(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// sampleSavings is a policy at 20% with three live suggestions -- open, pending
// a rate, dismissed -- as the adapter would hand them over, formatted.
func sampleSavings() *FakeSavings {
	f := NewFakeSavings()
	start := savingsDay("2026-10-01")
	current := SavingsPolicyVersion{ID: 1, RateFormatted: "20.00", EffectiveFrom: start, SuggestionCount: 3, InForce: true}
	f.Policy = SavingsPolicy{Versions: []SavingsPolicyVersion{current}, Current: &current, Start: &start}
	f.OverviewResult = SavingsOverview{
		PeriodFrom: start, PeriodTo: savingsDay("2026-10-31"), Policy: f.Policy,
		EligibleIncomeFormatted: "1200.00", SuggestedFormatted: "240.00",
		OpenFormatted: "160.00", DismissedFormatted: "80.00",
		OpenCount: 1, DismissedCount: 1, PendingValuationCount: 1,
		PendingByCurrency: []SavingsCurrencyAmount{{CurrencyCode: "VES", AmountFormatted: "32000.00"}},
		RateChangedCount:  1, UnsyncedCount: 2,
	}
	salary := "Quincena octubre"
	f.Suggestions = []SavingsSuggestion{
		{IncomeTransactionID: 11, Revision: 1, RateFormatted: "20.00", IncomeDescription: &salary,
			IncomeAccountName: "Mercantil USD", IncomeCategoryName: "Salario",
			IncomeAmountFormatted: "800.00", IncomeCurrencyCode: "USD", IncomeEffectiveDate: savingsDay("2026-10-05"),
			Valued: true, IncomeUSDFormatted: "800.00", SuggestedUSDFormatted: "160.00",
			Status: "OPEN", StatusLabel: "Pendiente de confirmar", BadgeClass: "badge-income",
			RateChanged: true, Live: true, CanDismiss: true, CanRevalue: true},
		{IncomeTransactionID: 12, Revision: 1, RateFormatted: "20.00",
			IncomeAccountName: "Banesco", IncomeCategoryName: "Salario",
			IncomeAmountFormatted: "32000.00", IncomeCurrencyCode: "VES", IncomeEffectiveDate: savingsDay("2026-10-06"),
			Status: "PENDING_VALUATION", StatusLabel: "Sin valorar", BadgeClass: "badge-warning",
			PendingLabel: "Falta la tasa VES/USD vigente el 2026-10-06", Live: true, CanValue: true},
	}
	return f
}

// zeroUSD matches a standalone "0.00 USD", not the tail of "160.00 USD".
var zeroUSD = regexp.MustCompile(`(^|[^0-9.,])0\.00 USD`)

func savingsRouter(t *testing.T, f *FakeSavings) http.Handler {
	t.Helper()
	fs := NewFakeServices()
	fs.Savings = f
	return NewRouter(slog.New(slog.DiscardHandler), fs.BuildServices(), DefaultConfig())
}

func TestTheSavingsPageShowsWhatWasComputed(t *testing.T) {
	t.Parallel()
	router := savingsRouter(t, sampleSavings())

	for _, path := range []string{"/savings", "/mobile/ahorro"} {
		rec := get(t, router, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		page := html.UnescapeString(body(t, rec))
		for _, want := range []string{
			"20.00", "1,200.00", "240.00", "160.00",
			"Quincena octubre", "Pendiente de confirmar",
			"Falta la tasa VES/USD vigente el 2026-10-06", "32,000.00 VES",
			"/savings/incomes/12/value", "/savings/incomes/11/dismiss", "/savings/incomes/11/revalue",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
		// A pending suggestion has no amount, and is never shown as zero.
		if zeroUSD.MatchString(page) {
			t.Errorf("%s shows a zero amount", path)
		}
		// Only the actions the backend allowed are offered.
		if strings.Contains(page, "/savings/incomes/12/dismiss") || strings.Contains(page, "/savings/incomes/11/value") {
			t.Errorf("%s offers an action the backend did not allow", path)
		}
	}
}

func TestTheSavingsPageInvitesToSetAPolicyWhenThereIsNone(t *testing.T) {
	t.Parallel()
	router := savingsRouter(t, NewFakeSavings())
	page := body(t, get(t, router, "/savings"))
	if !strings.Contains(page, "Todavía no hay una política de ahorro") {
		t.Error("the empty state is missing")
	}
}

func TestASavingsReadFailureIsAMessageNotA500(t *testing.T) {
	t.Parallel()
	f := NewFakeSavings()
	f.Err = errors.New("disk on fire")
	router := savingsRouter(t, f)
	rec := get(t, router, "/savings")
	if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "No se pudo cargar el ahorro.") {
		t.Errorf("status %d; want the page with a message", rec.Code)
	}
}

func TestSettingThePolicyRedirectsAndARefusalSaysWhy(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	router := savingsRouter(t, f)

	rec := post(t, router, "/savings/policy", url.Values{"rate": {"25"}, "effective_from": {"2026-11-01"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/savings/policy?msg=policy-saved" {
		t.Fatalf("status %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(f.PolicySet) != 1 || f.PolicySet[0].Rate != "25" || f.PolicySet[0].EffectiveFrom != "2026-11-01" {
		t.Errorf("SetPolicy got %+v", f.PolicySet)
	}

	f.ActionErr = ErrSavingsRetroactivePolicy
	rec = post(t, router, "/savings/policy", url.Values{
		"rate": {"25"}, "effective_from": {"2026-01-01"}, "return": {"/mobile/ahorro/politica"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a refused policy answered %d, want 422", rec.Code)
	}
	page := html.UnescapeString(body(t, rec))
	if !strings.Contains(page, "Solo la primera versión puede empezar en el pasado") {
		t.Error("the refusal does not say why")
	}
	if !strings.Contains(page, "mobile-viewport-wrapper") {
		t.Error("a refusal from the mobile page was not answered with the mobile page")
	}
}

func TestSuggestionActionsComeBackWithAMessage(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	router := savingsRouter(t, f)

	cases := []struct {
		path, back, want string
	}{
		{"/savings/incomes/12/value", "/savings", "/savings?msg=valued"},
		{"/savings/incomes/11/revalue", "/mobile/ahorro", "/mobile/ahorro?msg=revalued"},
		{"/savings/incomes/11/dismiss", "/savings/incomes/11", "/savings/incomes/11?msg=dismissed"},
	}
	for _, c := range cases {
		rec := post(t, router, c.path, url.Values{"return": {c.back}, "reason": {"No este mes"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.want {
			t.Errorf("%s: %d to %q, want %q", c.path, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
	want := []string{"value:12", "revalue:11", "dismiss:11"}
	if strings.Join(f.Actions, ",") != strings.Join(want, ",") || f.DismissReasons[0] != "No este mes" {
		t.Errorf("actions %v, reasons %v", f.Actions, f.DismissReasons)
	}

	f.ActionErr = ErrSavingsRateUnavailable
	rec := post(t, router, "/savings/incomes/12/value", url.Values{})
	if loc := rec.Header().Get("Location"); loc != "/savings?err=rate-unavailable" {
		t.Errorf("a missing rate redirected to %q", loc)
	}
	page := html.UnescapeString(body(t, get(t, router, "/savings?err=rate-unavailable")))
	if !strings.Contains(page, "Todavía no hay una tasa vigente en la fecha de ese ingreso") {
		t.Error("the page does not explain the missing rate")
	}
}

// The return field only ever leads back to a savings page.
func TestTheReturnFieldCannotRedirectElsewhere(t *testing.T) {
	t.Parallel()
	router := savingsRouter(t, sampleSavings())
	for _, back := range []string{"https://evil.example", "//evil.example", "/accounts", "/savings/../accounts?x", "/savingsX"} {
		rec := post(t, router, "/savings/incomes/12/value", url.Values{"return": {back}})
		if loc := rec.Header().Get("Location"); loc != "/savings?msg=valued" {
			t.Errorf("return %q redirected to %q", back, loc)
		}
	}
}

// The query never supplies words that reach the page.
func TestAnUnknownMessageKeyShowsNothingInvented(t *testing.T) {
	t.Parallel()
	router := savingsRouter(t, sampleSavings())
	page := body(t, get(t, router, "/savings?msg=<script>alert(1)</script>&err="))
	if strings.Contains(page, "<script>alert(1)") {
		t.Error("the query reached the page")
	}
}

func TestTheSyncPreviewAppliesExactlyWhatIsSubmitted(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Plan = SavingsSyncPlan{Items: []SavingsSyncItem{
		{Action: "CREATE", ActionLabel: "Crear sugerencia", TransactionID: 21, Date: savingsDay("2026-09-10"),
			AccountName: "Mercantil USD", CategoryName: "Salario", CurrencyCode: "USD", AmountFormatted: "500.00",
			RateFormatted: "20.00", Reason: "Categoría «Salario», tasa 20.00% vigente desde 2026-09-01"},
		{Action: "INVALIDATE", ActionLabel: "Anular sugerencia", TransactionID: 22, Date: savingsDay("2026-09-12"),
			AccountName: "Mercantil USD", CategoryName: "Reembolsos", CurrencyCode: "USD", AmountFormatted: "40.00",
			Reason: "La categoría «Reembolsos» está excluida del ahorro"},
	}}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/sync", "/mobile/ahorro/sincronizar"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{`value="21"`, `value="22"`, "Crear sugerencia", "Anular sugerencia", "excluida del ahorro"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
	}

	rec := post(t, router, "/savings/sync", url.Values{"transaction_id": {"21"}})
	if rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/savings?msg=synced&created=1&revised=0&invalidated=0" {
		t.Fatalf("status %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(f.Applied) != 1 || len(f.Applied[0]) != 1 || f.Applied[0][0] != 21 {
		t.Errorf("applied %v, want exactly [21]", f.Applied)
	}
	if rec := post(t, router, "/savings/sync", url.Values{"transaction_id": {"abc"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed id answered %d", rec.Code)
	}
}

func TestThePolicyPageShowsHistoryAndEligibility(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.Categories = []SavingsCategoryEligibility{
		{CategoryID: 1, Name: "Salario", IsActive: true, Eligible: true},
		{CategoryID: 2, Name: "Reembolsos", IsActive: true, Eligible: false, Explicit: true},
	}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/policy", "/mobile/ahorro/politica"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{"20.00%", "Salario", "Reembolsos", "/savings/categories/2/eligibility", "por defecto"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
	}

	rec := post(t, router, "/savings/categories/2/eligibility", url.Values{"eligible": {"1"}})
	if rec.Code != http.StatusSeeOther || !f.EligibilitySet[2] {
		t.Errorf("status %d, set %v", rec.Code, f.EligibilitySet)
	}
}

func TestTheIncomePageShowsItsHistoryAndTakesAnOverride(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	reason := "El ingreso cambió: importe"
	f.Details[11] = SavingsIncomeDetail{
		TransactionID: 11, Date: savingsDay("2026-10-05"), AccountName: "Banesco", CategoryName: "Salario",
		CurrencyCode: "VES", AmountFormatted: "40000.00", Eligible: true,
		Reason: "Categoría «Salario», tasa 20.00% vigente desde 2026-10-01",
		Revisions: []SavingsSuggestion{
			{Revision: 1, Status: "SUPERSEDED", StatusLabel: "Reemplazada", StatusReason: &reason, BadgeClass: "badge-neutral",
				RateFormatted: "20.00", IncomeAmountFormatted: "32000.00", IncomeCurrencyCode: "VES",
				IncomeEffectiveDate: savingsDay("2026-10-05"), Valued: true, SuggestedUSDFormatted: "160.00",
				IncomeUSDFormatted: "800.00", ShowUSDEquivalent: true, MethodLabel: "Tasa invertida",
				Legs: []SavingsRateLeg{{Pair: "USD/VES", Rate: "40.00", EffectiveDate: savingsDay("2026-10-04"), SourceLabel: "BCV", Inverted: true}}},
			{Revision: 2, Status: "OPEN", StatusLabel: "Pendiente de confirmar", BadgeClass: "badge-income",
				RateFormatted: "20.00", IncomeAmountFormatted: "40000.00", IncomeCurrencyCode: "VES",
				IncomeEffectiveDate: savingsDay("2026-10-05"), Valued: true, SuggestedUSDFormatted: "200.00"},
		},
	}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/incomes/11", "/mobile/ahorro/ingresos/11"} {
		rec := get(t, router, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		page := html.UnescapeString(body(t, rec))
		for _, want := range []string{"Revisión 1", "Reemplazada", "El ingreso cambió: importe",
			"1 USD/VES = 40.00", "160.00 USD", "200.00 USD"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
	}
	if rec := get(t, router, "/savings/incomes/99"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown income answered %d", rec.Code)
	}

	rec := post(t, router, "/savings/incomes/11/override", url.Values{"eligible": {"0"}, "reason": {"Reembolso"}})
	if rec.Code != http.StatusSeeOther || f.Overrides[11] != "Reembolso" {
		t.Errorf("status %d, overrides %v", rec.Code, f.Overrides)
	}
	f.ActionErr = ErrInvalidInput
	rec = post(t, router, "/savings/incomes/11/override", url.Values{"eligible": {"0"}})
	if loc := rec.Header().Get("Location"); loc != "/savings/incomes/11?err=reason-required" {
		t.Errorf("a missing reason redirected to %q", loc)
	}
}

func TestEveryPageLinksToSavings(t *testing.T) {
	t.Parallel()
	router := savingsRouter(t, sampleSavings())
	if !strings.Contains(body(t, get(t, router, "/recap")), `href="/savings"`) {
		t.Error("the desktop navigation has no link to savings")
	}
	if !strings.Contains(body(t, get(t, router, "/mobile")), `href="/mobile/ahorro"`) {
		t.Error("the mobile home has no link to savings")
	}
}

// A policy whose first version starts in the future has a start and no version
// in force; an income revision carries a valuation instant. Both are pointers
// the templates print.
func TestPagesRenderAScheduledPolicyAndAValuationInstant(t *testing.T) {
	t.Parallel()
	f := NewFakeSavings()
	start := savingsDay("2026-11-01")
	scheduled := SavingsPolicyVersion{ID: 1, RateFormatted: "15.00", EffectiveFrom: start, Scheduled: true}
	f.Policy = SavingsPolicy{Versions: []SavingsPolicyVersion{scheduled}, Scheduled: &scheduled, Start: &start}
	f.OverviewResult = SavingsOverview{Policy: f.Policy, PeriodFrom: savingsDay("2026-10-01"), PeriodTo: savingsDay("2026-10-31")}
	valued := time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC)
	f.Details[5] = SavingsIncomeDetail{TransactionID: 5, Revisions: []SavingsSuggestion{
		{Revision: 1, Status: "OPEN", StatusLabel: "Pendiente de confirmar", Valued: true,
			SuggestedUSDFormatted: "10.00", ValuedAt: &valued, CreatedAt: valued},
	}}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings", "/mobile/ahorro/politica"} {
		rec := get(t, router, path)
		if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "1 "+spanishShortMonths[10]+" 2026") {
			t.Errorf("%s: status %d, the scheduled start is not shown", path, rec.Code)
		}
	}
	page := body(t, get(t, router, "/savings/incomes/5"))
	if !strings.Contains(page, "valorada 2026-10-05") {
		t.Error("the valuation instant is not shown")
	}
}
