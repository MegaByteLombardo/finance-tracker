package web

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newFXRouter(t *testing.T) (http.Handler, *FakeRateStore) {
	t.Helper()

	fs := NewFakeServices()
	svcs := fs.BuildServices()
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), svcs, DefaultConfig())
	return router, fs.Rates
}

func get(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func post(t *testing.T, router http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the response body: %v", err)
		}
	}()
	content, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	return string(content)
}

func TestSettingsPageShowsTheBaseCurrency(t *testing.T) {
	t.Parallel()

	router, _ := newFXRouter(t)

	rec := get(t, router, "/settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	page := body(t, rec)
	if !strings.Contains(page, "Moneda base") {
		t.Error("the settings page does not offer the base currency")
	}
	// The invariant a user wants stated before they press the button.
	if !strings.Contains(page, "no modifica ninguna transacción") {
		t.Error("the settings page does not say that changing the base currency rewrites nothing")
	}
}

func TestSetBaseCurrency(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	rec := post(t, router, "/settings/base-currency", url.Values{"base_currency_code": {"VES"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	settings, err := store.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.BaseCurrencyCode != "VES" {
		t.Errorf("base currency = %q, want VES", settings.BaseCurrencyCode)
	}
}

func TestSetBaseCurrencyRejectsRubbish(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	rec := post(t, router, "/settings/base-currency", url.Values{"base_currency_code": {"nonsense"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}

	settings, err := store.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.BaseCurrencyCode != "USD" {
		t.Errorf("the base currency changed to %q despite the refusal", settings.BaseCurrencyCode)
	}
}

func TestExchangeRatesPageExplainsTheConvention(t *testing.T) {
	t.Parallel()

	router, _ := newFXRouter(t)

	page := body(t, get(t, router, "/settings/exchange-rates"))

	for _, phrase := range []string{
		"1 base = tasa cotizada", // the orientation, stated once and the same way everywhere
		"fecha valor",            // the economic date, distinguished from the fetch
		"Actualizar tasas BCV",   // the refresh button, since the fake has a provider
		"Registrar una tasa manual",
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the rates page does not mention %q", phrase)
		}
	}
}

// TestTheRateFormOpensOnTheMissingRate is the dashboard's "Cargar tasa" link:
// the pair and the day arrive filled in, and rubbish is ignored rather than
// echoed into the form.
func TestTheRateFormOpensOnTheMissingRate(t *testing.T) {
	t.Parallel()

	router, _ := newFXRouter(t)

	page := body(t, get(t, router, "/settings/exchange-rates?base=usd&quote=VES&date=2026-09-11"))
	for _, want := range []string{
		`<option value="USD" selected>`,
		`<option value="VES" selected>`,
		`value="2026-09-11"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the prefilled form does not contain %q", want)
		}
	}

	page = body(t, get(t, router, "/settings/exchange-rates?base=XXX&date=11/09/2026"))
	if strings.Contains(page, "XXX") || strings.Contains(page, "11/09/2026") {
		t.Error("an unknown currency or a malformed date was echoed into the form")
	}
	if !strings.Contains(page, `value="`+todayForForms()+`"`) {
		t.Error("a malformed date did not fall back to today")
	}
}

func TestCreateManualRate(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	rec := post(t, router, "/settings/exchange-rates", url.Values{
		"base_currency_code":  {"USD"},
		"quote_currency_code": {"VES"},
		"rate":                {"842.20670000"},
		"effective_date":      {"2026-09-11"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	rates, err := store.List(t.Context(), ListExchangeRatesInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("%d rates stored, want 1", len(rates))
	}
	if rates[0].Rate != "842.20670000" {
		t.Errorf("rate = %q; the precision the user typed is part of the value", rates[0].Rate)
	}
	if rates[0].Source != RateSourceManual {
		t.Errorf("source = %q, want MANUAL", rates[0].Source)
	}
}

// TestAFormCannotClaimToBeTheBancoCentral is the rule that keeps provenance
// meaningful.
//
// The handler does not read a source from the form, and CreateManualRateInput
// has no field for one, so a POST that tries to supply it is ignored rather than
// obeyed.
func TestAFormCannotClaimToBeTheBancoCentral(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	rec := post(t, router, "/settings/exchange-rates", url.Values{
		"base_currency_code":  {"USD"},
		"quote_currency_code": {"VES"},
		"rate":                {"1.00"},
		"effective_date":      {"2026-09-11"},
		"source":              {"BCV_OFFICIAL"},
		"Source":              {"BCV_OFFICIAL"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	rates, err := store.List(t.Context(), ListExchangeRatesInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("%d rates stored, want 1", len(rates))
	}
	if rates[0].Source != RateSourceManual {
		t.Errorf("a form submission was stored as %q; only the backend may set the source",
			rates[0].Source)
	}
	if rates[0].IsEditable != true {
		t.Error("the stored rate is not editable, which means it was not recorded as manual")
	}
}

func TestCreateManualRateRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantText   string
	}{
		{
			name: "a comma decimal separator",
			form: url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"VES"},
				"rate": {"842,2067"}, "effective_date": {"2026-09-11"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "sin separadores de miles",
		},
		{
			name: "a grouped number",
			form: url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"VES"},
				"rate": {"1,000.50"}, "effective_date": {"2026-09-11"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "sin separadores de miles",
		},
		{
			name: "a rate of zero",
			form: url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"VES"},
				"rate": {"0.00"}, "effective_date": {"2026-09-11"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "mayor que cero",
		},
		{
			name: "the same currency twice",
			form: url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"USD"},
				"rate": {"1"}, "effective_date": {"2026-09-11"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "dos monedas distintas",
		},
		{
			name: "a date that does not exist",
			form: url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"VES"},
				"rate": {"1"}, "effective_date": {"2026-02-31"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "fecha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, store := newFXRouter(t)

			rec := post(t, router, "/settings/exchange-rates", tc.form)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if page := body(t, rec); !strings.Contains(page, tc.wantText) {
				t.Errorf("the page does not explain the problem (%q)", tc.wantText)
			}

			rates, err := store.List(t.Context(), ListExchangeRatesInput{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(rates) != 0 {
				t.Errorf("%d rates were stored by a rejected submission", len(rates))
			}
		})
	}
}

func TestDuplicateManualRateIsAConflict(t *testing.T) {
	t.Parallel()

	router, _ := newFXRouter(t)

	form := url.Values{"base_currency_code": {"USD"}, "quote_currency_code": {"VES"},
		"rate": {"500"}, "effective_date": {"2026-09-11"}}

	if rec := post(t, router, "/settings/exchange-rates", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("the first submission returned %d", rec.Code)
	}

	rec := post(t, router, "/settings/exchange-rates", form)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if page := body(t, rec); !strings.Contains(page, "Ya existe una tasa manual") {
		t.Error("the page does not say a rate already exists for that pair and date")
	}
}

func TestAPublishedRateCannotBeEdited(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	store.mu.Lock()
	store.rates[99] = ExchangeRate{
		ID: 99, BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES", Pair: "USD/VES",
		EffectiveDate: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Rate:          "842.20670000",
		Source:        RateSourceBCVOfficial, SourceLabel: RateSourceBCVOfficial.Label(),
		IsEditable: false, IsActive: true,
	}
	store.nextID = 100
	store.mu.Unlock()

	rec := post(t, router, "/settings/exchange-rates/99", url.Values{"rate": {"1.00"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}

	stored, err := store.Get(t.Context(), 99)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Rate != "842.20670000" {
		t.Errorf("the published rate was changed to %q", stored.Rate)
	}

	// Nor archived.
	if rec := post(t, router, "/settings/exchange-rates/99/toggle", nil); rec.Code != http.StatusConflict {
		t.Errorf("archiving a published rate returned %d, want 409", rec.Code)
	}
}

func TestArchivingAManualRateAndRestoringIt(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)

	created, err := store.CreateManual(t.Context(), CreateManualRateInput{
		BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES",
		Rate: "500", EffectiveDate: "2026-09-11",
	})
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}

	if rec := post(t, router, "/settings/exchange-rates/"+strconv.FormatInt(created.ID, 10)+"/toggle", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("archiving returned %d, want 303", rec.Code)
	}
	archived, err := store.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if archived.IsActive {
		t.Error("the rate was not archived")
	}

	// Archiving is not deletion: the row is still there and comes back.
	if rec := post(t, router, "/settings/exchange-rates/"+strconv.FormatInt(created.ID, 10)+"/toggle", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("restoring returned %d, want 303", rec.Code)
	}
	restored, err := store.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !restored.IsActive {
		t.Error("the rate was not restored")
	}
}

// TestAProviderFailureIsNotAServerError is the resilience rule at the HTTP
// boundary.
//
// The BCV being unreachable, or having changed its page, says nothing about this
// application. A 500 would replace a working page -- stored rates, the manual
// form -- with an error screen.
func TestAProviderFailureIsNotAServerError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantText string
	}{
		{"unreachable", ErrProviderUnavailable, "No se pudo contactar al BCV"},
		{"unparseable", ErrProviderInvalidResponse, "no pudo interpretarse"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, store := newFXRouter(t)

			// A rate already stored, which must stay visible through the failure.
			if _, err := store.CreateManual(t.Context(), CreateManualRateInput{
				BaseCurrencyCode: "USD", QuoteCurrencyCode: "VES",
				Rate: "500", EffectiveDate: "2026-09-11",
			}); err != nil {
				t.Fatalf("CreateManual: %v", err)
			}
			store.SetProviderError(tc.err)

			rec := post(t, router, "/settings/exchange-rates/refresh", nil)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: a provider failure is not a server error", rec.Code)
			}

			page := body(t, rec)
			if !strings.Contains(page, tc.wantText) {
				t.Errorf("the page does not explain the failure (%q)", tc.wantText)
			}
			if !strings.Contains(page, "500") {
				t.Error("the stored rate disappeared from the page during a provider failure")
			}
			if !strings.Contains(page, "Registrar una tasa manual") {
				t.Error("manual entry is not offered while the provider is down")
			}
		})
	}
}

func TestTheRefreshButtonIsHiddenWithoutAProvider(t *testing.T) {
	t.Parallel()

	router, store := newFXRouter(t)
	store.SetProviderEnabled(false)

	page := body(t, get(t, router, "/settings/exchange-rates"))
	if strings.Contains(page, "Actualizar tasas BCV") {
		t.Error("the refresh button is offered with no provider configured")
	}
}

// TestTheDashboardSaysWhenTheNetWorthIsIncomplete is the missing-rate contract at
// the level the user meets it: never a silent partial total.
func TestTheDashboardSaysWhenTheNetWorthIsIncomplete(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	fs.Rates = NewFakeRateStore()
	fs.Rates.SetNetWorth(NetWorth{
		BaseCurrencyCode: "USD",
		BaseMinorUnit:    2,
		TotalMinor:       100000,
		TotalFormatted:   "1000.00",
		Complete:         false,
		Components: []NetWorthComponent{
			{CurrencyCode: "USD", BalanceFormatted: "1000.00", ConvertedFormatted: "1000.00", Valued: true},
			{CurrencyCode: "JPY", BalanceFormatted: "50000", Valued: false},
		},
		Missing: []MissingRateView{
			{FromCurrencyCode: "JPY", ToCurrencyCode: "USD", BalanceFormatted: "50000"},
		},
	})

	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())
	page := body(t, get(t, router, "/"))

	if !strings.Contains(page, "incompleto") {
		t.Error("the dashboard presents a partial total without saying it is partial")
	}
	if !strings.Contains(page, "JPY") {
		t.Error("the dashboard does not name the currency it could not value")
	}
	if !strings.Contains(page, "/settings/exchange-rates") {
		t.Error("the dashboard does not offer a way to add the missing rate")
	}
}

// TestTheDashboardShowsHowEachComponentWasValued is the traceability
// requirement: a figure nobody can check by hand has to explain itself.
func TestTheDashboardShowsHowEachComponentWasValued(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	fs.Rates = NewFakeRateStore()
	fs.Rates.SetNetWorth(NetWorth{
		BaseCurrencyCode: "USD",
		BaseMinorUnit:    2,
		TotalMinor:       142000,
		TotalFormatted:   "1420.00",
		Complete:         true,
		RatesAsOf:        time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Components: []NetWorthComponent{{
			CurrencyCode: "VES", BalanceFormatted: "50000.00",
			ConvertedFormatted: "100.00", Valued: true,
			Resolution: &RateResolutionView{
				Method:             RateMethodInverted,
				MethodLabel:        RateMethodInverted.Label(),
				UsesManualOverride: true,
				Legs: []RateLegView{{
					Pair: "USD/VES", Rate: "500", Inverted: true,
					Source: RateSourceManual, SourceLabel: RateSourceManual.Label(),
					EffectiveDate: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
				}},
			},
		}},
	})

	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())
	page := body(t, get(t, router, "/"))

	for _, phrase := range []string{
		"Patrimonio neto",
		"Base: USD",
		"Tasas al",
		"USD/VES",
		"invertida",
		"override manual", // precedence must be visible, never silent
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the dashboard does not show %q", phrase)
		}
	}
}

func TestTheDashboardSurvivesAValuationFailure(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	fs.Rates = NewFakeRateStore()
	fs.Rates.SetNetWorthError(errors.New("the valuation base is gone"))

	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())

	rec := get(t, router, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the ledger's own figures do not depend on any rate", rec.Code)
	}
	if page := body(t, rec); !strings.Contains(page, "No se pudo calcular el patrimonio") {
		t.Error("the dashboard does not say that the valuation failed")
	}
}

func TestMobileRatesAndSettingsRender(t *testing.T) {
	t.Parallel()

	router, _ := newFXRouter(t)

	for _, path := range []string{"/mobile/configuracion", "/mobile/tasas"} {
		rec := get(t, router, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

// TestTheEconomicLocationAgreesWithThePersistenceLayer is the counterpart to
// internal/web duplicating the zone rather than importing internal/database.
func TestTheEconomicLocationAgreesWithThePersistenceLayer(t *testing.T) {
	t.Parallel()

	_, offset := time.Now().In(economicLocation).Zone()
	if offset != -4*60*60 {
		t.Errorf("the web layer's economic offset is %d seconds, want -14400", offset)
	}
}
