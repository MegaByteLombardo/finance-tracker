package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func cardsServer(t *testing.T) (*FakeServices, http.Handler) {
	t.Helper()
	fs := NewFakeServices()
	fs.Cards = NewFakeCards()
	fs.Cards.Cards[7] = CreditCardView{
		AccountID: 7, Name: "Plaza Black", CurrencyCode: "VES", MinorUnit: 2, IsActive: true,
		StatementDay: 25, Denomination: "NOMINAL",
		BalanceMinor: -1250000, BalanceFormatted: "-12500.00", DebtMinor: 1250000, DebtFormatted: "12500.00",
	}
	fs.mu.Lock()
	fs.accounts[7] = Account{ID: 7, Name: "Plaza Black", Type: AccountTypeCreditCard, CurrencyCode: "VES", IsActive: true}
	fs.mu.Unlock()
	return fs, testRouter(t, fs)
}

func do(t *testing.T, router http.Handler, method, target string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })
	raw, _ := io.ReadAll(res.Body)
	return res, string(raw)
}

func TestCardPages(t *testing.T) {
	t.Parallel()
	_, router := cardsServer(t)

	res, body := do(t, router, http.MethodGet, "/cards", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Plaza Black") || !strings.Contains(body, "12,500.00") {
		t.Errorf("GET /cards = %d, missing the card or its debt", res.StatusCode)
	}

	res, body = do(t, router, http.MethodGet, "/cards/7", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Deuda total") || !strings.Contains(body, "Límite desconocido") {
		t.Errorf("GET /cards/7 = %d, missing the standing", res.StatusCode)
	}
	if res, _ := do(t, router, http.MethodGet, "/cards/8", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /cards/8 (not a card) = %d, want 404", res.StatusCode)
	}

	res, body = do(t, router, http.MethodGet, "/cards/new", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `name="request_key" value="`) {
		t.Errorf("GET /cards/new = %d, no request key rendered", res.StatusCode)
	}
}

// Opening a card: the terms are read from the form, the browser is sent to the
// card, a double submission opens one card, and a malformed day is a 422 that
// keeps the request key.
func TestOpenCardForm(t *testing.T) {
	t.Parallel()
	fs, router := cardsServer(t)
	form := url.Values{
		"request_key": {"0123456789abcdef0123"}, "name": {"Visa"}, "currency_code": {"USD"},
		"statement_day": {"25"}, "due_days": {""}, "processing_days": {"0"}, "denomination": {"NOMINAL"},
		"opening_debt": {"100.00"}, "opening_debt_date": {"2026-10-01"},
	}
	res, _ := do(t, router, http.MethodPost, "/cards", form)
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/cards/") {
		t.Fatalf("POST /cards = %d to %q, want 303 to the card", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := do(t, router, http.MethodPost, "/cards", form); res.StatusCode != http.StatusSeeOther {
		t.Errorf("the replay = %d, want 303", res.StatusCode)
	}
	if len(fs.Cards.Opened) != 1 {
		t.Fatalf("%d cards opened, want 1", len(fs.Cards.Opened))
	}
	opened := fs.Cards.Opened[0]
	if opened.Terms.StatementDay != 25 || opened.Terms.DueDaysAfterStatement != nil ||
		opened.OpeningDebt == nil || *opened.OpeningDebt != "100.00" {
		t.Errorf("opened %+v", opened)
	}

	form.Set("statement_day", "veinticinco")
	res, body := do(t, router, http.MethodPost, "/cards", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "0123456789abcdef0123") {
		t.Errorf("a malformed day = %d, want 422 keeping the request key", res.StatusCode)
	}

	fs.Cards.Err = ErrInvalidAmount
	form.Set("statement_day", "25")
	form.Set("request_key", "another-key-000000000")
	res, body = do(t, router, http.MethodPost, "/cards", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "deuda previa es un monto positivo") {
		t.Errorf("a refused debt = %d, want 422 on the debt field", res.StatusCode)
	}
}

func TestCardPurchaseAndChargeForms(t *testing.T) {
	t.Parallel()
	fs, router := cardsServer(t)

	res, body := do(t, router, http.MethodGet, "/cards/7/charges/new", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Interés") {
		t.Errorf("GET charge form = %d, no kind selector", res.StatusCode)
	}

	purchase := url.Values{
		"request_key": {"purchase-key-00000001"}, "category_id": {"1"}, "payment_method_id": {"2"},
		"amount": {"10000.00"}, "effective_date": {"2026-08-20"},
	}
	if res, _ := do(t, router, http.MethodPost, "/cards/7/purchases", purchase); res.StatusCode != http.StatusSeeOther ||
		res.Header.Get("Location") != "/cards/7" {
		t.Errorf("POST purchase = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if len(fs.Cards.Purchases) != 1 || fs.Cards.Purchases[0].AccountID != 7 || fs.Cards.Purchases[0].EffectiveDate != "2026-08-20" {
		t.Errorf("purchases = %+v", fs.Cards.Purchases)
	}

	charge := url.Values{
		"request_key": {"charge-key-0000000001"}, "kind": {"PENALTY"}, "category_id": {"1"},
		"payment_method_id": {"2"}, "amount": {"100"}, "effective_date": {"2026-10-25"},
	}
	if res, _ := do(t, router, http.MethodPost, "/cards/7/charges", charge); res.StatusCode != http.StatusSeeOther {
		t.Errorf("POST charge = %d", res.StatusCode)
	}
	if len(fs.Cards.Charges) != 1 || fs.Cards.Charges[0] != "PENALTY" {
		t.Errorf("charges = %v", fs.Cards.Charges)
	}

	delete(purchase, "amount")
	if res, _ := do(t, router, http.MethodPost, "/cards/7/purchases", purchase); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a purchase with no amount = %d, want 422", res.StatusCode)
	}
}

// The generic transaction screens hand a refund and a card charge to their own
// forms (TD-045 in the presentation layer), and never write one.
func TestGenericEditRoutesRefundsAndCharges(t *testing.T) {
	t.Parallel()
	fs, router := cardsServer(t)
	fs.mu.Lock()
	fs.transactions = append(fs.transactions,
		TransactionView{ID: 501, Type: TransactionTypeRefund, AccountID: 7, EffectiveDate: time.Now()},
		TransactionView{ID: 502, Type: TransactionTypeExpense, AccountID: 7, EffectiveDate: time.Now()},
	)
	fs.mu.Unlock()
	fs.Cards.ChargeKinds[502] = "INTEREST"

	if res, _ := do(t, router, http.MethodGet, "/transactions/501/edit", nil); res.StatusCode != http.StatusSeeOther ||
		res.Header.Get("Location") != "/refunds/501/edit" {
		t.Errorf("editing a refund = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := do(t, router, http.MethodGet, "/transactions/502/edit", nil); res.StatusCode != http.StatusSeeOther ||
		res.Header.Get("Location") != "/cards/7/charges/502/edit" {
		t.Errorf("editing a charge = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := do(t, router, http.MethodPost, "/transactions/501", url.Values{"type": {"EXPENSE"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("posting a refund to the generic form = %d, want 400", res.StatusCode)
	}
}

// A refund form names its account first, then records with its key and goes
// back where it was asked to -- never to another host.
func TestRefundForm(t *testing.T) {
	t.Parallel()
	fs, router := cardsServer(t)

	res, body := do(t, router, http.MethodGet, "/refunds/new", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Continuar") {
		t.Errorf("GET /refunds/new without an account = %d, want the account step", res.StatusCode)
	}

	refund := url.Values{
		"request_key": {"refund-key-0000000001"}, "account_id": {"7"}, "category_id": {"1"},
		"payment_method_id": {"2"}, "amount": {"2000"}, "effective_date": {"2026-10-02"},
		"return_to": {"//evil.example"},
	}
	res, _ = do(t, router, http.MethodPost, "/refunds", refund)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/transactions" {
		t.Errorf("POST /refunds = %d to %q, want 303 to /transactions", res.StatusCode, res.Header.Get("Location"))
	}
	if len(fs.Cards.Refunds) != 1 || fs.Cards.Refunds[0].OriginalTransactionID != nil {
		t.Errorf("refunds = %+v", fs.Cards.Refunds)
	}

	fs.Cards.Err = ErrRefundExceedsOriginal
	refund.Set("request_key", "refund-key-0000000002")
	refund.Set("original_transaction_id", "9")
	res, body = do(t, router, http.MethodPost, "/refunds", refund)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "superarían su monto") {
		t.Errorf("a refund past its purchase = %d, want 422 on the amount", res.StatusCode)
	}
}

// The dashboard lists assets and liabilities apart.
func TestDashboardGroupsAssetsAndLiabilities(t *testing.T) {
	t.Parallel()
	fs := NewFakeServices()
	if err := fs.SeedSampleData(t.Context()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := fs.OpenAccount(t.Context(), OpenAccountInput{Account: CreateAccountInput{
		Name: "Visa Oro", Type: AccountTypeCreditCard, CurrencyCode: "USD",
	}}); err != nil {
		t.Fatalf("opening a card in the fake: %v", err)
	}
	router := testRouter(t, fs)
	res, body := do(t, router, http.MethodGet, "/", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	assets, liabilities := strings.Index(body, "Activos ·"), strings.Index(body, "Pasivos ·")
	card := strings.Index(body, "Visa Oro")
	if assets < 0 || liabilities < 0 || card < liabilities {
		t.Errorf("the card is not listed under Pasivos (activos %d, pasivos %d, card %d)", assets, liabilities, card)
	}
}
