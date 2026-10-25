package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// statementsServer is cardsServer with a VES and a USD account to pay from,
// and a standing with every suggested amount.
func statementsServer(t *testing.T) (*FakeServices, http.Handler) {
	t.Helper()
	fs, _ := cardsServer(t)
	fs.mu.Lock()
	fs.accounts[3] = Account{ID: 3, Name: "Banesco", Type: AccountTypeBank, CurrencyCode: "VES", IsActive: true}
	fs.accounts[4] = Account{ID: 4, Name: "Zelle", Type: AccountTypeBank, CurrencyCode: "USD", IsActive: true}
	fs.mu.Unlock()
	closing := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	fs.Cards.Standings[7] = CardStandingView{
		HasCurrent: true,
		Current: CardCycleView{
			StatementID: 2, Registered: true, ClosingDate: closing, Status: CardCycleBilled, StatusLabel: "Facturado",
		},
		BilledPendingMinor: 1850000, BilledPendingFormatted: "18500.00",
		MinimumKnown: true, MinimumPendingMinor: 250000, MinimumPendingFormatted: "2500.00",
		PastDueMinor: 70000, PastDueFormatted: "700.00", PastDueCycles: []time.Time{closing.AddDate(0, -1, 0)},
		Cycles: []CardCycleView{{StatementID: 2, Registered: true, ClosingDate: closing, Status: CardCycleBilled, StatusLabel: "Facturado"}},
		Payments: []CardPaymentAllocationView{{
			TransactionID: 11, Date: closing.AddDate(0, 0, 5), AmountFormatted: "1000.00", AgainstClosing: &closing,
			Lines: []CardAllocationLineView{
				{Target: "OVERDUE", Label: "Vencido", AmountFormatted: "700.00"},
				{Target: "MINIMUM", Label: "Mínimo", AmountFormatted: "300.00"},
			},
		}},
	}
	fs.Cards.StatementInputs[2] = CardStatementInput{PeriodStart: "2026-09-26", ClosingDate: "2026-10-25", ClosingOwed: "18500.00"}
	fs.Cards.Reconciliations[2] = CardReconciliationView{
		Cycle:             CardCycleView{StatementID: 2, Registered: true, ClosingDate: closing},
		BankOwedFormatted: "18500.00", LedgerOwedFormatted: "18200.00", DifferenceFormatted: "300.00",
		Candidates: []CardPlacedView{{TransactionID: 21, Date: closing.AddDate(0, 0, -1), RoleLabel: "Consumo o cargo",
			AmountFormatted: "-300.00", ClosingDate: closing.AddDate(0, 1, 0), CanAssert: true}},
	}
	return fs, testRouter(t, fs)
}

// The payment form offers what the bank asks for -- overdue, minimum, billed,
// total debt -- from assets only, and submits nothing by itself.
func TestCardPaymentForm(t *testing.T) {
	t.Parallel()
	_, router := statementsServer(t)
	res, body := do(t, router, http.MethodGet, "/cards/7/payments/new", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cards/7/payments/new = %d", res.StatusCode)
	}
	for _, want := range []string{`data-fill-amount="700.00"`, `data-fill-amount="2500.00"`, `data-fill-amount="18500.00"`,
		`data-fill-amount="12500.00"`, "Banesco (VES)", "Zelle (USD)", `name="request_key"`, "type=CARD_PAYMENT"} {
		if !strings.Contains(body, want) {
			t.Errorf("the payment form lacks %q", want)
		}
	}
	if strings.Contains(body, "Plaza Black (VES)") {
		t.Error("the card is offered as a source of its own payment")
	}
	if res, _ := do(t, router, http.MethodGet, "/cards/8/payments/new", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("paying something that is not a card = %d, want 404", res.StatusCode)
	}
}

func TestRecordCardPayment(t *testing.T) {
	t.Parallel()
	fs, router := statementsServer(t)

	form := url.Values{"request_key": {"pay-key-0000000000001"}, "source_account_id": {"3"}, "amount": {"1000.00"},
		"source_amount": {"9.99"}, "effective_date": {"2026-10-30"}}
	res, _ := do(t, router, http.MethodPost, "/cards/7/payments", form)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/cards/7" {
		t.Fatalf("POST a VES payment = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if p := fs.Cards.Payments[0]; p.SourceAmount != "" || p.CardAccountID != 7 || p.Savings != nil {
		t.Errorf("a same-currency payment was sent %+v", p)
	}

	// From USD the amount that left is required; without it the form says so.
	form = url.Values{"request_key": {"pay-key-0000000000002"}, "source_account_id": {"4"}, "amount": {"20000.00"},
		"effective_date": {"2026-10-30"}}
	res, body := do(t, router, http.MethodPost, "/cards/7/payments", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "indica cuánto salió de ella") {
		t.Errorf("a USD payment without its USD amount = %d", res.StatusCode)
	}
	if strings.Contains(body, `id="source-amount-group" hidden`) {
		t.Error("the re-rendered form hides the amount it asks for")
	}

	// With it, and a savings plan, both reach the backend.
	form.Set("source_amount", "80.00")
	form["use_goal"] = []string{""}
	form["use_amount"] = []string{"30.00"}
	form.Set("savings_purpose", "goal")
	if res, _ := do(t, router, http.MethodPost, "/cards/7/payments", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST a USD payment = %d", res.StatusCode)
	}
	p := fs.Cards.Payments[1]
	if p.SourceAmount != "80.00" || p.Savings == nil || len(p.Savings.Spend) != 1 || p.Savings.Spend[0].Amount != "30.00" {
		t.Errorf("a USD payment was sent %+v", p)
	}

	// A refused savings plan says so and keeps the form.
	fs.Cards.Err = fmt.Errorf("plan: %w", ErrSavingsInsufficientReserved)
	form.Set("request_key", "pay-key-0000000000003")
	res, body = do(t, router, http.MethodPost, "/cards/7/payments", form)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "El pago no se registró") {
		t.Errorf("a refused plan = %d", res.StatusCode)
	}
}

// The savings hint of the payment form reads the amount that left the source:
// the USD amount of an exchange, the amount itself otherwise.
func TestSavingsImpactOfACardPayment(t *testing.T) {
	t.Parallel()
	fs, router := statementsServer(t)
	do(t, router, http.MethodGet, "/savings/impact?type=CARD_PAYMENT&source_account_id=4&amount=20000.00&source_amount=80.00", nil)
	do(t, router, http.MethodGet, "/savings/impact?type=CARD_PAYMENT&source_account_id=4&amount=50.00&source_amount=", nil)
	calls := fs.Savings.ImpactCalls
	if len(calls) != 2 || calls[0] != "4:80.00" || calls[1] != "4:50.00" {
		t.Errorf("impact calls = %v", calls)
	}
}

func TestCardStatementPages(t *testing.T) {
	t.Parallel()
	fs, router := statementsServer(t)

	res, body := do(t, router, http.MethodGet, "/cards/7/statements", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Aplicación de los pagos") || !strings.Contains(body, "Mínimo pendiente") {
		t.Errorf("GET /cards/7/statements = %d", res.StatusCode)
	}
	res, body = do(t, router, http.MethodGet, "/cards/7", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Facturado pendiente") || !strings.Contains(body, "/cards/7/payments/new") {
		t.Errorf("GET /cards/7 lacks the obligations: %d", res.StatusCode)
	}

	res, body = do(t, router, http.MethodGet, "/cards/7/statements/new", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `value="2026-10-25"`) {
		t.Errorf("GET the statement form = %d without the proposed closing", res.StatusCode)
	}
	res, body = do(t, router, http.MethodGet, "/cards/7/statements/new?period_start=2026-08-26&closing_date=2026-09-25", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `value="2026-09-25"`) {
		t.Errorf("registering a computed cycle = %d without its closing", res.StatusCode)
	}

	form := url.Values{"period_start": {"2026-10-26"}, "closing_date": {"2026-11-25"}}
	if res, body := do(t, router, http.MethodPost, "/cards/7/statements", form); res.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "saldo al corte") {
		t.Errorf("a statement without its balance = %d", res.StatusCode)
	}
	form.Set("closing_owed", "1500.00")
	res, _ = do(t, router, http.MethodPost, "/cards/7/statements", form)
	if res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "/reconcile") {
		t.Errorf("POST a statement = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	fs.Cards.Err = fmt.Errorf("closing: %w", ErrStatementExists)
	if res, _ := do(t, router, http.MethodPost, "/cards/7/statements", form); res.StatusCode != http.StatusConflict {
		t.Errorf("a second statement on the same closing = %d, want 409", res.StatusCode)
	}
	fs.Cards.Err = nil

	res, body = do(t, router, http.MethodGet, "/cards/7/statements/2/edit", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `value="18500.00"`) {
		t.Errorf("GET the statement's correction = %d", res.StatusCode)
	}
	if res, _ := do(t, router, http.MethodGet, "/cards/7/statements/99/edit", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown statement = %d, want 404", res.StatusCode)
	}
}

func TestReconcileAndAssert(t *testing.T) {
	t.Parallel()
	fs, router := statementsServer(t)

	res, body := do(t, router, http.MethodGet, "/cards/7/statements/2/reconcile", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Incluir en este estado") || !strings.Contains(body, "300.00") {
		t.Errorf("GET the reconciliation = %d", res.StatusCode)
	}
	if res, _ := do(t, router, http.MethodGet, "/cards/7/statements/99/reconcile", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("reconciling an unknown statement = %d, want 404", res.StatusCode)
	}

	res, _ = do(t, router, http.MethodPost, "/cards/7/statements/2/assertions", url.Values{"transaction_id": {"21"}, "action": {"assign"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "msg=assigned") {
		t.Errorf("assigning = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	res, _ = do(t, router, http.MethodPost, "/cards/7/statements/2/assertions", url.Values{"transaction_id": {"21"}, "action": {"clear"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "msg=cleared") {
		t.Errorf("clearing = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	a := fs.Cards.Assertions
	if len(a) != 2 || a[0].StatementID == nil || *a[0].StatementID != 2 || a[0].TransactionID != 21 || a[1].StatementID != nil {
		t.Errorf("assertions = %+v", a)
	}

	fs.Cards.Err = fmt.Errorf("late: %w", ErrAssertionAfterClosing)
	res, _ = do(t, router, http.MethodPost, "/cards/7/statements/2/assertions", url.Values{"transaction_id": {"21"}, "action": {"assign"}})
	if !strings.Contains(res.Header.Get("Location"), "err=assertion-after-closing") {
		t.Errorf("a refused assertion redirects to %q", res.Header.Get("Location"))
	}
	if res, _ := do(t, router, http.MethodPost, "/cards/7/statements/2/assertions", url.Values{"transaction_id": {"21"}, "action": {"move"}}); !strings.Contains(res.Header.Get("Location"), "err=") {
		t.Errorf("an unknown action redirects to %q", res.Header.Get("Location"))
	}
}
