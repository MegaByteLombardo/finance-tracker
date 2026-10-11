package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testRouter sets up a router using embedded templates
func testRouter(t *testing.T, fs *FakeServices) http.Handler {
	t.Helper()

	cfg := DefaultConfig()
	logger := slog.New(slog.DiscardHandler)
	return NewRouter(logger, fs.BuildServices(), cfg)
}

// 1. Dashboard vacío
func TestDashboardEmpty(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	fs.Reset()
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "No accounts yet") {
		t.Errorf("expected empty accounts state in body")
	}
	if !strings.Contains(bodyStr, "No transactions yet") {
		t.Errorf("expected empty transactions state in body")
	}
	if !strings.Contains(bodyStr, "Create at least one income and expense category") {
		t.Errorf("expected missing categories prompt in body")
	}
}

// 2. Dashboard con cuentas y transacciones
func TestDashboardWithAccounts(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	if err := fs.SeedSampleData(ctx); err != nil {
		t.Fatalf("seeding sample data: %v", err)
	}

	// Add Banesco VES account
	subSavings := AccountSubtypeSavings
	inst := "Banesco"
	bal := "10000.00"
	_, err := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name:         "Banesco VES",
			Type:         AccountTypeBank,
			Subtype:      &subSavings,
			CurrencyCode: "VES",
			Institution:  &inst,
		},
		OpeningBalance:     &bal,
		OpeningBalanceDate: TodayDate(),
	})
	if err != nil {
		t.Fatalf("opening banesco account: %v", err)
	}

	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	// USD account balance: 1000 + 500 - 25 = 1,475.00
	if !strings.Contains(bodyStr, "1,475.00") {
		t.Errorf("expected USD balance 1,475.00 in dashboard, body: %s", bodyStr)
	}
	// VES account balance: 10,000.00
	if !strings.Contains(bodyStr, "10,000.00") {
		t.Errorf("expected VES balance 10,000.00 in dashboard")
	}

	// The currencies are still grouped separately and their balances are still
	// the ledger's own figures. M5 added a consolidated VALUATION above them; it
	// did not start summing balances across currencies, and the page has to keep
	// saying which of the two figures is which.
	if !strings.Contains(bodyStr, "nunca se suman entre monedas") {
		t.Errorf("the dashboard no longer states that balances are not summed across currencies")
	}
	if !strings.Contains(bodyStr, "Patrimonio neto") {
		t.Errorf("the dashboard does not show the consolidated net worth")
	}

	// Verify recent transactions appear
	if !strings.Contains(bodyStr, "Salary") {
		t.Errorf("expected Salary category in recent transactions")
	}
	if !strings.Contains(bodyStr, "Supermarket") {
		t.Errorf("expected Supermarket description in recent transactions")
	}
}

// 3. Account form BANK muestra subtype y CASH lo oculta
func TestAccountFormBankSubtype(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	// For BANK: subtype selector should be rendered
	req := httptest.NewRequest(http.MethodGet, "/accounts/subtype-options?type=BANK", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "CHECKING") || !strings.Contains(bodyStr, "SAVINGS") {
		t.Errorf("expected CHECKING and SAVINGS options for BANK account type, got: %s", bodyStr)
	}

	// For CASH: subtype selector should NOT be rendered
	req2 := httptest.NewRequest(http.MethodGet, "/accounts/subtype-options?type=CASH", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	res2 := rec2.Result()
	defer res2.Body.Close()

	body2, _ := io.ReadAll(res2.Body)
	bodyStr2 := string(body2)

	if strings.Contains(bodyStr2, "CHECKING") || strings.Contains(bodyStr2, "SAVINGS") {
		t.Errorf("expected no subtype options for CASH, got: %s", bodyStr2)
	}
}

// 4. Validación de forms (422 Unprocessable Entity)
func TestAccountFormValidation(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	// Post invalid account: BANK without subtype, missing name
	formData := url.Values{
		"name":          {""},
		"type":          {"BANK"},
		"subtype":       {""},
		"currency_code": {"USD"},
	}

	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Account name is required") {
		t.Errorf("expected account name error, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Bank accounts require a subtype") {
		t.Errorf("expected subtype error, got: %s", bodyStr)
	}
}

// 5. Currency visible/read-only derived from account
func TestCurrencyDisplayReadOnly(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	sub := AccountSubtypeChecking
	acct, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Test Checking",
		Type:         AccountTypeBank,
		Subtype:      &sub,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/transactions/account-info?account_id=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "USD") {
		t.Errorf("expected USD currency in account-info fragment")
	}
	if !strings.Contains(bodyStr, "readonly") {
		t.Errorf("expected readonly attribute on currency field")
	}
	_ = acct
}

// 6. Income form & 7. Expense form
func TestIncomeAndExpenseForms(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	_, _ = fs.CreateCategory(ctx, CreateCategoryInput{Name: "Salary", Type: CategoryTypeIncome})
	_, _ = fs.CreateCategory(ctx, CreateCategoryInput{Name: "Rent", Type: CategoryTypeExpense})

	router := testRouter(t, fs)

	// Income form
	reqIncome := httptest.NewRequest(http.MethodGet, "/transactions/new?type=INCOME", nil)
	recIncome := httptest.NewRecorder()
	router.ServeHTTP(recIncome, reqIncome)

	resIncome := recIncome.Result()
	defer resIncome.Body.Close()

	if resIncome.StatusCode != http.StatusOK {
		t.Fatalf("income form expected 200, got %d", resIncome.StatusCode)
	}

	bodyIncome, _ := io.ReadAll(resIncome.Body)
	bodyIncomeStr := string(bodyIncome)

	if !strings.Contains(bodyIncomeStr, "active-income") {
		t.Errorf("expected active-income tab selected")
	}
	if !strings.Contains(bodyIncomeStr, "Salary") {
		t.Errorf("expected Salary category in income form")
	}

	// Expense form
	reqExpense := httptest.NewRequest(http.MethodGet, "/transactions/new?type=EXPENSE", nil)
	recExpense := httptest.NewRecorder()
	router.ServeHTTP(recExpense, reqExpense)

	resExpense := recExpense.Result()
	defer resExpense.Body.Close()

	if resExpense.StatusCode != http.StatusOK {
		t.Fatalf("expense form expected 200, got %d", resExpense.StatusCode)
	}

	bodyExpense, _ := io.ReadAll(resExpense.Body)
	bodyExpenseStr := string(bodyExpense)

	if !strings.Contains(bodyExpenseStr, "active-expense") {
		t.Errorf("expected active-expense tab selected")
	}
	if !strings.Contains(bodyExpenseStr, "Rent") {
		t.Errorf("expected Rent category in expense form")
	}
}

// 8. Category filtering
func TestCategoryFiltering(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	_, _ = fs.CreateCategory(ctx, CreateCategoryInput{Name: "Dividend", Type: CategoryTypeIncome})
	_, _ = fs.CreateCategory(ctx, CreateCategoryInput{Name: "Electricity", Type: CategoryTypeExpense})

	router := testRouter(t, fs)

	// Query INCOME categories options
	req := httptest.NewRequest(http.MethodGet, "/transactions/category-options?type=INCOME", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Dividend") {
		t.Errorf("expected Dividend in income category options")
	}
	if strings.Contains(bodyStr, "Electricity") {
		t.Errorf("did not expect Electricity in income category options")
	}
}

// 9. Backend error -> mensaje amigable
func TestBackendErrorFriendlyMessage(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	sub := AccountSubtypeChecking
	acct, _ := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Main Bank",
		Type:         AccountTypeBank,
		Subtype:      &sub,
		CurrencyCode: "USD",
	})
	cat, _ := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Food",
		Type: CategoryTypeExpense,
	})

	router := testRouter(t, fs)

	// Post expense with negative amount (must be positive string in backend)
	formData := url.Values{
		"type":              {"EXPENSE"},
		"account_id":        {strings.TrimSpace(string(rune('0' + acct.ID)))},
		"category_id":       {strings.TrimSpace(string(rune('0' + cat.ID)))},
		"amount":            {"-25.00"},
		"payment_method_id": {"2"},
		"effective_date":    {"2026-09-05"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Amount must be positive") {
		t.Errorf("expected friendly error message about positive amount, got: %s", bodyStr)
	}
}

// 10. PRG en submit normal
func TestPRGOnNormalSubmit(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	formData := url.Values{
		"name": {"Freelance"},
		"type": {"INCOME"},
	}

	req := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other for PRG, got %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/categories" {
		t.Errorf("expected Location /categories, got %q", loc)
	}
}

// 11. htmx request -> fragment o HX-Redirect
func TestHTMXRequestResponse(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	// 1. Partial fragment request
	reqFragment := httptest.NewRequest(http.MethodGet, "/accounts/subtype-options?type=BANK", nil)
	reqFragment.Header.Set("HX-Request", "true")
	recFragment := httptest.NewRecorder()

	router.ServeHTTP(recFragment, reqFragment)
	resFragment := recFragment.Result()
	defer resFragment.Body.Close()

	bodyFrag, _ := io.ReadAll(resFragment.Body)
	bodyFragStr := string(bodyFrag)

	if strings.Contains(bodyFragStr, "<!DOCTYPE html>") {
		t.Errorf("htmx partial request must not contain <!DOCTYPE html>")
	}
	if !strings.Contains(bodyFragStr, "Account Subtype") {
		t.Errorf("expected Account Subtype fragment content")
	}

	// 2. Form submission with HX-Request sets HX-Redirect
	formData := url.Values{
		"name": {"Consulting"},
		"type": {"INCOME"},
	}
	reqSubmit := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(formData.Encode()))
	reqSubmit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSubmit.Header.Set("HX-Request", "true")
	recSubmit := httptest.NewRecorder()

	router.ServeHTTP(recSubmit, reqSubmit)
	resSubmit := recSubmit.Result()
	defer resSubmit.Body.Close()

	if hxRedirect := resSubmit.Header.Get("HX-Redirect"); hxRedirect != "/categories" {
		t.Errorf("expected HX-Redirect: /categories, got %q", hxRedirect)
	}
}

// 12. HTML escaping correcto
func TestHTMLEscaping(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	_, err := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "<script>alert('xss')</script>",
		Type: CategoryTypeExpense,
	})
	if err != nil {
		t.Fatalf("creating category: %v", err)
	}

	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/categories", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "<script>alert('xss')</script>") {
		t.Errorf("raw XSS script unescaped in HTML output!")
	}
	if !strings.Contains(bodyStr, "&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;") {
		t.Errorf("expected escaped HTML entity in output, got: %s", bodyStr)
	}
}

// 13. No SQL in presentation layer
func TestNoSQLInPresentationLayer(t *testing.T) {
	t.Parallel()

	webFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing web files: %v", err)
	}

	for _, file := range webFiles {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading file %s: %v", file, err)
		}
		contentStr := string(content)

		// Disallow SQL queries
		sqlKeywords := []string{"SELECT ", "INSERT INTO", "UPDATE ", "DELETE FROM", "*sql.DB"}
		for _, kw := range sqlKeywords {
			if strings.Contains(contentStr, kw) {
				t.Errorf("file %s contains SQL / raw DB access: %s", file, kw)
			}
		}
	}
}

// 14. Smoke test: Full workflow as requested in Section 20
// Create Income Category -> Create Expense Category -> Create Mercantil USD (BANK/CHECKING, initial $1000)
// -> Record income +$500 -> Record expense -$25 -> Dashboard shows USD 1475.00
// -> Create Banesco VES (BANK/SAVINGS, initial Bs. 10000) -> Dashboard shows USD and VES independently, not summed.
func TestSmokeEndToEndFlow(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	fs.Reset()
	router := testRouter(t, fs)

	postForm := func(path string, data url.Values) *http.Response {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(data.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Result()
	}

	getHTML := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		res := rec.Result()
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	// 1. Create Income Category
	resCatInc := postForm("/categories", url.Values{
		"name": {"Salary"},
		"type": {"INCOME"},
	})
	if resCatInc.StatusCode != http.StatusSeeOther {
		t.Fatalf("create income category: expected 303, got %d", resCatInc.StatusCode)
	}

	// 2. Create Expense Category
	resCatExp := postForm("/categories", url.Values{
		"name": {"Groceries"},
		"type": {"EXPENSE"},
	})
	if resCatExp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create expense category: expected 303, got %d", resCatExp.StatusCode)
	}

	// 3. Create Mercantil USD (BANK / CHECKING, $1000 initial balance)
	resAcctUSD := postForm("/accounts", url.Values{
		"name":                 {"Mercantil USD"},
		"type":                 {"BANK"},
		"subtype":              {"CHECKING"},
		"currency_code":        {"USD"},
		"institution":          {"Mercantil"},
		"initial_balance":      {"1000.00"},
		"initial_balance_date": {"2026-09-05"},
	})
	if resAcctUSD.StatusCode != http.StatusSeeOther {
		t.Fatalf("create mercantil account: expected 303, got %d", resAcctUSD.StatusCode)
	}

	// 4. Record income +$500
	resInc := postForm("/transactions", url.Values{
		"type":              {"INCOME"},
		"account_id":        {"1"}, // Mercantil USD is ID 1
		"category_id":       {"1"}, // Salary is ID 1
		"amount":            {"500.00"},
		"payment_method_id": {"1"},
		"effective_date":    {"2026-09-05"},
		"description":       {"Monthly wage"},
	})
	if resInc.StatusCode != http.StatusSeeOther {
		t.Fatalf("record income: expected 303, got %d", resInc.StatusCode)
	}

	// 5. Record expense -$25
	resExp := postForm("/transactions", url.Values{
		"type":              {"EXPENSE"},
		"account_id":        {"1"},
		"category_id":       {"2"}, // Groceries is ID 2
		"amount":            {"25.00"},
		"payment_method_id": {"2"},
		"effective_date":    {"2026-09-05"},
		"description":       {"Supermarket dinner"},
	})
	if resExp.StatusCode != http.StatusSeeOther {
		t.Fatalf("record expense: expected 303, got %d", resExp.StatusCode)
	}

	// 6. Verify Dashboard: must show USD 1,475.00
	dash1 := getHTML("/")
	if !strings.Contains(dash1, "1,475.00") {
		t.Errorf("dashboard must show USD 1,475.00 balance, got:\n%s", dash1)
	}
	if !strings.Contains(dash1, "Mercantil USD") {
		t.Errorf("dashboard must show Mercantil USD account card")
	}

	// 7. Create Banesco VES (BANK / SAVINGS, Bs. 10,000 initial balance - with comma)
	resAcctVES := postForm("/accounts", url.Values{
		"name":          {"Banesco VES"},
		"type":          {"BANK"},
		"subtype":       {"SAVINGS"},
		"currency_code": {"VES"},
		"institution":   {"Banesco"},
		// Canonical format: no separators. The browser groups digits for
		// display and strips them again on submit; the backend never guesses.
		"initial_balance":      {"10000.00"},
		"initial_balance_date": {"2026-09-05"},
	})
	if resAcctVES.StatusCode != http.StatusSeeOther {
		t.Fatalf("create banesco account: expected 303, got %d", resAcctVES.StatusCode)
	}

	// 8. Verify Dashboard: must show USD 1,475.00 and VES 10,000.00 independently
	dash2 := getHTML("/")
	if !strings.Contains(dash2, "1,475.00") {
		t.Errorf("dashboard must show USD 1,475.00")
	}
	if !strings.Contains(dash2, "10,000.00") {
		t.Errorf("dashboard must show VES 10,000.00")
	}
	// Each currency is its own line of the breakdown, in its own currency:
	// balances are never summed across currencies, only valued.
	if !strings.Contains(dash2, "Por moneda") {
		t.Errorf("dashboard must show the breakdown by currency")
	}
	if !strings.Contains(dash2, "<strong>USD</strong>") || !strings.Contains(dash2, "<strong>VES</strong>") {
		t.Errorf("dashboard must list USD and VES as separate currencies")
	}
}

// 14. Test static assets serving
func TestStaticAssets(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	for _, path := range []string{"/static/css/app.css", "/static/vendor/htmx.min.js", "/static/js/app.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		res := rec.Result()
		if res.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for %s, got %d", path, res.StatusCode)
		}
		res.Body.Close()
	}
}

// 15. Test formatted amount input submission with commas
// The wire format is canonical -- digits and at most one decimal point, no
// separators. The browser formats for display and strips the grouping again on
// submit; the backend deliberately does not, because stripping blindly turns
// "1,5" into "15".
func TestFormattedAmountInputSubmission(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	st := AccountSubtypeChecking
	acct, _ := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Checking USD",
		Type:         AccountTypeBank,
		Subtype:      &st,
		CurrencyCode: "USD",
	})
	cat, _ := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Salary",
		Type: CategoryTypeIncome,
	})

	router := testRouter(t, fs)

	formData := url.Values{
		"type":              {"INCOME"},
		"account_id":        {strconv.FormatInt(acct.ID, 10)},
		"category_id":       {strconv.FormatInt(cat.ID, 10)},
		"amount":            {"1250.75"},
		"effective_date":    {"2026-09-05"},
		"payment_method_id": {"1"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other for a canonical amount, got %d", res.StatusCode)
	}

	// Verify balance in dashboard shows formatted 1,250.75
	reqDash := httptest.NewRequest(http.MethodGet, "/", nil)
	recDash := httptest.NewRecorder()
	router.ServeHTTP(recDash, reqDash)

	body, _ := io.ReadAll(recDash.Result().Body)
	if !strings.Contains(string(body), "1,250.75") {
		t.Errorf("expected dashboard to show 1,250.75, got: %s", string(body))
	}
}

// A grouped amount reaching the backend means the presentation layer failed to
// canonicalise it. That is a validation failure the user is told about, not
// something to guess at.
func TestGroupedAmountIsRejectedRatherThanGuessed(t *testing.T) {
	fs := NewFakeServices()
	ctx := t.Context()

	sub := AccountSubtypeChecking
	res, _ := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name: "Mercantil USD", Type: AccountTypeBank,
			Subtype: &sub, CurrencyCode: "USD",
		},
	})
	cat, _ := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Salary", Type: CategoryTypeIncome})

	router := testRouter(t, fs)

	for _, amount := range []string{"1,250.75", "1,5"} {
		formData := url.Values{
			"type":              {"INCOME"},
			"account_id":        {strconv.FormatInt(res.Account.ID, 10)},
			"category_id":       {strconv.FormatInt(cat.ID, 10)},
			"amount":            {amount},
			"effective_date":    {"2026-09-05"},
			"payment_method_id": {"1"},
		}
		req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Result().StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("amount %q returned %d, want 422 -- a grouped amount must not be silently reinterpreted",
				amount, rec.Result().StatusCode)
		}
	}
}

// --- M3 UI Tests -------------------------------------------------------------

func TestPaymentMethodsLifecycle(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	// 1. List payment methods
	req := httptest.NewRequest(http.MethodGet, "/payment-methods", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /payment-methods, got %d", rec.Result().StatusCode)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Bank transfer") {
		t.Errorf("expected seeded 'Bank transfer' in payment methods list")
	}

	// 2. New form
	reqNew := httptest.NewRequest(http.MethodGet, "/payment-methods/new", nil)
	recNew := httptest.NewRecorder()
	router.ServeHTTP(recNew, reqNew)
	if recNew.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /payment-methods/new, got %d", recNew.Result().StatusCode)
	}

	// 3. Create "Pago móvil"
	formData := url.Values{"name": {"Pago móvil"}}
	reqCreate := httptest.NewRequest(http.MethodPost, "/payment-methods", strings.NewReader(formData.Encode()))
	reqCreate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recCreate := httptest.NewRecorder()
	router.ServeHTTP(recCreate, reqCreate)
	if recCreate.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on create, got %d", recCreate.Result().StatusCode)
	}

	// 4. Exact duplicate name -> 409 Conflict
	reqDup := httptest.NewRequest(http.MethodPost, "/payment-methods", strings.NewReader(formData.Encode()))
	reqDup.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recDup := httptest.NewRecorder()
	router.ServeHTTP(recDup, reqDup)
	if recDup.Result().StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on exact duplicate name, got %d", recDup.Result().StatusCode)
	}

	// Case sensitivity check: "pago móvil" should succeed because uniqueness is exact
	formCase := url.Values{"name": {"pago móvil"}}
	reqCase := httptest.NewRequest(http.MethodPost, "/payment-methods", strings.NewReader(formCase.Encode()))
	reqCase.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recCase := httptest.NewRecorder()
	router.ServeHTTP(recCase, reqCase)
	if recCase.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for case-distinct name 'pago móvil', got %d", recCase.Result().StatusCode)
	}

	// Find created "Pago móvil" method ID
	methods, _ := fs.ListPaymentMethods(t.Context(), ListPaymentMethodsInput{IncludeInactive: true})
	var pmID int64
	for _, m := range methods {
		if m.Name == "Pago móvil" {
			pmID = m.ID
			break
		}
	}
	if pmID == 0 {
		t.Fatalf("could not find created payment method ID")
	}

	// 5. Edit form
	reqEdit := httptest.NewRequest(http.MethodGet, "/payment-methods/"+strconv.FormatInt(pmID, 10)+"/edit", nil)
	recEdit := httptest.NewRecorder()
	router.ServeHTTP(recEdit, reqEdit)
	if recEdit.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from edit form, got %d", recEdit.Result().StatusCode)
	}
	if !strings.Contains(recEdit.Body.String(), "Renaming changes history") {
		t.Errorf("expected warning about renaming changing history in edit template")
	}

	// 6. Rename
	formRename := url.Values{"name": {"Pago Móvil P2P"}}
	reqRename := httptest.NewRequest(http.MethodPost, "/payment-methods/"+strconv.FormatInt(pmID, 10), strings.NewReader(formRename.Encode()))
	reqRename.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recRename := httptest.NewRecorder()
	router.ServeHTTP(recRename, reqRename)
	if recRename.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on rename, got %d", recRename.Result().StatusCode)
	}

	mUpdated, _ := fs.GetPaymentMethod(t.Context(), pmID)
	if mUpdated.Name != "Pago Móvil P2P" {
		t.Errorf("expected renamed to 'Pago Móvil P2P', got %q", mUpdated.Name)
	}

	// 7. Toggle active/inactive
	reqToggle := httptest.NewRequest(http.MethodPost, "/payment-methods/"+strconv.FormatInt(pmID, 10)+"/toggle", nil)
	recToggle := httptest.NewRecorder()
	router.ServeHTTP(recToggle, reqToggle)
	if recToggle.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on toggle, got %d", recToggle.Result().StatusCode)
	}

	mArchived, _ := fs.GetPaymentMethod(t.Context(), pmID)
	if mArchived.IsActive {
		t.Errorf("expected method to be archived/inactive after toggle")
	}
}

func TestEditAccount(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	sub := AccountSubtypeChecking
	res, err := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name:         "Banesco Checking",
			Type:         AccountTypeBank,
			Subtype:      &sub,
			CurrencyCode: "USD",
		},
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	router := testRouter(t, fs)

	// 1. GET /accounts/{id}/edit renders form, currency is read-only
	reqEdit := httptest.NewRequest(http.MethodGet, "/accounts/"+strconv.FormatInt(res.Account.ID, 10)+"/edit", nil)
	recEdit := httptest.NewRecorder()
	router.ServeHTTP(recEdit, reqEdit)
	if recEdit.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /accounts/{id}/edit, got %d", recEdit.Result().StatusCode)
	}
	bodyStr := recEdit.Body.String()
	if !strings.Contains(bodyStr, "Banesco Checking") {
		t.Errorf("expected account name in edit body")
	}
	if strings.Contains(bodyStr, `<select class="form-control" id="currency_code" name="currency_code"`) {
		t.Errorf("currency must not be selectable or editable in account edit form")
	}

	// 2. POST /accounts/{id} updates metadata
	formData := url.Values{
		"name":        {"Banesco Savings Main"},
		"type":        {"BANK"},
		"subtype":     {"SAVINGS"},
		"institution": {"Banesco Bank"},
	}
	reqUpdate := httptest.NewRequest(http.MethodPost, "/accounts/"+strconv.FormatInt(res.Account.ID, 10), strings.NewReader(formData.Encode()))
	reqUpdate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recUpdate := httptest.NewRecorder()
	router.ServeHTTP(recUpdate, reqUpdate)
	if recUpdate.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on account update, got %d", recUpdate.Result().StatusCode)
	}

	updated, _ := fs.GetAccount(ctx, res.Account.ID)
	if updated.Name != "Banesco Savings Main" {
		t.Errorf("expected name 'Banesco Savings Main', got %q", updated.Name)
	}
	if updated.Subtype == nil || *updated.Subtype != AccountSubtypeSavings {
		t.Errorf("expected subtype SAVINGS, got %+v", updated.Subtype)
	}
	if updated.Institution == nil || *updated.Institution != "Banesco Bank" {
		t.Errorf("expected institution 'Banesco Bank', got %+v", updated.Institution)
	}
	if updated.CurrencyCode != "USD" {
		t.Errorf("currency must remain USD, got %q", updated.CurrencyCode)
	}
}

func TestEditAccountSubtypeInvariants(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	sub := AccountSubtypeChecking
	res, err := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name:         "Bank Test Acct",
			Type:         AccountTypeBank,
			Subtype:      &sub,
			CurrencyCode: "USD",
		},
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}

	router := testRouter(t, fs)
	acctID := strconv.FormatInt(res.Account.ID, 10)

	// 1. Reclassify BANK -> CASH with non-empty subtype should fail with 422
	formBadCash := url.Values{
		"name":    {"Cash Box"},
		"type":    {"CASH"},
		"subtype": {"CHECKING"},
	}
	reqBadCash := httptest.NewRequest(http.MethodPost, "/accounts/"+acctID, strings.NewReader(formBadCash.Encode()))
	reqBadCash.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recBadCash := httptest.NewRecorder()
	router.ServeHTTP(recBadCash, reqBadCash)
	if recBadCash.Result().StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when reclassifying to CASH with subtype, got %d", recBadCash.Result().StatusCode)
	}

	// 2. Reclassify BANK -> CASH with empty subtype succeeds
	formGoodCash := url.Values{
		"name":    {"Cash Box"},
		"type":    {"CASH"},
		"subtype": {""},
	}
	reqGoodCash := httptest.NewRequest(http.MethodPost, "/accounts/"+acctID, strings.NewReader(formGoodCash.Encode()))
	reqGoodCash.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recGoodCash := httptest.NewRecorder()
	router.ServeHTTP(recGoodCash, reqGoodCash)
	if recGoodCash.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 when reclassifying to CASH with empty subtype, got %d", recGoodCash.Result().StatusCode)
	}

	// 3. Reclassify CASH -> BANK without subtype should fail with 422
	formBadBank := url.Values{
		"name":    {"Bank Again"},
		"type":    {"BANK"},
		"subtype": {""},
	}
	reqBadBank := httptest.NewRequest(http.MethodPost, "/accounts/"+acctID, strings.NewReader(formBadBank.Encode()))
	reqBadBank.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recBadBank := httptest.NewRecorder()
	router.ServeHTTP(recBadBank, reqBadBank)
	if recBadBank.Result().StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when reclassifying to BANK with empty subtype, got %d", recBadBank.Result().StatusCode)
	}

	// 4. Reclassify CASH -> BANK with valid subtype succeeds
	formGoodBank := url.Values{
		"name":    {"Bank Again"},
		"type":    {"BANK"},
		"subtype": {"SAVINGS"},
	}
	reqGoodBank := httptest.NewRequest(http.MethodPost, "/accounts/"+acctID, strings.NewReader(formGoodBank.Encode()))
	reqGoodBank.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recGoodBank := httptest.NewRecorder()
	router.ServeHTTP(recGoodBank, reqGoodBank)
	if recGoodBank.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 when reclassifying to BANK with SAVINGS subtype, got %d", recGoodBank.Result().StatusCode)
	}
}

func TestEditCategory(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	cat, err := fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Food & Diners",
		Type: CategoryTypeExpense,
	})
	if err != nil {
		t.Fatalf("creating category: %v", err)
	}
	_, _ = fs.CreateCategory(ctx, CreateCategoryInput{
		Name: "Utilities",
		Type: CategoryTypeExpense,
	})

	router := testRouter(t, fs)
	catID := strconv.FormatInt(cat.ID, 10)

	// 1. GET /categories/{id}/edit shows form with read-only type
	reqEdit := httptest.NewRequest(http.MethodGet, "/categories/"+catID+"/edit", nil)
	recEdit := httptest.NewRecorder()
	router.ServeHTTP(recEdit, reqEdit)
	if recEdit.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from edit category form, got %d", recEdit.Result().StatusCode)
	}
	if strings.Contains(recEdit.Body.String(), `<select class="form-control" id="type" name="type"`) {
		t.Errorf("category type must not be editable in edit form")
	}

	// 2. Rename category succeeds
	formRename := url.Values{"name": {"Groceries & Market"}}
	reqRename := httptest.NewRequest(http.MethodPost, "/categories/"+catID, strings.NewReader(formRename.Encode()))
	reqRename.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recRename := httptest.NewRecorder()
	router.ServeHTTP(recRename, reqRename)
	if recRename.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on rename, got %d", recRename.Result().StatusCode)
	}

	updated, _ := fs.GetCategory(ctx, cat.ID)
	if updated.Name != "Groceries & Market" {
		t.Errorf("expected renamed name 'Groceries & Market', got %q", updated.Name)
	}
	if updated.Type != CategoryTypeExpense {
		t.Errorf("expected type EXPENSE preserved, got %q", updated.Type)
	}

	// 3. Rename to case-insensitive duplicate ("utilities") gives 409 Conflict
	formDup := url.Values{"name": {"utilities"}}
	reqDup := httptest.NewRequest(http.MethodPost, "/categories/"+catID, strings.NewReader(formDup.Encode()))
	reqDup.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recDup := httptest.NewRecorder()
	router.ServeHTTP(recDup, reqDup)
	if recDup.Result().StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate category name, got %d", recDup.Result().StatusCode)
	}
}

func TestEditTransactionRegularAndTypeSwitching(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()

	sub := AccountSubtypeChecking
	res, _ := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name: "Checking USD", Type: AccountTypeBank,
			Subtype: &sub, CurrencyCode: "USD",
		},
	})
	catFood, _ := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Food", Type: CategoryTypeExpense})
	catSalary, _ := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Salary", Type: CategoryTypeIncome})

	desc := "Lunch"
	tx, err := fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:       res.Account.ID,
		CategoryID:      catFood.ID,
		Amount:          "25.00",
		PaymentMethodID: 1,
		Description:     &desc,
		EffectiveDate:   "2026-09-05",
	})
	if err != nil {
		t.Fatalf("recording expense: %v", err)
	}

	router := testRouter(t, fs)
	txID := strconv.FormatInt(tx.ID, 10)

	// 1. GET /transactions/{id}/edit shows positive amount 25.00
	reqEdit := httptest.NewRequest(http.MethodGet, "/transactions/"+txID+"/edit", nil)
	recEdit := httptest.NewRecorder()
	router.ServeHTTP(recEdit, reqEdit)
	if recEdit.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from edit transaction form, got %d", recEdit.Result().StatusCode)
	}
	bodyStr := recEdit.Body.String()
	if !strings.Contains(bodyStr, `value="25.00"`) {
		t.Errorf("expected positive amount 25.00 in edit form")
	}

	// 2. Edit regular expense fields (amount, description, date)
	formUpdate := url.Values{
		"type":              {"EXPENSE"},
		"account_id":        {strconv.FormatInt(res.Account.ID, 10)},
		"category_id":       {strconv.FormatInt(catFood.ID, 10)},
		"payment_method_id": {"2"},
		"amount":            {"35.50"},
		"description":       {"Expensive lunch"},
		"effective_date":    {"2026-09-06"},
	}
	reqUpdate := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formUpdate.Encode()))
	reqUpdate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recUpdate := httptest.NewRecorder()
	router.ServeHTTP(recUpdate, reqUpdate)
	if recUpdate.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on transaction update, got %d", recUpdate.Result().StatusCode)
	}

	txUpdated, _ := fs.GetTransaction(ctx, tx.ID)
	if txUpdated.AmountFormatted != "-35.50" {
		t.Errorf("expected formatted amount -35.50, got %q", txUpdated.AmountFormatted)
	}
	if txUpdated.Description == nil || *txUpdated.Description != "Expensive lunch" {
		t.Errorf("expected description 'Expensive lunch', got %+v", txUpdated.Description)
	}

	// 3. Switch EXPENSE -> INCOME
	formSwitch := url.Values{
		"type":              {"INCOME"},
		"account_id":        {strconv.FormatInt(res.Account.ID, 10)},
		"category_id":       {strconv.FormatInt(catSalary.ID, 10)},
		"payment_method_id": {"1"},
		"amount":            {"100.00"},
		"effective_date":    {"2026-09-06"},
	}
	reqSwitch := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formSwitch.Encode()))
	reqSwitch.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recSwitch := httptest.NewRecorder()
	router.ServeHTTP(recSwitch, reqSwitch)
	if recSwitch.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on EXPENSE -> INCOME switch, got %d", recSwitch.Result().StatusCode)
	}

	txIncome, _ := fs.GetTransaction(ctx, tx.ID)
	if txIncome.Type != TransactionTypeIncome {
		t.Errorf("expected type INCOME, got %q", txIncome.Type)
	}
	if txIncome.AmountFormatted != "100.00" {
		t.Errorf("expected positive formatted amount 100.00, got %q", txIncome.AmountFormatted)
	}

	// 4. Category type mismatch rejected with 422
	formMismatch := url.Values{
		"type":              {"INCOME"},
		"account_id":        {strconv.FormatInt(res.Account.ID, 10)},
		"category_id":       {strconv.FormatInt(catFood.ID, 10)}, // EXPENSE category with INCOME type
		"payment_method_id": {"1"},
		"amount":            {"50.00"},
		"effective_date":    {"2026-09-06"},
	}
	reqMismatch := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formMismatch.Encode()))
	reqMismatch.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recMismatch := httptest.NewRecorder()
	router.ServeHTTP(recMismatch, reqMismatch)
	if recMismatch.Result().StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on category mismatch, got %d", recMismatch.Result().StatusCode)
	}

	// 5. Switching to OPENING_BALANCE rejected with 422
	formOpen := url.Values{
		"type":              {"OPENING_BALANCE"},
		"account_id":        {strconv.FormatInt(res.Account.ID, 10)},
		"category_id":       {strconv.FormatInt(catSalary.ID, 10)},
		"payment_method_id": {"1"},
		"amount":            {"50.00"},
		"effective_date":    {"2026-09-06"},
	}
	reqOpen := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formOpen.Encode()))
	reqOpen.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recOpen := httptest.NewRecorder()
	router.ServeHTTP(recOpen, reqOpen)
	if recOpen.Result().StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on attempt to switch to OPENING_BALANCE, got %d", recOpen.Result().StatusCode)
	}
}

func TestEditTransactionCurrencyChangeFlow(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()

	sub := AccountSubtypeChecking
	resUSD, _ := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{Name: "USD Account", Type: AccountTypeBank, Subtype: &sub, CurrencyCode: "USD"},
	})
	resEUR, _ := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{Name: "EUR Account", Type: AccountTypeBank, Subtype: &sub, CurrencyCode: "EUR"},
	})

	catFood, _ := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Food", Type: CategoryTypeExpense})

	desc := "Dinner"
	tx, err := fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:       resUSD.Account.ID,
		CategoryID:      catFood.ID,
		Amount:          "50.00",
		PaymentMethodID: 1,
		Description:     &desc,
		EffectiveDate:   "2026-09-05",
	})
	if err != nil {
		t.Fatalf("recording expense: %v", err)
	}

	router := testRouter(t, fs)
	txID := strconv.FormatInt(tx.ID, 10)

	// 1. Move to EUR account without allow_currency_change -> 409 Conflict with confirmation warning
	formNoConfirm := url.Values{
		"type":              {"EXPENSE"},
		"account_id":        {strconv.FormatInt(resEUR.Account.ID, 10)},
		"category_id":       {strconv.FormatInt(catFood.ID, 10)},
		"payment_method_id": {"1"},
		"amount":            {"50.00"},
		"effective_date":    {"2026-09-05"},
	}
	reqNoConfirm := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formNoConfirm.Encode()))
	reqNoConfirm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recNoConfirm := httptest.NewRecorder()
	router.ServeHTTP(recNoConfirm, reqNoConfirm)

	if recNoConfirm.Result().StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for unconfirmed currency change, got %d", recNoConfirm.Result().StatusCode)
	}
	bodyStr := recNoConfirm.Body.String()
	if !strings.Contains(bodyStr, "El importe 50.00 se interpretará como 50.00 EUR, no se convertirá") {
		t.Errorf("expected warning message explaining amount reinterpretation without conversion, got:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `<input type="hidden" name="allow_currency_change" value="true">`) {
		t.Errorf("expected hidden allow_currency_change=true input in confirmation response")
	}

	// 2. Submit with allow_currency_change=true -> succeeds with 303
	formConfirm := url.Values{
		"type":                  {"EXPENSE"},
		"account_id":            {strconv.FormatInt(resEUR.Account.ID, 10)},
		"category_id":           {strconv.FormatInt(catFood.ID, 10)},
		"payment_method_id":     {"1"},
		"amount":                {"50.00"},
		"effective_date":        {"2026-09-05"},
		"allow_currency_change": {"true"},
	}
	reqConfirm := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formConfirm.Encode()))
	reqConfirm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recConfirm := httptest.NewRecorder()
	router.ServeHTTP(recConfirm, reqConfirm)

	if recConfirm.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on confirmed currency change, got %d", recConfirm.Result().StatusCode)
	}

	txUpdated, _ := fs.GetTransaction(ctx, tx.ID)
	if txUpdated.AccountID != resEUR.Account.ID {
		t.Errorf("expected account ID %d, got %d", resEUR.Account.ID, txUpdated.AccountID)
	}
	if txUpdated.CurrencyCode != "EUR" {
		t.Errorf("expected currency EUR, got %s", txUpdated.CurrencyCode)
	}
}

func TestEditOpeningBalance(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()

	sub := AccountSubtypeChecking
	initialBal := "100.00"
	openRes, err := fs.OpenAccount(ctx, OpenAccountInput{
		Account: CreateAccountInput{
			Name: "Initial Balance Acct", Type: AccountTypeBank,
			Subtype: &sub, CurrencyCode: "USD",
		},
		OpeningBalance:     &initialBal,
		OpeningBalanceDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("opening account: %v", err)
	}
	if openRes.OpeningBalance == nil {
		t.Fatalf("expected non-nil opening balance transaction")
	}

	router := testRouter(t, fs)
	txID := strconv.FormatInt(openRes.OpeningBalance.ID, 10)

	// 1. GET /transactions/{id}/edit for opening balance uses edit_opening.html
	reqEdit := httptest.NewRequest(http.MethodGet, "/transactions/"+txID+"/edit", nil)
	recEdit := httptest.NewRecorder()
	router.ServeHTTP(recEdit, reqEdit)

	if recEdit.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from edit opening balance, got %d", recEdit.Result().StatusCode)
	}
	bodyStr := recEdit.Body.String()
	if !strings.Contains(bodyStr, "Edit Opening Balance") {
		t.Errorf("expected 'Edit Opening Balance' header in body")
	}
	if strings.Contains(bodyStr, `<select class="form-control" id="category_id" name="category_id"`) {
		t.Errorf("opening balance edit form must not have category selector")
	}
	if strings.Contains(bodyStr, `<select class="form-control" id="payment_method_id" name="payment_method_id"`) {
		t.Errorf("opening balance edit form must not have payment method selector")
	}

	// 2. Zero amount rejected with 422
	formZero := url.Values{
		"amount":         {"0"},
		"effective_date": {"2026-09-02"},
	}
	reqZero := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formZero.Encode()))
	reqZero.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recZero := httptest.NewRecorder()
	router.ServeHTTP(recZero, reqZero)

	if recZero.Result().StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when editing opening balance to 0, got %d", recZero.Result().StatusCode)
	}

	// 3. Valid update succeeds
	formValid := url.Values{
		"amount":         {"250.00"},
		"effective_date": {"2026-09-02"},
	}
	reqValid := httptest.NewRequest(http.MethodPost, "/transactions/"+txID, strings.NewReader(formValid.Encode()))
	reqValid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recValid := httptest.NewRecorder()
	router.ServeHTTP(recValid, reqValid)

	if recValid.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on opening balance update, got %d", recValid.Result().StatusCode)
	}

	txUpdated, _ := fs.GetTransaction(ctx, openRes.OpeningBalance.ID)
	if txUpdated.AmountFormatted != "250.00" {
		t.Errorf("expected amount 250.00, got %q", txUpdated.AmountFormatted)
	}
}

func TestBackupsManagement(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	// 1. GET /settings/data shows empty state initially
	reqList := httptest.NewRequest(http.MethodGet, "/settings/data", nil)
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, reqList)

	if recList.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /settings/data, got %d", recList.Result().StatusCode)
	}
	if !strings.Contains(recList.Body.String(), "No backups found") {
		t.Errorf("expected empty state when no backups exist")
	}

	// 2. POST /settings/data creates a snapshot
	reqCreate := httptest.NewRequest(http.MethodPost, "/settings/data", nil)
	recCreate := httptest.NewRecorder()
	router.ServeHTTP(recCreate, reqCreate)

	if recCreate.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on create backup, got %d", recCreate.Result().StatusCode)
	}

	// 3. GET /settings/data now lists the created backup and shows CLI restore commands
	reqList2 := httptest.NewRequest(http.MethodGet, "/settings/data", nil)
	recList2 := httptest.NewRecorder()
	router.ServeHTTP(recList2, reqList2)

	bodyStr := recList2.Body.String()
	if !strings.Contains(bodyStr, "manual") {
		t.Errorf("expected backup with reason 'manual' in list")
	}
	if !strings.Contains(bodyStr, "docker compose stop") || !strings.Contains(bodyStr, "docker compose run --rm app restore") {
		t.Errorf("expected offline restore instructions in settings page")
	}
}

func TestTransactionsListFilterControlsAndAttributes(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()
	if err := fs.SeedSampleData(ctx); err != nil {
		t.Fatalf("seeding sample data: %v", err)
	}

	router := testRouter(t, fs)
	req := httptest.NewRequest(http.MethodGet, "/transactions", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := string(bodyBytes)

	// Verify filter toolbar controls exist
	expectedElements := []string{
		`id="tx-filter-card"`,
		`id="filter-date-from"`,
		`id="filter-date-to"`,
		`id="filter-institution"`,
		`id="filter-account-name"`,
		`id="filter-account-type"`,
		`id="filter-category"`,
		`id="filter-payment-method"`,
		`id="filter-search-text"`,
		`id="tx-filter-reset-btn"`,
		`id="tx-filter-count"`,
		`id="tx-filter-empty-row"`,
	}
	for _, el := range expectedElements {
		if !strings.Contains(body, el) {
			t.Errorf("expected element %q in /transactions HTML", el)
		}
	}

	// Verify rows have filter data attributes
	expectedAttributes := []string{
		`class="tx-row"`,
		`data-date=`,
		`data-institution=`,
		`data-account-name=`,
		`data-dest-account-name=`,
		`data-account-type=`,
		`data-category=`,
		`data-payment-method=`,
		`data-search=`,
	}
	for _, attr := range expectedAttributes {
		if !strings.Contains(body, attr) {
			t.Errorf("expected attribute %q in /transactions row HTML", attr)
		}
	}
}
