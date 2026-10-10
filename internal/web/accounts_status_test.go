package web

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func statusRouter(t *testing.T) (*FakeServices, http.Handler) {
	t.Helper()
	fs := NewFakeServices()
	return fs, NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), fs.BuildServices(), DefaultConfig())
}

func isActive(t *testing.T, fs *FakeServices, id int64) bool {
	t.Helper()
	acct, err := (fakeAccountService{fs}).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("reading account %d: %v", id, err)
	}
	return acct.IsActive
}

func TestAnAccountCanBeArchivedAndRestoredFromTheDesktop(t *testing.T) {
	t.Parallel()

	fs, router := statusRouter(t)
	id := mustOpen(t, fs, "Cuenta vieja", "USD", "150.00")
	path := "/accounts/" + itoa(id)

	// The list offers the action, and the edit page explains what it does.
	if page := body(t, get(t, router, "/accounts")); !strings.Contains(page, `action="`+path+`/archive"`) {
		t.Error("the accounts list does not offer to archive an active account")
	}
	edit := html.UnescapeString(body(t, get(t, router, path+"/edit")))
	for _, phrase := range []string{"Archivar cuenta", "Saldo actual: 150.00 USD", "seguirá contando en el patrimonio"} {
		if !strings.Contains(edit, phrase) {
			t.Errorf("the edit page does not say %q", phrase)
		}
	}

	rec := post(t, router, path+"/archive", url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/accounts" {
		t.Fatalf("archive = %d to %q, want 303 to /accounts", rec.Code, rec.Header().Get("Location"))
	}
	if isActive(t, fs, id) {
		t.Fatal("the account is still active")
	}

	// Archiving twice is archiving once: the route names the state it sets.
	post(t, router, path+"/archive", url.Values{})
	if isActive(t, fs, id) {
		t.Error("a second archive restored the account; the route must not toggle")
	}

	page := body(t, get(t, router, "/accounts"))
	if !strings.Contains(page, "Archived · con saldo") || !strings.Contains(page, `action="`+path+`/restore"`) {
		t.Error("the archived account is not marked, or cannot be restored, from the list")
	}

	post(t, router, path+"/restore", url.Values{})
	if !isActive(t, fs, id) {
		t.Error("restoring did not reactivate the account")
	}
}

func TestArchivingReturnsWhereItWasAskedAndNowhereElse(t *testing.T) {
	t.Parallel()

	fs, router := statusRouter(t)
	id := mustOpen(t, fs, "Efectivo", "USD", "10.00")
	path := "/accounts/" + itoa(id) + "/archive"

	rec := post(t, router, path, url.Values{"return_to": {"/mobile/cuentas"}})
	if got := rec.Header().Get("Location"); got != "/mobile/cuentas" {
		t.Errorf("redirected to %q, want /mobile/cuentas", got)
	}

	// Not an open redirect.
	for _, hostile := range []string{"//evil.example", "https://evil.example"} {
		rec := post(t, router, path, url.Values{"return_to": {hostile}})
		if got := rec.Header().Get("Location"); got != "/accounts" {
			t.Errorf("return_to %q redirected to %q, want /accounts", hostile, got)
		}
	}
}

func TestArchivingAnUnknownAccountIsNotFound(t *testing.T) {
	t.Parallel()

	_, router := statusRouter(t)
	for _, path := range []string{"/accounts/4242/archive", "/accounts/4242/restore", "/accounts/abc/archive"} {
		if rec := post(t, router, path, url.Values{}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestTheMobileAccountsScreenListsArchivedAccountsApart(t *testing.T) {
	t.Parallel()

	fs, router := statusRouter(t)
	active := mustOpen(t, fs, "Mercantil USD", "USD", "100.00")
	old := mustOpen(t, fs, "Cuenta vieja", "USD", "150.00")
	if _, err := (fakeAccountService{fs}).SetActive(context.Background(), old, false); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	page := body(t, get(t, router, "/mobile/cuentas"))
	archived := strings.Index(page, "Archivadas")
	if archived < 0 {
		t.Fatal("the mobile accounts screen has no archived section")
	}
	activeCard := strings.Index(page, `action="/accounts/`+itoa(active)+`/archive"`)
	restore := strings.Index(page, `action="/accounts/`+itoa(old)+`/restore"`)
	if activeCard < 0 || activeCard > archived {
		t.Error("the active account is not offered for archiving above the archived section")
	}
	if restore < archived {
		t.Error("the archived account is not offered for restoring in its own section")
	}
	if !strings.Contains(page, `name="return_to" value="/mobile/cuentas"`) {
		t.Error("the mobile actions do not return to the mobile screen")
	}
	if !strings.Contains(page, "Con saldo · sigue contando") {
		t.Error("the archived account with money is not marked")
	}
}

func TestTheMobileMovementsSummaryIsTheMonthsCashFlow(t *testing.T) {
	t.Parallel()

	fs, router := statusRouter(t)
	fs.Analytics.SetMonthCashFlow(CashFlow{
		CurrencyCode: "USD", IncomeFormatted: "3000.00", ExpenseFormatted: "2159.80",
		Complete: false, Missing: []MissingFlow{{CurrencyCode: "VES"}},
	})

	page := html.UnescapeString(body(t, get(t, router, "/mobile/movimientos")))
	for _, phrase := range []string{
		"Ingresos del mes", "+ 3,000.00 USD",
		"Gastos del mes", "- 2,159.80 USD",
		"Sin tasa para valorar movimientos en VES",
	} {
		if !strings.Contains(page, phrase) {
			t.Errorf("the movements summary does not show %q", phrase)
		}
	}
}

func TestTheMobileMovementsSurviveACashFlowFailure(t *testing.T) {
	t.Parallel()

	fs, router := statusRouter(t)
	fs.Analytics.SetError(errContext("valuation broke"))

	rec := get(t, router, "/mobile/movimientos")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: the list needs no exchange rate and must still render", rec.Code)
	}
	if !strings.Contains(body(t, rec), "No se pudo calcular el flujo del mes") {
		t.Error("the failure is not stated")
	}
}

type errContext string

func (e errContext) Error() string { return string(e) }

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
