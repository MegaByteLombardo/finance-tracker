package creditcard_test

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

var keySeq atomic.Int64

func key() string { return fmt.Sprintf("cc-test-%012d", keySeq.Add(1)) }

func ptr(s string) *string { return &s }

// Today is 2026-10-15 in Venezuela for every test here.
func today() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, database.EconomicLocation) }

// The standing is derived from the ledger as of today and the terms: debt,
// credit balance and -- with a known limit -- what is available. A movement
// dated after today is counted and not summed (TD-063).
func TestPosition(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	app := application.NewService(db, savings.NewService(db))
	cards := creditcard.NewService(db)
	creditcard.SetClock(cards, today)

	opened, err := app.OpenCreditCard(ctx, application.OpenCreditCardInput{
		RequestKey: key(), Name: "Plaza Black", CurrencyCode: "VES",
		Terms: creditcard.TermsInput{StatementDay: 25, CreditLimit: ptr("5000.00")},
	})
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	card := opened.Account.ID
	food, err := category.NewService(db).Create(ctx, category.CreateInput{Name: "Comida", Type: category.Expense})
	if err != nil {
		t.Fatalf("creating a category: %v", err)
	}
	for _, p := range []struct{ amount, date string }{{"1200.00", "2026-10-01"}, {"300.00", "2026-10-20"}} {
		if _, err := app.RecordCardPurchase(ctx, key(), ledger.RecordExpenseInput{
			AccountID: card, CategoryID: food.ID, Amount: p.amount, PaymentMethodID: 2, EffectiveDate: p.date,
		}); err != nil {
			t.Fatalf("RecordCardPurchase: %v", err)
		}
	}

	view, err := cards.Get(ctx, card)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	p := view.Position
	if p.BalanceMinor != -120000 || p.DebtMinor != 120000 || p.CreditBalanceMinor != 0 ||
		!p.HasLimit || p.AvailableMinor != 380000 || p.FutureMovementCount != 1 {
		t.Errorf("position = %+v", p)
	}

	list, err := cards.List(ctx, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Position != p || list[0].MinorUnit != 2 {
		t.Errorf("List = %+v", list)
	}
}

// Terms are replaced as a whole and validated against the card: the limit at
// its currency's scale, the payment method active. A bank account has no terms.
func TestUpdateTerms(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	app := application.NewService(db, savings.NewService(db))
	cards := creditcard.NewService(db)

	opened, err := app.OpenCreditCard(ctx, application.OpenCreditCardInput{
		RequestKey: key(), Name: "Plaza Black", CurrencyCode: "VES",
		Terms: creditcard.TermsInput{StatementDay: 25},
	})
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	due := 20
	method := int64(2)
	updated, err := cards.UpdateTerms(ctx, opened.Account.ID, creditcard.TermsInput{
		StatementDay: 31, CreditLimit: ptr("1500000.50"), DueDaysAfterStatement: &due, PaymentProcessingDays: 2,
		Denomination: "indexed", DenominationConfirmed: true, DefaultPaymentMethodID: &method,
	})
	if err != nil {
		t.Fatalf("UpdateTerms: %v", err)
	}
	if updated.StatementDay != 31 || updated.CreditLimitMinor == nil || *updated.CreditLimitMinor != 150000050 ||
		updated.Denomination != creditcard.Indexed || !updated.DenominationConfirmed {
		t.Errorf("updated terms = %+v", updated)
	}

	for name, in := range map[string]creditcard.TermsInput{
		"day 0":               {StatementDay: 0},
		"61 due days":         {StatementDay: 25, DueDaysAfterStatement: ptrInt(61)},
		"a negative limit":    {StatementDay: 25, CreditLimit: ptr("-1")},
		"an unknown kind":     {StatementDay: 25, Denomination: "UVC"},
		"11 processing days":  {StatementDay: 25, PaymentProcessingDays: 11},
		"an unknown method":   {StatementDay: 25, DefaultPaymentMethodID: ptrInt64(9999)},
		"a limit in letters":  {StatementDay: 25, CreditLimit: ptr("mucho")},
		"a limit too precise": {StatementDay: 25, CreditLimit: ptr("1.005")},
	} {
		if _, err := cards.UpdateTerms(ctx, opened.Account.ID, in); err == nil {
			t.Errorf("terms with %s were accepted", name)
		}
	}

	bank, err := account.NewService(db).Create(ctx, account.CreateInput{Name: "Efectivo", Type: account.Cash, CurrencyCode: "VES"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := cards.Get(ctx, bank.ID); !errors.Is(err, creditcard.ErrNotACreditCard) {
		t.Errorf("Get on a cash account = %v, want ErrNotACreditCard", err)
	}
	if _, err := cards.UpdateTerms(ctx, bank.ID, creditcard.TermsInput{StatementDay: 25}); !errors.Is(err, creditcard.ErrNotFound) {
		t.Errorf("UpdateTerms on a cash account = %v, want ErrNotFound", err)
	}
}

func ptrInt(v int) *int       { return &v }
func ptrInt64(v int64) *int64 { return &v }
