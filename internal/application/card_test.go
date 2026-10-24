package application_test

import (
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

const methodPOS int64 = 2 // seeded by migration 00008

var keySeq atomic.Int64

// key returns a fresh request key, as a form renders one.
func key() string { return fmt.Sprintf("card-test-%012d", keySeq.Add(1)) }

// plazaBlack is the card of the M8.1 acceptance case: bolívares, closing on the
// 25th, no known limit or due date, assumed nominal and not yet confirmed.
func plazaBlack(requestKey string) application.OpenCreditCardInput {
	return application.OpenCreditCardInput{
		RequestKey:   requestKey,
		Name:         "Plaza Black",
		Institution:  ptr("Banco Plaza"),
		CurrencyCode: "VES",
		Terms: creditcard.TermsInput{
			ProductName:  ptr("Mastercard Black"),
			StatementDay: 25,
		},
	}
}

func expenseCategory(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	c, err := category.NewService(db).Create(t.Context(), category.CreateInput{Name: name, Type: category.Expense})
	if err != nil {
		t.Fatalf("creating category %q: %v", name, err)
	}
	return c.ID
}

func rows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

func TestOpenCreditCard(t *testing.T) {
	t.Parallel()
	app, ledgerSvc, _, db := fixture(t)
	ctx := t.Context()

	in := plazaBlack(key())
	in.OpeningDebt, in.OpeningDebtDate = ptr("1250.00"), "2026-09-01"
	result, err := app.OpenCreditCard(ctx, in)
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	if result.Account.Type != account.CreditCard || result.Account.Type.Nature() != account.Liability {
		t.Errorf("the account is %s", result.Account.Type)
	}
	card := result.Card
	if card.StatementDay != 25 || card.CreditLimitMinor != nil || card.DueDaysAfterStatement != nil ||
		card.Denomination != creditcard.Nominal || card.DenominationConfirmed {
		t.Errorf("terms = %+v, want day 25, no limit, no due date, NOMINAL unconfirmed", card)
	}
	if result.OpeningBalance == nil || result.OpeningBalance.AmountMinor != -125000 {
		t.Fatalf("opening balance = %+v, want -125000", result.OpeningBalance)
	}
	balance, err := ledgerSvc.GetAccountBalance(ctx, result.Account.ID)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != -125000 {
		t.Errorf("balance = %d, want -125000: a debt is a negative balance", balance.BalanceMinor)
	}

	// Submitted twice, the form opens one card.
	again, err := app.OpenCreditCard(ctx, in)
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if again.Account.ID != result.Account.ID || again.OpeningBalance == nil || again.OpeningBalance.ID != result.OpeningBalance.ID {
		t.Errorf("the replay returned %+v, want the card already opened", again)
	}
	if accounts, transactions, _ := counts(t, db); accounts != 1 || transactions != 1 || rows(t, db, "credit_cards") != 1 {
		t.Errorf("after a replay: %d accounts, %d transactions", accounts, transactions)
	}
}

// Opening a card is all or nothing: terms that fail after the account was
// written leave no account behind.
func TestOpenCreditCardIsAtomic(t *testing.T) {
	t.Parallel()
	app, _, _, db := fixture(t)
	ctx := t.Context()

	for name, mutate := range map[string]func(*application.OpenCreditCardInput){
		"a statement day of 32":     func(in *application.OpenCreditCardInput) { in.Terms.StatementDay = 32 },
		"a limit of zero":           func(in *application.OpenCreditCardInput) { in.Terms.CreditLimit = ptr("0") },
		"an unknown payment method": func(in *application.OpenCreditCardInput) { id := int64(9999); in.Terms.DefaultPaymentMethodID = &id },
		"a signed previous debt":    func(in *application.OpenCreditCardInput) { in.OpeningDebt = ptr("-10") },
		"a debt with no date":       func(in *application.OpenCreditCardInput) { in.OpeningDebt = ptr("10") },
		"a malformed request key":   func(in *application.OpenCreditCardInput) { in.RequestKey = "short" },
		"an unknown currency":       func(in *application.OpenCreditCardInput) { in.CurrencyCode = "XXX" },
		"a limit finer than a cent": func(in *application.OpenCreditCardInput) { in.Terms.CreditLimit = ptr("10.001") },
	} {
		in := plazaBlack(key())
		mutate(&in)
		if _, err := app.OpenCreditCard(ctx, in); err == nil {
			t.Errorf("a card with %s was opened", name)
		}
	}
	if accounts, transactions, _ := counts(t, db); accounts != 0 || transactions != 0 || rows(t, db, "credit_cards") != 0 ||
		rows(t, db, "operation_requests") != 0 {
		t.Errorf("failed openings left %d accounts and %d transactions", accounts, transactions)
	}
}

// The generic form opens assets; a card goes through its own.
func TestOpenAccountRefusesACreditCard(t *testing.T) {
	t.Parallel()
	app, _, _, db := fixture(t)
	_, err := app.OpenAccount(t.Context(), application.OpenAccountInput{
		Account: account.CreateInput{Name: "Plaza", Type: account.CreditCard, CurrencyCode: "VES"},
	})
	if !errors.Is(err, account.ErrInvalidInput) {
		t.Errorf("OpenAccount(CREDIT_CARD) = %v, want ErrInvalidInput", err)
	}
	if accounts, _, _ := counts(t, db); accounts != 0 {
		t.Errorf("%d accounts written", accounts)
	}
}

type cardWorld struct {
	app  *application.Service
	db   *sql.DB
	card int64
	bank int64
	food int64
	fees int64
}

func newCardWorld(t *testing.T) cardWorld {
	t.Helper()
	app, _, _, db := fixture(t)
	opened, err := app.OpenCreditCard(t.Context(), plazaBlack(key()))
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	bank, err := app.OpenAccount(t.Context(), application.OpenAccountInput{Account: account.CreateInput{
		Name: "Banesco", Type: account.Bank, Subtype: checking(), CurrencyCode: "VES",
	}})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	return cardWorld{
		app: app, db: db, card: opened.Account.ID, bank: bank.Account.ID,
		food: expenseCategory(t, db, "Comida"), fees: expenseCategory(t, db, "Comisiones bancarias"),
	}
}

func (w cardWorld) purchaseInput(accountID int64, amount, date string) ledger.RecordExpenseInput {
	return ledger.RecordExpenseInput{
		AccountID: accountID, CategoryID: w.food, Amount: amount, PaymentMethodID: methodPOS, EffectiveDate: date,
	}
}

// A purchase is one movement, on the card: no asset is touched (I3). It is
// recorded once however many times the form is submitted, and only on a card.
func TestRecordCardPurchase(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	requestKey := key()

	view, err := w.app.RecordCardPurchase(ctx, requestKey, w.purchaseInput(w.card, "10000.00", "2026-08-20"))
	if err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}
	if view.Type != ledger.Expense || view.AccountID != w.card || view.AmountMinor != -1000000 {
		t.Errorf("purchase = %s on %d for %d", view.Type, view.AccountID, view.AmountMinor)
	}
	replayed, err := w.app.RecordCardPurchase(ctx, requestKey, w.purchaseInput(w.card, "10000.00", "2026-08-20"))
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if replayed.ID != view.ID {
		t.Errorf("the replay recorded transaction %d, want %d", replayed.ID, view.ID)
	}
	if _, transactions, movements := counts(t, w.db); transactions != 1 || movements != 1 {
		t.Errorf("%d transactions and %d movements, want 1 and 1", transactions, movements)
	}

	if _, err := w.app.RecordCardPurchase(ctx, key(), w.purchaseInput(w.bank, "1", "2026-08-20")); !errors.Is(err, creditcard.ErrNotACreditCard) {
		t.Errorf("a card purchase on a bank account = %v, want ErrNotACreditCard", err)
	}
}

// A charge is an expense on the card marked with its kind. A generic edit
// cannot reach it; its own edit corrects both, and refuses to move it off a
// card -- rolling the whole correction back.
func TestCardCharges(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()

	charge, err := w.app.RecordCardCharge(ctx, key(), creditcard.Interest, ledger.RecordExpenseInput{
		AccountID: w.card, CategoryID: w.fees, Amount: "300.00", PaymentMethodID: methodPOS, EffectiveDate: "2026-10-25",
	})
	if err != nil {
		t.Fatalf("RecordCardCharge: %v", err)
	}
	kind, ok, err := creditcard.NewService(w.db).ChargeKindOf(ctx, charge.ID)
	if err != nil || !ok || kind != creditcard.Interest {
		t.Errorf("charge kind = %q, %v, %v", kind, ok, err)
	}

	edit := ledger.UpdateTransactionInput{
		AccountID: w.card, Type: ledger.Income, CategoryID: w.fees, PaymentMethodID: methodPOS,
		Amount: "300.00", EffectiveDate: "2026-10-25",
	}
	if _, err := w.app.UpdateTransaction(ctx, charge.ID, edit); !errors.Is(err, creditcard.ErrChargeEditedAsCharge) {
		t.Errorf("a generic edit of a charge = %v, want ErrChargeEditedAsCharge", err)
	}

	edit.Amount = "100.00"
	view, err := w.app.UpdateCardCharge(ctx, charge.ID, creditcard.Penalty, edit)
	if err != nil {
		t.Fatalf("UpdateCardCharge: %v", err)
	}
	if view.Type != ledger.Expense || view.AmountMinor != -10000 {
		t.Errorf("corrected charge = %s %d, want an EXPENSE of -10000", view.Type, view.AmountMinor)
	}
	if kind, _, _ := creditcard.NewService(w.db).ChargeKindOf(ctx, charge.ID); kind != creditcard.Penalty {
		t.Errorf("kind = %q after the correction, want PENALTY", kind)
	}

	edit.AccountID = w.bank
	if _, err := w.app.UpdateCardCharge(ctx, charge.ID, creditcard.Fee, edit); !errors.Is(err, creditcard.ErrNotACharge) {
		t.Errorf("moving a charge to a bank account = %v, want ErrNotACharge", err)
	}
	if view, err := ledger.NewService(w.db).GetTransaction(ctx, charge.ID); err != nil || view.AccountID != w.card || view.AmountMinor != -10000 {
		t.Errorf("the refused correction left %+v, %v", view, err)
	}

	purchase, err := w.app.RecordCardPurchase(ctx, key(), w.purchaseInput(w.card, "5", "2026-10-01"))
	if err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}
	if _, err := w.app.UpdateCardCharge(ctx, purchase.ID, creditcard.Fee, edit); !errors.Is(err, creditcard.ErrNotACharge) {
		t.Errorf("correcting a purchase as a charge = %v, want ErrNotACharge", err)
	}
}

// A failure after the expense was written rolls the expense back: a charge with
// an unknown kind leaves nothing.
func TestARefusedChargeLeavesNothing(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	requests := rows(t, w.db, "operation_requests")
	_, err := w.app.RecordCardCharge(t.Context(), key(), "CASHBACK", ledger.RecordExpenseInput{
		AccountID: w.card, CategoryID: w.fees, Amount: "1.00", PaymentMethodID: methodPOS, EffectiveDate: "2026-10-25",
	})
	if !errors.Is(err, creditcard.ErrInvalidInput) {
		t.Fatalf("an unknown charge kind = %v, want ErrInvalidInput", err)
	}
	if _, transactions, movements := counts(t, w.db); transactions != 0 || movements != 0 ||
		rows(t, w.db, "operation_requests") != requests {
		t.Errorf("a refused charge left %d transactions and %d movements", transactions, movements)
	}
}

// A refund on a card lowers the debt, once per request key.
func TestRecordRefundOnACard(t *testing.T) {
	t.Parallel()
	w := newCardWorld(t)
	ctx := t.Context()
	purchase, err := w.app.RecordCardPurchase(ctx, key(), w.purchaseInput(w.card, "10000.00", "2026-08-20"))
	if err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}
	requestKey := key()
	in := ledger.RecordRefundInput{
		AccountID: w.card, CategoryID: w.food, Amount: "2000.00", PaymentMethodID: methodPOS,
		EffectiveDate: "2026-10-02", OriginalTransactionID: &purchase.ID,
	}
	refund, err := w.app.RecordRefund(ctx, requestKey, in)
	if err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}
	if again, err := w.app.RecordRefund(ctx, requestKey, in); err != nil || again.ID != refund.ID {
		t.Errorf("replay = %d, %v; want %d", again.ID, err, refund.ID)
	}
	balance, err := ledger.NewService(w.db).GetAccountBalance(ctx, w.card)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if balance.BalanceMinor != -800000 {
		t.Errorf("debt balance = %d, want -800000", balance.BalanceMinor)
	}

	in.Amount = "1000.00"
	updated, err := w.app.UpdateRefund(ctx, refund.ID, ledger.UpdateRefundInput{
		AccountID: in.AccountID, CategoryID: in.CategoryID, Amount: in.Amount, PaymentMethodID: in.PaymentMethodID,
		EffectiveDate: in.EffectiveDate, OriginalTransactionID: in.OriginalTransactionID,
	})
	if err != nil || updated.AmountMinor != 100000 {
		t.Errorf("UpdateRefund = %d, %v", updated.AmountMinor, err)
	}
}
