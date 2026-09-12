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

func setupTransferAccounts(t *testing.T, fs *FakeServices) (Account, Account, Account) {
	t.Helper()
	ctx := context.Background()

	subChecking := AccountSubtypeChecking
	subSavings := AccountSubtypeSavings

	usd1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Mercantil USD",
		Type:         AccountTypeBank,
		Subtype:      &subChecking,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating usd1: %v", err)
	}

	usd2, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Efectivo USD",
		Type:         AccountTypeCash,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating usd2: %v", err)
	}

	ves1, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name:         "Banesco VES",
		Type:         AccountTypeBank,
		Subtype:      &subSavings,
		CurrencyCode: "VES",
	})
	if err != nil {
		t.Fatalf("creating ves1: %v", err)
	}

	return usd1, usd2, ves1
}

func TestGetNewTransferForm(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/transfers/new", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "New Internal Transfer") {
		t.Errorf("expected form title in page")
	}
	if !strings.Contains(bodyStr, "Mercantil USD") {
		t.Errorf("expected account in options")
	}
}

func TestPostCreateTransferSuccess(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	formData := url.Values{
		"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
		"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
		"amount":                 {"150.00"},
		"effective_date":         {"2026-09-10"},
		"description":            {"Ahorro quincenal"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transfers", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/transactions" {
		t.Errorf("expected redirect to /transactions, got %s", loc)
	}

	txs, err := fs.ListTransactions(context.Background(), ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("expected 1 transaction recorded, got %d", len(txs))
	}
	if txs[0].Type != TransactionTypeTransfer {
		t.Errorf("expected transaction type TRANSFER, got %s", txs[0].Type)
	}
	if txs[0].Transfer == nil {
		t.Fatalf("expected Transfer detail on transaction")
	}
	if txs[0].Transfer.AmountMinor != 15000 {
		t.Errorf("expected amount 15000, got %d", txs[0].Transfer.AmountMinor)
	}
}

func TestPostCreateTransferValidationErrors(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, ves1 := setupTransferAccounts(t, fs)
	router := testRouter(t, fs)

	tests := []struct {
		name         string
		form         url.Values
		expectedText string
	}{
		{
			name: "same account",
			form: url.Values{
				"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
				"destination_account_id": {strconv.FormatInt(usd1.ID, 10)},
				"amount":                 {"50.00"},
				"effective_date":         {"2026-09-10"},
			},
			expectedText: "Source and destination accounts must be different",
		},
		{
			name: "currency mismatch",
			form: url.Values{
				"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
				"destination_account_id": {strconv.FormatInt(ves1.ID, 10)},
				"amount":                 {"50.00"},
				"effective_date":         {"2026-09-10"},
			},
			expectedText: "Both accounts must be in the same currency",
		},
		{
			name: "negative amount",
			form: url.Values{
				"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
				"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
				"amount":                 {"-50.00"},
				"effective_date":         {"2026-09-10"},
			},
			expectedText: "Amount must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/transfers", strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			res := rec.Result()
			defer res.Body.Close()

			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected status 422, got %d", res.StatusCode)
			}
			body, _ := io.ReadAll(res.Body)
			if !strings.Contains(string(body), tc.expectedText) {
				t.Errorf("expected %q in response body, got %s", tc.expectedText, string(body))
			}
		})
	}
}

func TestTransferSideErrorMapping(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	ctx := context.Background()

	// Archive usd2 to trigger ErrAccountInactive on destination side
	if _, err := fs.SetAccountActive(ctx, usd2.ID, false); err != nil {
		t.Fatalf("archiving usd2: %v", err)
	}

	router := testRouter(t, fs)

	formData := url.Values{
		"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
		"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
		"amount":                 {"75.00"},
		"effective_date":         {"2026-09-10"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transfers", strings.NewReader(formData.Encode()))
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

	if !strings.Contains(bodyStr, "Selected account is inactive/archived") {
		t.Errorf("expected inactive account message, got %s", bodyStr)
	}
}

func TestEditTransferFlow(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	ctx := context.Background()

	created, err := fs.RecordTransfer(ctx, RecordTransferInput{
		SourceAccountID:      usd1.ID,
		DestinationAccountID: usd2.ID,
		Amount:               "200.00",
		EffectiveDate:        "2026-09-10",
	})
	if err != nil {
		t.Fatalf("recording transfer: %v", err)
	}

	router := testRouter(t, fs)

	// 1. GET /transfers/{id}/edit returns 200
	req := httptest.NewRequest(http.MethodGet, "/transfers/"+strconv.FormatInt(created.ID, 10)+"/edit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for GET edit, got %d", res.StatusCode)
	}

	// 2. GET /transactions/{id}/edit redirects to /transfers/{id}/edit
	reqGeneric := httptest.NewRequest(http.MethodGet, "/transactions/"+strconv.FormatInt(created.ID, 10)+"/edit", nil)
	recGeneric := httptest.NewRecorder()
	router.ServeHTTP(recGeneric, reqGeneric)

	resGeneric := recGeneric.Result()
	defer resGeneric.Body.Close()
	if resGeneric.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", resGeneric.StatusCode)
	}
	if loc := resGeneric.Header.Get("Location"); loc != "/transfers/"+strconv.FormatInt(created.ID, 10)+"/edit" {
		t.Errorf("expected redirect to /transfers/%d/edit, got %s", created.ID, loc)
	}

	// 3. POST /transactions/{id} rejects transfer
	postGeneric := httptest.NewRequest(http.MethodPost, "/transactions/"+strconv.FormatInt(created.ID, 10), nil)
	recPostGeneric := httptest.NewRecorder()
	router.ServeHTTP(recPostGeneric, postGeneric)

	resPostGeneric := recPostGeneric.Result()
	defer resPostGeneric.Body.Close()
	if resPostGeneric.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when updating transfer via /transactions/{id}, got %d", resPostGeneric.StatusCode)
	}

	// 4. POST /transfers/{id} updates successfully
	updateData := url.Values{
		"source_account_id":      {strconv.FormatInt(usd1.ID, 10)},
		"destination_account_id": {strconv.FormatInt(usd2.ID, 10)},
		"amount":                 {"250.00"},
		"effective_date":         {"2026-09-11"},
		"description":            {"Updated transfer"},
	}
	reqUpdate := httptest.NewRequest(http.MethodPost, "/transfers/"+strconv.FormatInt(created.ID, 10), strings.NewReader(updateData.Encode()))
	reqUpdate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recUpdate := httptest.NewRecorder()
	router.ServeHTTP(recUpdate, reqUpdate)

	resUpdate := recUpdate.Result()
	defer resUpdate.Body.Close()
	if resUpdate.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 on successful update, got %d", resUpdate.StatusCode)
	}

	updated, err := fs.GetTransaction(ctx, created.ID)
	if err != nil {
		t.Fatalf("fetching updated tx: %v", err)
	}
	if updated.Transfer.AmountMinor != 25000 {
		t.Errorf("expected 25000 minor units, got %d", updated.Transfer.AmountMinor)
	}
}

func TestTransactionsListShowsTransferProperly(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	usd1, usd2, _ := setupTransferAccounts(t, fs)
	ctx := context.Background()

	created, err := fs.RecordTransfer(ctx, RecordTransferInput{
		SourceAccountID:      usd1.ID,
		DestinationAccountID: usd2.ID,
		Amount:               "320.00",
		EffectiveDate:        "2026-09-10",
		Description:          OptionalString("Test transfer listing"),
	})
	if err != nil {
		t.Fatalf("recording transfer: %v", err)
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

	body, _ := io.ReadAll(res.Body)
	bodyStr := string(body)

	// Badge
	if !strings.Contains(bodyStr, "badge-transfer") {
		t.Errorf("expected badge-transfer in transactions table")
	}
	// Source and destination account names
	if !strings.Contains(bodyStr, "Mercantil USD &rarr; Efectivo USD") && !strings.Contains(bodyStr, "Mercantil USD → Efectivo USD") {
		t.Errorf("expected transfer endpoint account names rendered in table, got: %s", bodyStr)
	}
	// Edit link pointing to /transfers/{id}/edit
	expectedEditLink := "/transfers/" + strconv.FormatInt(created.ID, 10) + "/edit"
	if !strings.Contains(bodyStr, expectedEditLink) {
		t.Errorf("expected edit link %s in transactions table", expectedEditLink)
	}
}
