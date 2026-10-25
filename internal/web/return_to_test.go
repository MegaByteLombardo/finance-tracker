package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A form opened from a card's page goes back there, on Cancel and on success,
// instead of dropping the user on /transactions.

func TestTransferFormComesBackToWhereItStarted(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	rec := get(t, router, "/transfers/new?to_account_id="+strconv.FormatInt(usd2.ID, 10)+"&return_to=/cards/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, `name="return_to" value="/cards/7"`) {
		t.Error("the form does not carry return_to")
	}
	if !strings.Contains(page, `<a href="/cards/7" class="btn btn-secondary">Cancel</a>`) {
		t.Error("Cancel does not lead back to the card")
	}

	form := url.Values{
		"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
		"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
		"amount":                 {"10.00"},
		"effective_date":         {"2026-09-10"},
		"return_to":              {"/cards/7"},
	}
	if got := post(t, router, "/transfers", form).Header().Get("Location"); got != "/cards/7" {
		t.Errorf("redirected to %q, want /cards/7", got)
	}
}

func TestReturnToIsNeverAnotherHost(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	for _, hostile := range []string{"//evil.example", `/\evil.example`, "https://evil.example"} {
		page := get(t, router, "/transfers/new?return_to="+url.QueryEscape(hostile)).Body.String()
		if strings.Contains(page, "evil.example") {
			t.Errorf("return_to %q was rendered into the page", hostile)
		}

		form := url.Values{
			"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
			"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
			"amount":                 {"10.00"},
			"effective_date":         {"2026-09-10"},
			"return_to":              {hostile},
		}
		if got := post(t, router, "/transfers", form).Header().Get("Location"); got != "/transactions" {
			t.Errorf("return_to %q redirected to %q, want /transactions", hostile, got)
		}
	}
}

func TestGenericEditKeepsReturnToWhenItHandsOver(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	created, err := fs.RecordTransfer(context.Background(), RecordTransferInput{
		SourceAccountID: usd1.ID, DestinationAccountID: usd2.ID, Amount: "5.00", EffectiveDate: "2026-09-10",
	})
	if err != nil {
		t.Fatalf("recording transfer: %v", err)
	}
	router := testRouter(t, fs)
	id := strconv.FormatInt(created.ID, 10)

	rec := get(t, router, "/transactions/"+id+"/edit?return_to=/cards/7")
	if want := "/transfers/" + id + "/edit?return_to=/cards/7"; rec.Header().Get("Location") != want {
		t.Fatalf("redirected to %q, want %q", rec.Header().Get("Location"), want)
	}
	if page := get(t, router, rec.Header().Get("Location")).Body.String(); !strings.Contains(page, `name="return_to" value="/cards/7"`) {
		t.Error("the transfer's edit form does not carry return_to")
	}
}

func TestExchangeFormPreselectsTheDestination(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	_, _, ves := setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	page := get(t, router, "/exchanges/new?to_account_id="+strconv.FormatInt(ves.ID, 10)+"&return_to=/cards/7").Body.String()
	selected := regexp.MustCompile(`<option value="` + strconv.FormatInt(ves.ID, 10) + `"[^>]*selected`)
	if !selected.MatchString(page) {
		t.Error("the destination account is not preselected")
	}
	if !strings.Contains(page, `<a href="/cards/7" class="btn btn-secondary">Cancel</a>`) {
		t.Error("Cancel does not lead back to the card")
	}
}
