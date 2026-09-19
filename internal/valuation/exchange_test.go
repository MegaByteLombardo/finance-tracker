package valuation_test

import (
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// M5.5 adds a transaction that changes two currencies' balances at once. What
// this file pins down is that valuation was not taught anything about it.
//
// The net worth is derived from balances and REFERENCE rates. An FX_EXCHANGE
// moves balances, so the figure moves; the rate the exchange happened at takes
// no part in it, and nothing reconciles the two. That is not an oversight: a
// valuation says what the money is worth today according to a published source,
// and what the user paid for it last Tuesday is a different question.

// An exchange at the reference rate leaves the net worth unchanged, because the
// same wealth is simply denominated differently afterwards.
func TestAnExchangeAtTheReferenceRateLeavesTheNetWorthUnchanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	usd := h.openAccount(t, "Mercantil USD", "USD", "100.00")
	ves := h.openAccount(t, "Banesco VES", "VES", "100000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	on := mustDate(t, "2026-09-11")

	before, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt before: %v", err)
	}
	// 100 USD + 100,000 VES / 500 = 100 + 200 = 300.00 USD.
	if before.TotalMinor != 30000 {
		t.Fatalf("the net worth before is %d, want 30000", before.TotalMinor)
	}

	// 50,000 VES for 100 USD is exactly the published rate.
	if _, err := h.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      ves.ID,
		SourceAmount:         "50000.00",
		DestinationAccountID: usd.ID,
		DestinationAmount:    "100.00",
		EffectiveDate:        "2026-09-11",
	}); err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	after, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt after: %v", err)
	}
	if after.TotalMinor != before.TotalMinor {
		t.Errorf("the net worth is %d after an exchange at the reference rate, want %d",
			after.TotalMinor, before.TotalMinor)
	}
	if !after.Complete {
		t.Errorf("the valuation is incomplete: %+v", after.Missing)
	}
}

// An exchange at a rate different from the reference one DOES change the valued
// net worth, and that is correct rather than a discrepancy to reconcile.
//
// Nothing turns the difference into an expense, an income, a fee, a gain or a
// loss. There is no such row, no such column, and no code that would create one.
func TestAnExchangeAwayFromTheReferenceRateMovesTheNetWorthAndCreatesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	usd := h.openAccount(t, "Mercantil USD", "USD", "100.00")
	ves := h.openAccount(t, "Banesco VES", "VES", "100000.00")
	h.seedOfficial(t, "USD", "VES", "2026-09-11", "500")

	on := mustDate(t, "2026-09-11")

	before, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt before: %v", err)
	}

	// 60,000 VES for 100 USD: 600 VES per dollar, worse than the official 500.
	if _, err := h.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      ves.ID,
		SourceAmount:         "60000.00",
		DestinationAccountID: usd.ID,
		DestinationAmount:    "100.00",
		EffectiveDate:        "2026-09-11",
	}); err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	after, err := h.valuation.GetNetWorthAt(t.Context(), on)
	if err != nil {
		t.Fatalf("GetNetWorthAt after: %v", err)
	}

	// 200 USD + 40,000 VES / 500 = 200 + 80 = 280.00 USD, down from 300.00.
	if after.TotalMinor != 28000 {
		t.Errorf("the net worth is %d after the exchange, want 28000", after.TotalMinor)
	}
	if after.TotalMinor >= before.TotalMinor {
		t.Errorf("paying above the official rate did not reduce the valued net worth: %d then %d",
			before.TotalMinor, after.TotalMinor)
	}

	// The difference exists nowhere as a row. Only the two movements of the
	// exchange were written, and neither carries a category.
	var categorised int
	if err := h.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM transactions
		 WHERE transaction_type IN ('INCOME', 'EXPENSE')
		   AND id NOT IN (SELECT id FROM transactions WHERE transaction_type = 'OPENING_BALANCE')
	`).Scan(&categorised); err != nil {
		t.Fatalf("counting income and expense transactions: %v", err)
	}
	if categorised != 0 {
		t.Errorf("%d income/expense transactions exist; the FX difference was turned into one", categorised)
	}
}

// The rate an exchange happened at never becomes a reference rate, so it never
// values anything.
func TestAnExchangeDoesNotBecomeAReferenceRate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	usd := h.openAccount(t, "Mercantil USD", "USD", "100.00")
	ves := h.openAccount(t, "Banesco VES", "VES", "100000.00")

	// Deliberately NO reference rate is seeded.
	if _, err := h.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      ves.ID,
		SourceAmount:         "50000.00",
		DestinationAccountID: usd.ID,
		DestinationAmount:    "100.00",
		EffectiveDate:        "2026-09-11",
	}); err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	worth, err := h.valuation.GetNetWorthAt(t.Context(), mustDate(t, "2026-09-11"))
	if err != nil {
		t.Fatalf("GetNetWorthAt: %v", err)
	}

	// The valuation reports the missing pair rather than quietly adopting the
	// rate the user just traded at. A missing rate is reported, never guessed.
	if worth.Complete {
		t.Error("the valuation claimed to be complete with no reference rate for VES")
	}
	if len(worth.Missing) == 0 {
		t.Error("the valuation reports no missing pair")
	}

	var rates int
	if err := h.db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rates); err != nil {
		t.Fatalf("counting exchange rates: %v", err)
	}
	if rates != 0 {
		t.Errorf("%d exchange rates exist; recording an exchange created one", rates)
	}
}
