package migrations_test

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
)

const insertCreditCard = `
	INSERT INTO credit_cards (account_id, product_name, credit_limit_minor, statement_day,
	                          due_days_after_statement, payment_processing_days, denomination,
	                          denomination_confirmed, default_payment_method_id)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// The shape of a card's terms (00020). Unknown is NULL, never zero.
func TestCreditCardConstraints(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		product, limit, day, due, processing, denomination, confirmed, method any
		accepted                                                              bool
	}{
		"everything unknown":         {nil, nil, 25, nil, 0, "NOMINAL", 0, nil, true},
		"everything stated":          {"Mastercard Black", 500000000, 25, 20, 2, "INDEXED", 1, 1, true},
		"day 31":                     {nil, nil, 31, nil, 0, "NOMINAL", 0, nil, true},
		"day 0":                      {nil, nil, 0, nil, 0, "NOMINAL", 0, nil, false},
		"day 32":                     {nil, nil, 32, nil, 0, "NOMINAL", 0, nil, false},
		"no statement day":           {nil, nil, nil, nil, 0, "NOMINAL", 0, nil, false},
		"zero limit":                 {nil, 0, 25, nil, 0, "NOMINAL", 0, nil, false},
		"real limit":                 {nil, 1000.5, 25, nil, 0, "NOMINAL", 0, nil, false},
		"due on the closing day":     {nil, nil, 25, 0, 0, "NOMINAL", 0, nil, false},
		"due after two months":       {nil, nil, 25, 61, 0, "NOMINAL", 0, nil, false},
		"eleven processing days":     {nil, nil, 25, nil, 11, "NOMINAL", 0, nil, false},
		"unknown denomination":       {nil, nil, 25, nil, 0, "UVC", 0, nil, false},
		"blank product":              {" ", nil, 25, nil, 0, "NOMINAL", 0, nil, false},
		"undefined payment method":   {nil, nil, 25, nil, 0, "NOMINAL", 0, 9999, false},
		"confirmation is a flag":     {nil, nil, 25, nil, 0, "NOMINAL", 2, nil, false},
		"processing days are needed": {nil, nil, 25, nil, nil, "NOMINAL", 0, nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := dbtest.New(t)
			result, err := db.ExecContext(t.Context(), insertAccount, "Plaza Black", "CREDIT_CARD", nil, "VES", "Banco Plaza", 1)
			if err != nil {
				t.Fatalf("seeding the card account: %v", err)
			}
			account, err := result.LastInsertId()
			if err != nil {
				t.Fatalf("reading the account id: %v", err)
			}
			_, err = db.ExecContext(t.Context(), insertCreditCard, account,
				tc.product, tc.limit, tc.day, tc.due, tc.processing, tc.denomination, tc.confirmed, tc.method)
			if tc.accepted && err != nil {
				t.Fatalf("refused valid terms: %v", err)
			}
			if !tc.accepted && err == nil {
				t.Fatal("accepted invalid terms")
			}
		})
	}
}

// A card's terms belong to an account that exists.
func TestCreditCardNeedsItsAccount(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	if _, err := db.ExecContext(t.Context(), insertCreditCard, 9999, nil, nil, 25, nil, 0, "NOMINAL", 0, nil); err == nil {
		t.Error("a card without an account was accepted")
	}
}

// The facts and assertions of 00021.
func TestCreditChargeRefundAndRequestConstraints(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	category := seedCategory(t, db, "Comisiones", "EXPENSE")
	insert := func(txType string) int64 {
		t.Helper()
		result, err := db.ExecContext(ctx, insertTransaction, txType, category, methodPOS, nil, "2026-10-05")
		if err != nil {
			t.Fatalf("seeding a %s: %v", txType, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("reading the id: %v", err)
		}
		return id
	}
	expense, refund := insert("EXPENSE"), insert("REFUND")

	for name, tc := range map[string]struct {
		query    string
		args     []any
		accepted bool
	}{
		"interest":                 {`INSERT INTO credit_charges (transaction_id, kind) VALUES (?, ?)`, []any{expense, "INTEREST"}, true},
		"unknown charge kind":      {`INSERT INTO credit_charges (transaction_id, kind) VALUES (?, ?)`, []any{refund, "CASHBACK"}, false},
		"charge of nothing":        {`INSERT INTO credit_charges (transaction_id, kind) VALUES (?, ?)`, []any{9999, "FEE"}, false},
		"refund linked to itself":  {`INSERT INTO transaction_refunds (refund_transaction_id, original_transaction_id) VALUES (?, ?)`, []any{refund, refund}, false},
		"refund of nothing":        {`INSERT INTO transaction_refunds (refund_transaction_id, original_transaction_id) VALUES (?, ?)`, []any{refund, 9999}, false},
		"request with nothing":     {`INSERT INTO operation_requests (request_key, transaction_id, account_id) VALUES (?, ?, ?)`, []any{"k-0000000000000001", nil, nil}, false},
		"short request key":        {`INSERT INTO operation_requests (request_key, transaction_id) VALUES (?, ?)`, []any{"short", expense}, false},
		"request key with a space": {`INSERT INTO operation_requests (request_key, transaction_id) VALUES (?, ?)`, []any{"k 00000000000000002", expense}, false},
	} {
		_, err := db.ExecContext(ctx, tc.query, tc.args...)
		if tc.accepted && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.accepted && err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO transaction_refunds (refund_transaction_id, original_transaction_id) VALUES (?, ?)`, refund, expense); err != nil {
		t.Fatalf("linking a refund: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO transaction_refunds (refund_transaction_id, original_transaction_id) VALUES (?, ?)`, refund, expense); err == nil {
		t.Error("a refund was linked twice")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO operation_requests (request_key, transaction_id) VALUES (?, ?)`, "k-0000000000000003", expense); err != nil {
		t.Fatalf("recording a request: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO operation_requests (request_key, transaction_id) VALUES (?, ?)`, "k-0000000000000003", refund); err == nil {
		t.Error("a request key was recorded twice")
	}
}
