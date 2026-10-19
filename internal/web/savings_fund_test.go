package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func sampleFund() SavingsFund {
	return SavingsFund{
		Accounts: []SavingsAccountAvailability{
			{AccountID: 1, AccountName: "Mercantil USD", IsActive: true, BalanceFormatted: "1000.00",
				ReservedFormatted: "800.00", FreeFormatted: "200.00", OverallocatedFormatted: "0.00",
				HasReservation: true, HasFree: true},
			{AccountID: 2, AccountName: "Zelle", IsActive: true, BalanceFormatted: "100.00",
				ReservedFormatted: "150.00", FreeFormatted: "-50.00", OverallocatedFormatted: "50.00",
				HasReservation: true, Overallocated: true},
		},
		BalanceFormatted: "1100.00", ReservedFormatted: "950.00", FreeFormatted: "200.00",
		OverallocatedFormatted: "50.00", AllocatedFormatted: "0.00", UnallocatedFormatted: "950.00",
		Overallocated: true, CanReserve: true, CanRelease: true,
	}
}

var requestKeyField = regexp.MustCompile(`name="request_key" value="([0-9a-f]{32})"`)

func TestTheFundPageShowsTheFundAndFreshKeys(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.FundResult = sampleFund()
	reason := "Reparación del carro"
	f.Operations = []SavingsOperation{
		{ID: 2, Kind: "RELEASE", KindLabel: "Liberación", Reason: &reason, EffectiveDate: savingsDay("2026-10-14"),
			Entries: []SavingsEntry{{AccountName: "Mercantil USD", AmountFormatted: "-300.00"}}, TotalFormatted: "-300.00", IsOutflow: true},
	}
	router := savingsRouter(t, f)

	for _, path := range []string{"/savings/fund", "/mobile/ahorro/fondo"} {
		rec := get(t, router, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		page := html.UnescapeString(body(t, rec))
		for _, want := range []string{"950.00", "200.00", "Mercantil USD", "Zelle", "Liberación", "Reparación del carro",
			"-300.00", `action="/savings/reserve"`, `action="/savings/release"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
		keys := requestKeyField.FindAllStringSubmatch(page, -1)
		if len(keys) != 2 || keys[0][1] == keys[1][1] {
			t.Errorf("%s: want two distinct request keys, got %v", path, keys)
		}
		// Only an account with free money is offered for a reservation.
		if strings.Contains(page, `<option value="2">Zelle · -50.00 libres`) {
			t.Errorf("%s offers an account with no free balance", path)
		}
	}
	// A fresh key per render: a second load is a second form.
	a := requestKeyField.FindStringSubmatch(body(t, get(t, router, "/savings/fund")))
	b := requestKeyField.FindStringSubmatch(body(t, get(t, router, "/savings/fund")))
	if a[1] == b[1] {
		t.Error("two renders carried the same request key")
	}
}

func TestReservingAndReleasingPostWhatTheFormSays(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	router := savingsRouter(t, f)
	const k = "0123456789abcdef0123456789abcdef"

	rec := post(t, router, "/savings/reserve", url.Values{
		"request_key": {k}, "income_id": {"11"}, "account_id": {"1"}, "amount": {" 100.00 "},
		"exception": {"1"}, "reason": {"Bono"}, "return": {"/savings/incomes/11"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/savings/incomes/11?msg=reserved" {
		t.Fatalf("%d to %q", rec.Code, rec.Header().Get("Location"))
	}
	got := f.Reserved[0]
	if got.RequestKey != k || got.IncomeTransactionID == nil || *got.IncomeTransactionID != 11 ||
		got.AccountID != 1 || got.Amount != "100.00" || !got.Exception || got.Reason != "Bono" {
		t.Errorf("Reserve got %+v", got)
	}

	rec = post(t, router, "/savings/reserve", url.Values{"request_key": {k}, "account_id": {"1"}, "amount": {"5"}})
	if rec.Code != http.StatusSeeOther || f.Reserved[1].IncomeTransactionID != nil {
		t.Errorf("a manual reservation named an income: %+v", f.Reserved[1])
	}

	rec = post(t, router, "/savings/release", url.Values{
		"request_key": {k}, "account_id": {"1"}, "amount": {"300"}, "reason": {"Imprevisto"},
	})
	if rec.Header().Get("Location") != "/savings/fund?msg=released" || f.Released[0].Reason != "Imprevisto" {
		t.Errorf("release: %q, %+v", rec.Header().Get("Location"), f.Released)
	}

	for errValue, key := range map[error]string{
		ErrSavingsInsufficientFree:  "insufficient-free",
		ErrSavingsExceedsSuggestion: "exceeds-suggestion",
		ErrSavingsNotUSDAccount:     "not-usd",
		ErrSavingsInvalidRequestKey: "request-key",
		ErrAccountInactive:          "account-inactive",
	} {
		f.ActionErr = errValue
		rec := post(t, router, "/savings/reserve", url.Values{"request_key": {k}, "account_id": {"1"}, "amount": {"5"}})
		if loc := rec.Header().Get("Location"); loc != "/savings/fund?err="+key {
			t.Errorf("%v redirected to %q", errValue, loc)
		}
		page := html.UnescapeString(body(t, get(t, router, "/savings/fund?err="+key)))
		if strings.Contains(page, "No se pudo completar la acción.") {
			t.Errorf("%s has no message of its own", key)
		}
	}
	f.ActionErr = nil
	if rec := post(t, router, "/savings/reserve", url.Values{"request_key": {k}, "amount": {"5"}}); rec.Header().Get("Location") != "/savings/fund?err=account" {
		t.Errorf("a reservation with no account redirected to %q", rec.Header().Get("Location"))
	}
}

func TestTheIncomePageOffersAConfirmationWithWhatRemains(t *testing.T) {
	t.Parallel()
	f := sampleSavings()
	f.FundResult = sampleFund()
	f.Details[11] = SavingsIncomeDetail{TransactionID: 11, Eligible: true, Revisions: []SavingsSuggestion{{
		IncomeTransactionID: 11, Revision: 1, Status: "OPEN", StatusLabel: "Confirmada en parte", Live: true, Valued: true,
		SuggestedUSDFormatted: "160.00", IncomeConfirmedFormatted: "100.00", RemainingFormatted: "60.00",
		RemainingAmount: "60.00", CanConfirm: true, CanDismiss: true,
	}}}
	router := savingsRouter(t, f)
	for _, path := range []string{"/savings/incomes/11", "/mobile/ahorro/ingresos/11"} {
		page := html.UnescapeString(body(t, get(t, router, path)))
		for _, want := range []string{`name="income_id" value="11"`, `value="60.00"`, "Confirmada en parte",
			"Mercantil USD · 200.00 libres", "100.00"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not show %q", path, want)
			}
		}
		if !requestKeyField.MatchString(page) {
			t.Errorf("%s has no request key", path)
		}
	}
}
