package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMobileDashboard(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
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
	if !strings.Contains(bodyStr, "Domingo, 6 de septiembre") {
		t.Errorf("expected date 'Domingo, 6 de septiembre'")
	}

	// 2. Net Worth checks
	if !strings.Contains(bodyStr, "Patrimonio neto") {
		t.Errorf("expected label 'Patrimonio neto'")
	}
	if !strings.Contains(bodyStr, "$ 5,842.37") {
		t.Errorf("expected amount '$ 5,842.37'")
	}
	if !strings.Contains(bodyStr, "USD · moneda base") {
		t.Errorf("expected base currency 'USD · moneda base'")
	}
	if !strings.Contains(bodyStr, "2.4% este mes") {
		t.Error("expected monthly change '2.4% este mes'")
	}

	// 3. Multi-currency balances checks
	if !strings.Contains(bodyStr, "Balances") {
		t.Errorf("expected section title 'Balances'")
	}
	if !strings.Contains(bodyStr, "USD") || !strings.Contains(bodyStr, "$2,057.56") {
		t.Errorf("expected USD balance")
	}
	if !strings.Contains(bodyStr, "VES") || !strings.Contains(bodyStr, "Bs 150,548.84") {
		t.Errorf("expected VES balance")
	}
	if !strings.Contains(bodyStr, "EUR") || !strings.Contains(bodyStr, "€850.00") {
		t.Errorf("expected EUR balance")
	}
	if !strings.Contains(bodyStr, "Ver cuentas") {
		t.Errorf("expected 'Ver cuentas' link")
	}

	// 4. Weekly budget checks
	if !strings.Contains(bodyStr, "Presupuesto semanal") {
		t.Errorf("expected section title 'Presupuesto semanal'")
	}
	if !strings.Contains(bodyStr, "Bs 8,450") || !strings.Contains(bodyStr, "Bs 12,000") {
		t.Errorf("expected spent and total budget amounts")
	}
	if !strings.Contains(bodyStr, "Bs 3,550") || !strings.Contains(bodyStr, "disponibles") {
		t.Errorf("expected available budget amount")
	}
	if !strings.Contains(bodyStr, "Quedan 3 días") {
		t.Errorf("expected 'Quedan 3 días' badge")
	}

	// 5. Recent transactions checks
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

	// 6. Floating Action Button checks
	if !strings.Contains(bodyStr, "fab-new-operation") {
		t.Errorf("expected FAB button with id 'fab-new-operation'")
	}

	// 7. Bottom sheet modal checks
	if !strings.Contains(bodyStr, "Nueva operación") {
		t.Errorf("expected bottom sheet title 'Nueva operación'")
	}
	if !strings.Contains(bodyStr, "Gasto") || !strings.Contains(bodyStr, "Ingreso") || !strings.Contains(bodyStr, "Transferencia") {
		t.Errorf("expected Gasto, Ingreso, and Transferencia options in bottom sheet")
	}

	// 8. Bottom Navigation checks
	if !strings.Contains(bodyStr, "Inicio") || !strings.Contains(bodyStr, "Movimientos") || !strings.Contains(bodyStr, "Cuentas") {
		t.Errorf("expected 3 destinations in bottom navigation")
	}
}

func TestMobileTransactions(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
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
		if !strings.Contains(bodyStr, "Septiembre 2026") {
			t.Errorf("path %s: expected period label 'Septiembre 2026'", path)
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

		// 4. Date Groups
		if !strings.Contains(bodyStr, "Hoy, 6 de septiembre") || !strings.Contains(bodyStr, "Ayer, 5 de septiembre") {
			t.Errorf("path %s: expected date groups", path)
		}

		// 5. Movements in native currency
		if !strings.Contains(bodyStr, "Supermercado Automercados Plaza&#39;s") && !strings.Contains(bodyStr, "Plaza") {
			t.Errorf("path %s: expected supermarket transaction", path)
		}
		if !strings.Contains(bodyStr, "1,240.00") || !strings.Contains(bodyStr, "850.00") {
			t.Errorf("path %s: expected native amount formatted", path)
		}

		// 6. Active nav state
		if !strings.Contains(bodyStr, "nav-tab-active") {
			t.Errorf("path %s: expected active nav tab", path)
		}

		// 7. FAB & Bottom sheet
		if !strings.Contains(bodyStr, "fab-new-operation") || !strings.Contains(bodyStr, "operation-sheet") {
			t.Errorf("path %s: expected FAB and bottom sheet", path)
		}
	}
}

func TestMobileAccounts(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
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
		if !strings.Contains(bodyStr, "1,650.00") || !strings.Contains(bodyStr, "142,548.84") {
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
	}
}

func TestMobileCategories(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
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
	if !strings.Contains(bodyStr, "Pago Móvil") || !strings.Contains(bodyStr, "Zelle") {
		t.Errorf("expected payment methods list")
	}
}
