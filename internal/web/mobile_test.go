package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func seedMobileTestData(t *testing.T, fs *FakeServices) {
	t.Helper()
	ctx := context.Background()

	subChecking := AccountSubtypeChecking
	subSavings := AccountSubtypeSavings
	instMercantil := "Banco Mercantil"
	instBanesco := "Banesco Banco Universal"
	instN26 := "N26 Bank"

	usd1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Mercantil USD",
		Type:         AccountTypeBank,
		Subtype:      &subChecking,
		CurrencyCode: "USD",
		Institution:  &instMercantil,
	})
	if err != nil {
		t.Fatalf("creating usd1: %v", err)
	}

	usd2, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Efectivo en mano",
		Type:         AccountTypeCash,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating usd2: %v", err)
	}

	ves1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Banesco Corriente",
		Type:         AccountTypeBank,
		Subtype:      &subChecking,
		CurrencyCode: "VES",
		Institution:  &instBanesco,
	})
	if err != nil {
		t.Fatalf("creating ves1: %v", err)
	}

	eur1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Cuenta Ahorro Europa",
		Type:         AccountTypeBank,
		Subtype:      &subSavings,
		CurrencyCode: "EUR",
		Institution:  &instN26,
	})
	if err != nil {
		t.Fatalf("creating eur1: %v", err)
	}

	catFood, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Alimentación", Type: CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating catFood: %v", err)
	}
	catCoffee, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Café", Type: CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating catCoffee: %v", err)
	}
	catSalary, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Nómina y Sueldos", Type: CategoryTypeIncome})
	if err != nil {
		t.Fatalf("creating catSalary: %v", err)
	}

	// Opening balances
	fs.RecordOpeningBalance(ctx, RecordOpeningBalanceInput{
		AccountID: usd1.ID, Amount: "1650.00", EffectiveDate: "2026-09-01",
	})
	fs.RecordOpeningBalance(ctx, RecordOpeningBalanceInput{
		AccountID: ves1.ID, Amount: "142548.84", EffectiveDate: "2026-09-01",
	})
	fs.RecordOpeningBalance(ctx, RecordOpeningBalanceInput{
		AccountID: eur1.ID, Amount: "850.00", EffectiveDate: "2026-09-01",
	})

	// Income & Expense
	fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:       ves1.ID,
		CategoryID:      catFood.ID,
		PaymentMethodID: 1,
		Amount:          "1240.00",
		EffectiveDate:   "2026-09-06",
		Description:     OptionalString("Supermercado"),
	})
	fs.RecordIncome(ctx, RecordIncomeInput{
		AccountID:       usd1.ID,
		CategoryID:      catSalary.ID,
		PaymentMethodID: 1,
		Amount:          "850.00",
		EffectiveDate:   "2026-09-05",
		Description:     OptionalString("Nómina"),
	})
	fs.RecordExpense(ctx, RecordExpenseInput{
		AccountID:       usd2.ID,
		CategoryID:      catCoffee.ID,
		PaymentMethodID: 3,
		Amount:          "4.50",
		EffectiveDate:   "2026-09-05",
		Description:     OptionalString("Café"),
	})
}

func TestMobileDashboard(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/mobile", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	bodyStr := string(body)

	// 1. Header checks
	if !strings.Contains(bodyStr, "Hola, Andres") {
		t.Errorf("expected greeting 'Hola, Andres'")
	}

	// 2. Net Worth checks
	if !strings.Contains(bodyStr, "Patrimonio neto") {
		t.Errorf("expected label 'Patrimonio neto'")
	}
	if !strings.Contains(bodyStr, "USD") {
		t.Errorf("expected base currency 'USD'")
	}

	// 3. Multi-currency balances checks
	if !strings.Contains(bodyStr, "Balances") {
		t.Errorf("expected section title 'Balances'")
	}
	if !strings.Contains(bodyStr, "USD") {
		t.Errorf("expected USD balance")
	}
	if !strings.Contains(bodyStr, "VES") {
		t.Errorf("expected VES balance")
	}
	if !strings.Contains(bodyStr, "EUR") {
		t.Errorf("expected EUR balance")
	}
	if !strings.Contains(bodyStr, "Ver cuentas") {
		t.Errorf("expected 'Ver cuentas' link")
	}

	// 4. Recent transactions checks
	if !strings.Contains(bodyStr, "Últimos movimientos") {
		t.Errorf("expected section title 'Últimos movimientos'")
	}
	if !strings.Contains(bodyStr, "Ver todos") {
		t.Errorf("expected 'Ver todos' link")
	}
	if !strings.Contains(bodyStr, "Supermercado") || !strings.Contains(bodyStr, "1,240.00") {
		t.Errorf("expected Supermercado transaction with native VES amount")
	}
	if !strings.Contains(bodyStr, "Nómina") || !strings.Contains(bodyStr, "850.00") {
		t.Errorf("expected Nómina transaction with native USD amount")
	}
	if !strings.Contains(bodyStr, "Café") || !strings.Contains(bodyStr, "4.50") {
		t.Errorf("expected Café transaction with native USD amount")
	}

	// 5. Floating Action Button checks
	if !strings.Contains(bodyStr, "fab-new-operation") {
		t.Errorf("expected FAB button with id 'fab-new-operation'")
	}

	// 6. Bottom sheet modal checks
	if !strings.Contains(bodyStr, "Nueva operación") {
		t.Errorf("expected bottom sheet title 'Nueva operación'")
	}
	if !strings.Contains(bodyStr, "Gasto") || !strings.Contains(bodyStr, "Ingreso") || !strings.Contains(bodyStr, "Transferencia") {
		t.Errorf("expected Gasto, Ingreso, and Transferencia options in bottom sheet")
	}

	// 7. Bottom Navigation checks
	if !strings.Contains(bodyStr, "Inicio") || !strings.Contains(bodyStr, "Movimientos") || !strings.Contains(bodyStr, "Cuentas") {
		t.Errorf("expected 3 destinations in bottom navigation")
	}
}

func TestMobileTransactions(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	for _, path := range []string{"/mobile/movimientos", "/mobile/transactions"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		res := rec.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("path %s: expected status 200, got %d", path, res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("path %s: reading body: %v", path, err)
		}
		bodyStr := string(body)

		// 1. Header & Title
		if !strings.Contains(bodyStr, "Movimientos") {
			t.Errorf("path %s: expected title 'Movimientos'", path)
		}

		// 2. Summary
		if !strings.Contains(bodyStr, "Ingresos del periodo") || !strings.Contains(bodyStr, "Gastos del periodo") {
			t.Errorf("path %s: expected period summary card", path)
		}

		// 3. Search & Filter chips
		if !strings.Contains(bodyStr, "tx-search-input") {
			t.Errorf("path %s: expected search input", path)
		}
		if !strings.Contains(bodyStr, "Gastos") || !strings.Contains(bodyStr, "Ingresos") || !strings.Contains(bodyStr, "Transferencias") {
			t.Errorf("path %s: expected filter chips", path)
		}

		// 4. Movements in native currency
		if !strings.Contains(bodyStr, "Supermercado") {
			t.Errorf("path %s: expected supermarket transaction", path)
		}
		if !strings.Contains(bodyStr, "1,240.00") || !strings.Contains(bodyStr, "850.00") {
			t.Errorf("path %s: expected native amount formatted", path)
		}

		// 5. Active nav state
		if !strings.Contains(bodyStr, "nav-tab-active") {
			t.Errorf("path %s: expected active nav tab", path)
		}

		// 6. FAB & Bottom sheet
		if !strings.Contains(bodyStr, "fab-new-operation") || !strings.Contains(bodyStr, "operation-sheet") {
			t.Errorf("path %s: expected FAB and bottom sheet", path)
		}
	}
}

func TestMobileAccounts(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	for _, path := range []string{"/mobile/cuentas", "/mobile/accounts"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		res := rec.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("path %s: expected status 200, got %d", path, res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("path %s: reading body: %v", path, err)
		}
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "Cuentas") {
			t.Errorf("path %s: expected title 'Cuentas'", path)
		}
		if !strings.Contains(bodyStr, "Mercantil USD") || !strings.Contains(bodyStr, "Banesco Corriente") {
			t.Errorf("path %s: expected accounts list", path)
		}
		if !strings.Contains(bodyStr, "141,308.84") && !strings.Contains(bodyStr, "141308.84") && !strings.Contains(bodyStr, "142548.84") && !strings.Contains(bodyStr, "142,548.84") {
			t.Errorf("path %s: expected account balances", path)
		}
		if !strings.Contains(bodyStr, "nav-tab-active") {
			t.Errorf("path %s: expected active nav tab", path)
		}
	}
}

func TestMobileNewOperation(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	for _, path := range []string{"/mobile/operacion/nueva?type=EXPENSE", "/mobile/operacion/nueva?type=INCOME", "/mobile/operacion/nueva?type=TRANSFER"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		res := rec.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("path %s: expected status 200, got %d", path, res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("path %s: reading body: %v", path, err)
		}
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "Nueva operación") {
			t.Errorf("path %s: expected title 'Nueva operación'", path)
		}
		if !strings.Contains(bodyStr, "op-type-segmented-control") {
			t.Errorf("path %s: expected segmented control", path)
		}
		if !strings.Contains(bodyStr, "op-amount-input") {
			t.Errorf("path %s: expected amount input", path)
		}
		if !strings.Contains(bodyStr, "Mercantil USD") {
			t.Errorf("path %s: expected real account in options", path)
		}
	}
}

func TestMobileCategories(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/mobile/categorias", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Categorías") {
		t.Errorf("expected title 'Categorías'")
	}
	if !strings.Contains(bodyStr, "Alimentación") || !strings.Contains(bodyStr, "Nómina y Sueldos") {
		t.Errorf("expected categories list")
	}
}

func TestMobilePaymentMethods(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	seedMobileTestData(t, fs)
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/mobile/metodos-pago", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Métodos de pago") {
		t.Errorf("expected title 'Métodos de pago'")
	}
	if !strings.Contains(bodyStr, "Bank transfer") {
		t.Errorf("expected payment methods list")
	}
}

func TestMobileFormSubmissionCreatesRealRecords(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := context.Background()

	subChecking := AccountSubtypeChecking
	acct1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Mercantil USD", Type: AccountTypeBank, Subtype: &subChecking, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating acct1: %v", err)
	}
	acct2, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Efectivo USD", Type: AccountTypeCash, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating acct2: %v", err)
	}
	cat, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Alimentación", Type: CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating cat: %v", err)
	}

	router := testRouter(t, fs)

	// 1. Submit expense from mobile form
	expenseData := url.Values{
		"type":              {"EXPENSE"},
		"account_id":        {strconv.FormatInt(acct1.ID, 10)},
		"category_id":       {strconv.FormatInt(cat.ID, 10)},
		"payment_method_id": {"1"},
		"amount":            {"45.00"},
		"effective_date":    {"2026-09-10"},
		"description":       {"Almuerzo móvil"},
		"return_to":         {"/mobile/movimientos"},
	}

	reqExp := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(expenseData.Encode()))
	reqExp.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recExp := httptest.NewRecorder()
	router.ServeHTTP(recExp, reqExp)

	resExp := recExp.Result()
	defer resExp.Body.Close()

	if resExp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", resExp.StatusCode)
	}
	if loc := resExp.Header.Get("Location"); loc != "/mobile/movimientos" {
		t.Errorf("expected redirect to /mobile/movimientos, got %s", loc)
	}

	// 2. Submit transfer from mobile form
	transferData := url.Values{
		"source_account_id":      {strconv.FormatInt(acct1.ID, 10)},
		"destination_account_id": {strconv.FormatInt(acct2.ID, 10)},
		"amount":                 {"100.00"},
		"effective_date":         {"2026-09-10"},
		"description":            {"Transferencia desde móvil"},
		"return_to":              {"/mobile/movimientos"},
	}

	reqTf := httptest.NewRequest(http.MethodPost, "/transfers", strings.NewReader(transferData.Encode()))
	reqTf.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recTf := httptest.NewRecorder()
	router.ServeHTTP(recTf, reqTf)

	resTf := recTf.Result()
	defer resTf.Body.Close()

	if resTf.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", resTf.StatusCode)
	}
	if loc := resTf.Header.Get("Location"); loc != "/mobile/movimientos" {
		t.Errorf("expected redirect to /mobile/movimientos, got %s", loc)
	}

	// Verify both exist in ledger
	txs, err := fs.ListTransactions(ctx, ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("expected 2 transactions in ledger, got %d", len(txs))
	}
}
