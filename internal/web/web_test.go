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
	"time"
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
		OpeningBalanceDate: time.Now().UTC().Format("2006-01-02"),
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

	// Verify currencies are grouped separately and notice is present
	if !strings.Contains(bodyStr, "Finance Tracker does not convert or aggregate balances across different currencies") {
		t.Errorf("expected no-FX warning notice in body")
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
		"type":           {"EXPENSE"},
		"account_id":     {strings.TrimSpace(string(rune('0' + acct.ID)))},
		"category_id":    {strings.TrimSpace(string(rune('0' + cat.ID)))},
		"amount":         {"-25.00"},
		"payment_method": {"POS"},
		"effective_date": {"2026-09-05"},
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
		"type":           {"INCOME"},
		"account_id":     {"1"}, // Mercantil USD is ID 1
		"category_id":    {"1"}, // Salary is ID 1
		"amount":         {"500.00"},
		"payment_method": {"BANK_TRANSFER"},
		"effective_date": {"2026-09-05"},
		"description":    {"Monthly wage"},
	})
	if resInc.StatusCode != http.StatusSeeOther {
		t.Fatalf("record income: expected 303, got %d", resInc.StatusCode)
	}

	// 5. Record expense -$25
	resExp := postForm("/transactions", url.Values{
		"type":           {"EXPENSE"},
		"account_id":     {"1"},
		"category_id":    {"2"}, // Groceries is ID 2
		"amount":         {"25.00"},
		"payment_method": {"POS"},
		"effective_date": {"2026-09-05"},
		"description":    {"Supermarket dinner"},
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
		"name":                 {"Banesco VES"},
		"type":                 {"BANK"},
		"subtype":              {"SAVINGS"},
		"currency_code":        {"VES"},
		"institution":          {"Banesco"},
		"initial_balance":      {"10,000.00"},
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
	if !strings.Contains(dash2, "Total USD:") {
		t.Errorf("dashboard must show Total USD section")
	}
	if !strings.Contains(dash2, "Total VES:") {
		t.Errorf("dashboard must show Total VES section")
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
		"type":           {"INCOME"},
		"account_id":     {strconv.FormatInt(acct.ID, 10)},
		"category_id":    {strconv.FormatInt(cat.ID, 10)},
		"amount":         {"1,250.75"},
		"effective_date": {"2026-09-05"},
		"payment_method": {"BANK_TRANSFER"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other when submitting amount with commas, got %d", res.StatusCode)
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
