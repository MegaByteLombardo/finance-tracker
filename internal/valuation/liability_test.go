package valuation_test

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// A card is valued like any other account -- a debt is a negative balance,
// converted once per currency -- and the total splits exactly into what is held
// and what is owed (I2). A purchase entered today with an earlier date moves
// the history from its own date, not from today.
func TestAssetsMinusLiabilitiesIsTheNetWorth(t *testing.T) {
	t.Parallel()
	h := newHistory(t)
	ctx := t.Context()
	app := application.NewService(h.db, savings.NewService(h.db))

	cash := h.open(t, "Efectivo", "USD", "1000.00", "2026-09-01")
	opened, err := app.OpenCreditCard(ctx, application.OpenCreditCardInput{
		RequestKey: "valuation-test-card-0001", Name: "Plaza Black", CurrencyCode: "VES",
		Terms:       creditcard.TermsInput{StatementDay: 25},
		OpeningDebt: ptr("10000.00"), OpeningDebtDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("OpenCreditCard: %v", err)
	}
	card := opened.Account.ID
	h.seedOfficial(t, "USD", "VES", "2026-09-01", "250")

	worth, err := h.valuation.GetNetWorthAt(ctx, mustDate(t, "2026-09-10"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}
	if worth.TotalMinor != 96000 || worth.AssetsMinor != 100000 || worth.LiabilitiesMinor != 4000 {
		t.Errorf("total %d, assets %d, liabilities %d; want 96000, 100000, 4000",
			worth.TotalMinor, worth.AssetsMinor, worth.LiabilitiesMinor)
	}
	if worth.AssetsMinor-worth.LiabilitiesMinor != worth.TotalMinor {
		t.Error("assets minus liabilities is not the net worth")
	}
	for _, line := range worth.Accounts {
		switch line.AccountID {
		case cash.ID:
			if line.Nature != account.Asset || !line.Share.Defined || line.Share.BasisPoints != 10000 {
				t.Errorf("cash line = %+v, want an asset holding all of the assets", line)
			}
		case card:
			if line.Nature != account.Liability || line.Share.Defined || line.ConvertedMinor != -4000 {
				t.Errorf("card line = %+v, want a liability of -4000 with no share", line)
			}
		}
	}

	// A purchase recorded today, dated 2026-09-05, already billed by the bank:
	// the history changes from that day.
	food := h.food
	if _, err := app.RecordCardPurchase(ctx, "valuation-test-buy-00001", ledger.RecordExpenseInput{
		AccountID: card, CategoryID: food, Amount: "2500.00", PaymentMethodID: methodCash, EffectiveDate: "2026-09-05",
	}); err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}
	expectTotals(t, h.series(t, "2026-09-04", "2026-09-06"), map[string]int64{
		"2026-09-04": 96000,
		"2026-09-05": 95000,
		"2026-09-06": 95000,
	})
}

func ptr(s string) *string { return &s }
