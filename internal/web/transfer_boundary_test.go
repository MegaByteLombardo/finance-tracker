package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The income-and-expense form is not a transfer form, and must not become one by
// accident now that TRANSFER is a value the type knows.
//
// A transfer has two accounts and two movements; this form has fields for
// neither. What the code here replaced coerced any unrecognised type to EXPENSE,
// so a submission saying TRANSFER was filed as an expense against one account —
// a wrong record rather than a rejected one.

func TestTheIncomeExpenseFormNeverOffersTransfer(t *testing.T) {
	t.Parallel()

	offered := TransactionTypesForIncomeExpenseForm()
	if len(offered) != 2 {
		t.Fatalf("the form offers %d types, want 2", len(offered))
	}
	for _, offeredType := range offered {
		if offeredType == TransactionTypeTransfer || offeredType == TransactionTypeOpeningBalance {
			t.Errorf("the income-and-expense form offers %s", offeredType)
		}
	}

	// The full enumeration still knows it; that is a different question.
	var found bool
	for _, known := range TransactionTypes() {
		if known == TransactionTypeTransfer {
			found = true
		}
	}
	if !found {
		t.Error("TransactionTypes does not include TRANSFER")
	}
}

func TestTheIncomeExpenseFormRefusesTransfer(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"TRANSFER", "transfer", "OPENING_BALANCE", "REFUND"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseIncomeOrExpenseType(raw); err == nil {
				t.Errorf("%q was accepted by the income-and-expense form", raw)
			}
		})
	}

	// And the two it is for, plus the empty default, still work.
	for raw, want := range map[string]TransactionType{
		"INCOME":  TransactionTypeIncome,
		"income":  TransactionTypeIncome,
		"EXPENSE": TransactionTypeExpense,
		"":        TransactionTypeExpense,
		"  ":      TransactionTypeExpense,
	} {
		got, err := parseIncomeOrExpenseType(raw)
		if err != nil {
			t.Errorf("%q was rejected: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q became %q, want %q", raw, got, want)
		}
	}
}

// Posting TRANSFER to the generic form must not record anything at all — and in
// particular must not record an expense.
func TestPostingTransferToTheGenericFormRecordsNothing(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	ctx := t.Context()

	sub := AccountSubtypeChecking
	acct, err := fs.CreateAccount(ctx, CreateAccountInput{
		Name: "Mercantil USD", Type: AccountTypeBank, Subtype: &sub, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	cat, err := fs.CreateCategory(ctx, CreateCategoryInput{Name: "Comida", Type: CategoryTypeExpense})
	if err != nil {
		t.Fatalf("creating the category: %v", err)
	}

	router := testRouter(t, fs)

	formData := url.Values{
		"type":              {"TRANSFER"},
		"account_id":        {strconv.FormatInt(acct.ID, 10)},
		"category_id":       {strconv.FormatInt(cat.ID, 10)},
		"amount":            {"250.00"},
		"payment_method_id": {"2"},
		"effective_date":    {"2026-09-06"},
	}

	req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the body: %v", err)
		}
	}()

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("posting TRANSFER returned %d, want 422", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if !strings.Contains(string(body), "Income or Expense only") {
		t.Errorf("the page does not say what this form is for: %s", body)
	}

	// Nothing was recorded, and in particular no expense.
	txs, err := fs.ListTransactions(ctx, ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(txs) != 0 {
		t.Errorf("the rejected submission recorded %d transaction(s): %+v", len(txs), txs)
	}
}

// The same for the form page itself: it must not render as though a transfer
// could be entered on it.
func TestTheGenericFormPageRefusesATransferType(t *testing.T) {
	t.Parallel()

	fs := NewFakeServices()
	router := testRouter(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/transactions/new?type=TRANSFER", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the body: %v", err)
		}
	}()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /transactions/new?type=TRANSFER returned %d, want 400", res.StatusCode)
	}
}
