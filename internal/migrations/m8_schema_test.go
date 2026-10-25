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

// The statements and assertions of 00022, and the card's refund policy.
func TestCreditStatementConstraints(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	result, err := db.ExecContext(ctx, insertAccount, "Plaza Black", "CREDIT_CARD", nil, "VES", "Banco Plaza", 1)
	if err != nil {
		t.Fatalf("seeding the card account: %v", err)
	}
	card, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the account id: %v", err)
	}
	if _, err := db.ExecContext(ctx, insertCreditCard, card, nil, nil, 25, nil, 0, "NOMINAL", 0, nil); err != nil {
		t.Fatalf("seeding the card: %v", err)
	}

	const insertStatement = `
		INSERT INTO credit_statements (card_account_id, period_start, closing_date, due_date,
		                               closing_balance_minor, minimum_payment_minor, payments_minor, past_due_minor)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	for name, tc := range map[string]struct {
		start, closing, due any
		balance, minimum    any
		payments, pastDue   any
		accepted            bool
	}{
		"everything stated":       {"2026-09-26", "2026-10-25", "2026-11-14", -1850000, 250000, 1000, 70000, true},
		"due date unknown":        {"2026-08-26", "2026-09-25", nil, -1000000, nil, nil, nil, true},
		"credit balance":          {"2026-07-26", "2026-08-25", nil, 50000, 0, nil, nil, true},
		"closing before start":    {"2026-12-26", "2026-12-25", nil, 0, nil, nil, nil, false},
		"due on the closing day":  {"2026-11-26", "2026-12-25", "2026-12-25", 0, nil, nil, nil, false},
		"negative minimum":        {"2026-11-26", "2026-12-25", nil, 0, -1, nil, nil, false},
		"negative payments":       {"2026-11-26", "2026-12-25", nil, 0, nil, -1, nil, false},
		"negative past due":       {"2026-11-26", "2026-12-25", nil, 0, nil, nil, -1, false},
		"impossible date":         {"2026-02-01", "2026-02-31", nil, 0, nil, nil, nil, false},
		"no balance":              {"2026-11-26", "2026-12-25", nil, nil, nil, nil, nil, false},
		"real balance":            {"2026-11-26", "2026-12-25", nil, 10.5, nil, nil, nil, false},
		"statement of a non-card": {"2026-11-26", "2026-12-25", nil, 0, nil, nil, nil, false},
	} {
		owner := card
		if name == "statement of a non-card" {
			owner = 9999
		}
		_, err := db.ExecContext(ctx, insertStatement, owner, tc.start, tc.closing, tc.due, tc.balance, tc.minimum, tc.payments, tc.pastDue)
		if tc.accepted && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.accepted && err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := db.ExecContext(ctx, insertStatement, card, "2026-10-01", "2026-10-25", nil, 0, nil, nil, nil); err == nil {
		t.Error("two statements of one card closed on the same day")
	}

	var statement int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM credit_statements WHERE closing_date = '2026-10-25'`).Scan(&statement); err != nil {
		t.Fatalf("reading the statement: %v", err)
	}
	category := seedCategory(t, db, "Supermercado", "EXPENSE")
	result, err = db.ExecContext(ctx, insertTransaction, "EXPENSE", category, methodPOS, nil, "2026-10-24")
	if err != nil {
		t.Fatalf("seeding a purchase: %v", err)
	}
	purchase, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the id: %v", err)
	}
	const assign = `INSERT INTO credit_statement_assignments (transaction_id, statement_id) VALUES (?, ?)`
	if _, err := db.ExecContext(ctx, assign, purchase, 9999); err == nil {
		t.Error("an assertion to no statement was accepted")
	}
	if _, err := db.ExecContext(ctx, assign, 9999, statement); err == nil {
		t.Error("an assertion of no transaction was accepted")
	}
	if _, err := db.ExecContext(ctx, assign, purchase, statement); err != nil {
		t.Fatalf("asserting a membership: %v", err)
	}
	if _, err := db.ExecContext(ctx, assign, purchase, statement); err == nil {
		t.Error("a transaction was asserted into two statements")
	}

	var policy string
	if err := db.QueryRowContext(ctx, `SELECT refund_policy FROM credit_cards WHERE account_id = ?`, card).Scan(&policy); err != nil {
		t.Fatalf("reading the refund policy: %v", err)
	}
	if policy != "NEXT_STATEMENT" {
		t.Errorf("a card's refund policy defaults to %q, want NEXT_STATEMENT", policy)
	}
	if _, err := db.ExecContext(ctx, `UPDATE credit_cards SET refund_policy = 'SOMETIMES' WHERE account_id = ?`, card); err == nil {
		t.Error("an unknown refund policy was accepted")
	}
}
