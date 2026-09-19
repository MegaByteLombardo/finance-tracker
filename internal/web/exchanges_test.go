package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The currency exchange screens.
//
// What every test here is ultimately about: an exchange has TWO amounts in TWO
// currencies, and the interface must never let one of them stand for both, nor
// let the operation be recorded through a screen built for a single amount.

func setupExchangeAccounts(t *testing.T, fs *FakeServices) (banescoVES, mercantilUSD, efectivoVES Account) {
	t.Helper()
	ctx := context.Background()

	checking := AccountSubtypeChecking

	var err error
	banescoVES, err = fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Banesco VES", Type: AccountTypeBank, Subtype: &checking, CurrencyCode: "VES",
	})
	if err != nil {
		t.Fatalf("creating Banesco VES: %v", err)
	}
	mercantilUSD, err = fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Mercantil USD", Type: AccountTypeBank, Subtype: &checking, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating Mercantil USD: %v", err)
	}
	efectivoVES, err = fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Efectivo VES", Type: AccountTypeCash, CurrencyCode: "VES",
	})
	if err != nil {
		t.Fatalf("creating Efectivo VES: %v", err)
	}
	return banescoVES, mercantilUSD, efectivoVES
}

func postForm(t *testing.T, router http.Handler, path string, form url.Values) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Result()
}

func bodyOf(t *testing.T, res *http.Response) string {
	t.Helper()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the response body: %v", err)
		}
	}()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	return string(body)
}

func TestGetNewExchangeForm(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/exchanges/new", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /exchanges/new returned %d, want 200", res.StatusCode)
	}
	body := bodyOf(t, res)

	// Two amount fields, because there are two amounts.
	for _, want := range []string{
		`name="source_amount"`, `name="destination_amount"`,
		`name="source_account_id"`, `name="destination_account_id"`,
		"Banesco VES", "Mercantil USD", "Implied rate",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form does not contain %q", want)
		}
	}
	// And no single "amount", which would be the transfer form's field.
	if strings.Contains(body, `name="amount"`) {
		t.Error(`the exchange form has a single "amount" field`)
	}
}

func TestCreateExchangeRecordsBothAmounts(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ves, usd, _ := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)

	res := postForm(t, router, "/exchanges", url.Values{
		"source_account_id":      {strconv.FormatInt(ves.ID, 10)},
		"source_amount":          {"84220.67"},
		"destination_account_id": {strconv.FormatInt(usd.ID, 10)},
		"destination_amount":     {"100.00"},
		"effective_date":         {"2026-09-16"},
		"description":            {"Compra de divisas"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /exchanges returned %d, want 303: %s", res.StatusCode, bodyOf(t, res))
	}
	_ = bodyOf(t, res)

	txs, err := fs.ListTransactions(context.Background(), ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("%d transactions were recorded, want 1", len(txs))
	}

	tx := txs[0]
	if tx.Type != TransactionTypeFXExchange {
		t.Errorf("the transaction type is %s, want FX_EXCHANGE", tx.Type)
	}
	if tx.CategoryID != nil || tx.PaymentMethodID != nil {
		t.Error("the exchange acquired a category or a payment method")
	}
	if tx.Exchange == nil {
		t.Fatal("the transaction carries no exchange detail")
	}
	if tx.Exchange.SourceAmountMinor != 8422067 || tx.Exchange.DestinationAmountMinor != 10000 {
		t.Errorf("the amounts are %d and %d, want 8422067 and 10000",
			tx.Exchange.SourceAmountMinor, tx.Exchange.DestinationAmountMinor)
	}
	rate := tx.Exchange.ExecutionRate
	if rate.BaseCurrencyCode != "USD" || rate.QuoteCurrencyCode != "VES" || rate.RateFormatted != "842.2067" {
		t.Errorf("the execution rate is 1 %s = %s %s, want 1 USD = 842.2067 VES",
			rate.BaseCurrencyCode, rate.RateFormatted, rate.QuoteCurrencyCode)
	}

	// The balances moved in two directions, in two currencies.
	vesBalance, err := fs.GetAccountBalance(context.Background(), ves.ID)
	if err != nil {
		t.Fatalf("reading the VES balance: %v", err)
	}
	usdBalance, err := fs.GetAccountBalance(context.Background(), usd.ID)
	if err != nil {
		t.Fatalf("reading the USD balance: %v", err)
	}
	if vesBalance.BalanceMinor != -8422067 || usdBalance.BalanceMinor != 10000 {
		t.Errorf("the balances are %d VES and %d USD, want -8422067 and 10000",
			vesBalance.BalanceMinor, usdBalance.BalanceMinor)
	}
}

func TestCreateExchangeRefusesTwoAccountsInOneCurrency(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	banesco, _, efectivo := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)

	res := postForm(t, router, "/exchanges", url.Values{
		"source_account_id":      {strconv.FormatInt(banesco.ID, 10)},
		"source_amount":          {"85000.00"},
		"destination_account_id": {strconv.FormatInt(efectivo.ID, 10)},
		"destination_amount":     {"85000.00"},
		"effective_date":         {"2026-09-16"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a same-currency exchange returned %d, want 422", res.StatusCode)
	}
	body := bodyOf(t, res)
	if !strings.Contains(body, "transfer, not an exchange") {
		t.Errorf("the page does not say why it was refused:\n%s", body)
	}

	txs, err := fs.ListTransactions(context.Background(), ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 0 {
		t.Errorf("%d transactions were recorded by a refused exchange", len(txs))
	}
}

func TestCreateExchangeValidatesTheForm(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ves, usd, _ := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)

	base := func() url.Values {
		return url.Values{
			"source_account_id":      {strconv.FormatInt(ves.ID, 10)},
			"source_amount":          {"85000.00"},
			"destination_account_id": {strconv.FormatInt(usd.ID, 10)},
			"destination_amount":     {"100.00"},
			"effective_date":         {"2026-09-16"},
		}
	}

	cases := map[string]func(url.Values){
		"no source account":      func(v url.Values) { v.Set("source_account_id", "") },
		"no destination account": func(v url.Values) { v.Set("destination_account_id", "") },
		"same account":           func(v url.Values) { v.Set("destination_account_id", v.Get("source_account_id")) },
		"no source amount":       func(v url.Values) { v.Set("source_amount", "") },
		"no destination amount":  func(v url.Values) { v.Set("destination_amount", "") },
		"negative source":        func(v url.Values) { v.Set("source_amount", "-85000.00") },
		"negative destination":   func(v url.Values) { v.Set("destination_amount", "-100.00") },
		"no date":                func(v url.Values) { v.Set("effective_date", "") },
		"unparseable amount":     func(v url.Values) { v.Set("source_amount", "85.000,00") },
		"impossible date":        func(v url.Values) { v.Set("effective_date", "2026-02-31") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			form := base()
			mutate(form)

			res := postForm(t, router, "/exchanges", form)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("%s returned %d, want 422", name, res.StatusCode)
			}
			_ = bodyOf(t, res)
		})
	}

	txs, err := fs.ListTransactions(context.Background(), ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 0 {
		t.Errorf("%d transactions were recorded by refused submissions", len(txs))
	}
}

func TestEditExchangeKeepsTheMovementIDsAndRederivesTheRate(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ves, usd, _ := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)
	ctx := context.Background()

	created, err := fs.RecordExchange(ctx, RecordExchangeInput{
		SourceAccountID:      ves.ID,
		SourceAmount:         "84220.67",
		DestinationAccountID: usd.ID,
		DestinationAmount:    "100.00",
		EffectiveDate:        "2026-09-16",
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	// The edit form opens, and shows what the exchange currently reads as.
	req := httptest.NewRequest(http.MethodGet,
		"/exchanges/"+strconv.FormatInt(created.ID, 10)+"/edit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the edit form returned %d, want 200", res.StatusCode)
	}
	body := bodyOf(t, res)
	for _, want := range []string{"84220.67", "100.00", "1 USD = 842.2067 VES"} {
		if !strings.Contains(body, want) {
			t.Errorf("the edit form does not show %q", want)
		}
	}

	// Correcting the delivered amount.
	res = postForm(t, router, "/exchanges/"+strconv.FormatInt(created.ID, 10), url.Values{
		"source_account_id":      {strconv.FormatInt(ves.ID, 10)},
		"source_amount":          {"85000.00"},
		"destination_account_id": {strconv.FormatInt(usd.ID, 10)},
		"destination_amount":     {"100.00"},
		"effective_date":         {"2026-09-16"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the edit returned %d, want 303: %s", res.StatusCode, bodyOf(t, res))
	}
	_ = bodyOf(t, res)

	updated, err := fs.GetTransaction(ctx, created.ID)
	if err != nil {
		t.Fatalf("reading the edited exchange: %v", err)
	}
	if updated.Exchange.SourceMovementID != created.Exchange.SourceMovementID ||
		updated.Exchange.DestinationMovementID != created.Exchange.DestinationMovementID {
		t.Errorf("the movement ids changed: %d,%d were %d,%d",
			updated.Exchange.SourceMovementID, updated.Exchange.DestinationMovementID,
			created.Exchange.SourceMovementID, created.Exchange.DestinationMovementID)
	}
	if got := updated.Exchange.ExecutionRate.RateFormatted; got != "850" {
		t.Errorf("the execution rate is %q after the edit, want \"850\"", got)
	}
}

// --- the type guards --------------------------------------------------------

// Every generic screen must refuse an exchange, and the exchange screen must
// refuse everything else. The guards name what they ACCEPT (TD-045); this is the
// test that fails if one of them is rewritten as a list of exclusions.
func TestExchangeCannotBeEditedThroughAnotherScreen(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ves, usd, efectivo := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)
	ctx := context.Background()

	exchange, err := fs.RecordExchange(ctx, RecordExchangeInput{
		SourceAccountID: ves.ID, SourceAmount: "85000.00",
		DestinationAccountID: usd.ID, DestinationAmount: "100.00",
		EffectiveDate: "2026-09-16",
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}
	id := strconv.FormatInt(exchange.ID, 10)

	// The generic edit form redirects rather than rendering an income form over
	// a two-currency transaction.
	req := httptest.NewRequest(http.MethodGet, "/transactions/"+id+"/edit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("the generic edit form returned %d for an exchange, want 303", res.StatusCode)
	}
	if location := res.Header.Get("Location"); location != "/exchanges/"+id+"/edit" {
		t.Errorf("it redirected to %q, want the exchange form", location)
	}
	_ = bodyOf(t, res)

	// And the generic update refuses outright.
	res = postForm(t, router, "/transactions/"+id, url.Values{
		"account_id": {strconv.FormatInt(ves.ID, 10)}, "type": {"EXPENSE"},
		"category_id": {"1"}, "payment_method_id": {"1"},
		"amount": {"85000.00"}, "effective_date": {"2026-09-16"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("the generic update returned %d for an exchange, want 400", res.StatusCode)
	}
	_ = bodyOf(t, res)

	// The transfer update refuses it too.
	res = postForm(t, router, "/transfers/"+id, url.Values{
		"source_account_id":      {strconv.FormatInt(ves.ID, 10)},
		"destination_account_id": {strconv.FormatInt(efectivo.ID, 10)},
		"amount":                 {"85000.00"}, "effective_date": {"2026-09-16"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("the transfer update returned %d for an exchange, want 400", res.StatusCode)
	}
	_ = bodyOf(t, res)

	// Nothing changed.
	after, err := fs.GetTransaction(ctx, exchange.ID)
	if err != nil {
		t.Fatalf("reading the exchange: %v", err)
	}
	if after.Type != TransactionTypeFXExchange || after.Exchange == nil ||
		after.Exchange.SourceAmountMinor != 8500000 || after.Exchange.DestinationAmountMinor != 10000 {
		t.Errorf("the exchange was altered: %+v", after)
	}
}

func TestTheExchangeScreenRefusesEveryOtherType(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	banesco, usd, efectivo := setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)
	ctx := context.Background()

	transfer, err := fs.RecordTransfer(ctx, RecordTransferInput{
		SourceAccountID: banesco.ID, DestinationAccountID: efectivo.ID,
		Amount: "500.00", EffectiveDate: "2026-09-16",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	id := strconv.FormatInt(transfer.ID, 10)

	req := httptest.NewRequest(http.MethodGet, "/exchanges/"+id+"/edit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("the exchange form returned %d for a transfer, want 400", res.StatusCode)
	}
	_ = bodyOf(t, res)

	res = postForm(t, router, "/exchanges/"+id, url.Values{
		"source_account_id": {strconv.FormatInt(banesco.ID, 10)}, "source_amount": {"85000.00"},
		"destination_account_id": {strconv.FormatInt(usd.ID, 10)}, "destination_amount": {"100.00"},
		"effective_date": {"2026-09-16"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("the exchange update returned %d for a transfer, want 400", res.StatusCode)
	}
	_ = bodyOf(t, res)
}

// --- the listing ------------------------------------------------------------

func TestTheTransactionListShowsBothSidesOfAnExchange(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ves, usd, _ := setupExchangeAccounts(t, fs)
	ctx := context.Background()

	if _, err := fs.RecordExchange(ctx, RecordExchangeInput{
		SourceAccountID: ves.ID, SourceAmount: "84220.67",
		DestinationAccountID: usd.ID, DestinationAmount: "100.00",
		EffectiveDate: "2026-09-16",
	}); err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	router := testRouter(t, fs)

	for _, path := range []string{"/transactions", "/mobile/movimientos"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s returned %d, want 200", path, res.StatusCode)
			}
			body := bodyOf(t, res)

			// Both amounts, both currency codes, and the rate. None of them is
			// derivable from the others by anything the interface may do.
			for _, want := range []string{"84,220.67", "VES", "100.00", "USD", "842.2067"} {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s does not show %q", path, want)
				}
			}
		})
	}
}

// --- the mobile operation form ----------------------------------------------

// The mobile form posts to four endpoints with four sets of field names, and
// which one it uses must be right BEFORE any script runs.
//
// This is the regression test for the bug M5.5 found: the template hard-coded
// the income shape and a script rewrote it on the first tab click, so opening
// the screen with ?type=TRANSFER gave a form that would have posted a transfer
// to /transactions.
func TestTheMobileOperationFormIsRenderedForTheTypeItWasAskedFor(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	setupExchangeAccounts(t, fs)
	router := testRouter(t, fs)

	cases := map[string]struct {
		action      string
		sourceField string
		amountField string
	}{
		"EXPENSE":     {"/transactions", "account_id", "amount"},
		"INCOME":      {"/transactions", "account_id", "amount"},
		"TRANSFER":    {"/transfers", "source_account_id", "amount"},
		"FX_EXCHANGE": {"/exchanges", "source_account_id", "source_amount"},
	}

	for opType, want := range cases {
		t.Run(opType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/mobile/operacion/nueva?type="+opType, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("returned %d, want 200", res.StatusCode)
			}
			body := bodyOf(t, res)

			if !strings.Contains(body, `action="`+want.action+`"`) {
				t.Errorf("the form does not post to %s", want.action)
			}
			if !strings.Contains(body, `name="`+want.sourceField+`"`) {
				t.Errorf("the account select is not called %s", want.sourceField)
			}
			if !strings.Contains(body, `name="`+want.amountField+`"`) {
				t.Errorf("the amount input is not called %s", want.amountField)
			}
			if opType == "FX_EXCHANGE" && !strings.Contains(body, `name="destination_amount"`) {
				t.Error("the exchange form has no second amount")
			}
		})
	}
}

// The script switches the form between the four shapes without a round trip, so
// it carries its own copy of the table the server renders from. Two copies that
// disagree would send an exchange to /transfers on the second click; this reads
// the script and checks them against each other.
func TestTheMobileScriptAgreesWithTheServerAboutEveryOperationShape(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("../../web/static/js/mobile.js")
	if err != nil {
		t.Fatalf("reading mobile.js: %v", err)
	}
	script := string(source)

	for opType, shape := range mobileOperationShapes() {
		t.Run(opType, func(t *testing.T) {
			block := shapeBlock(t, script, opType)

			for field, want := range map[string]string{
				"action":      shape.Action,
				"sourceField": shape.SourceFieldName,
				"amountField": shape.AmountFieldName,
				"originLabel": shape.OriginLabel,
				"submitLabel": shape.SubmitLabel,
				"submitClass": shape.SubmitClass,
			} {
				if got := jsStringField(t, block, field); got != want {
					t.Errorf("mobile.js has %s: %q for %s, the server says %q", field, got, opType, want)
				}
			}

			for field, want := range map[string]bool{
				"category":          shape.ShowCategory,
				"method":            shape.ShowPaymentMethod,
				"destination":       shape.ShowDestination,
				"destinationAmount": shape.ShowDestinationAmount,
			} {
				if got := jsBoolField(t, block, field); got != want {
					t.Errorf("mobile.js has %s: %v for %s, the server says %v", field, got, opType, want)
				}
			}
		})
	}
}

// shapeBlock extracts one entry of OPERATION_SHAPES from the script.
func shapeBlock(t *testing.T, script, opType string) string {
	t.Helper()

	start := strings.Index(script, opType+": {")
	if start < 0 {
		t.Fatalf("mobile.js has no OPERATION_SHAPES entry for %s", opType)
	}
	end := strings.Index(script[start:], "}")
	if end < 0 {
		t.Fatalf("the %s entry in mobile.js is not closed", opType)
	}
	return script[start : start+end]
}

func jsStringField(t *testing.T, block, field string) string {
	t.Helper()

	match := regexp.MustCompile(field + `: '([^']*)'`).FindStringSubmatch(block)
	if match == nil {
		t.Fatalf("the shape has no %s", field)
	}
	return match[1]
}

func jsBoolField(t *testing.T, block, field string) bool {
	t.Helper()

	match := regexp.MustCompile(field + `: (true|false)`).FindStringSubmatch(block)
	if match == nil {
		t.Fatalf("the shape has no %s", field)
	}
	return match[1] == "true"
}
