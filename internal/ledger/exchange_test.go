package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// Currency exchanges between two of the user's own accounts.
//
// The invariant every test here is ultimately about is the MIRROR of the one
// transfers maintain: an exchange does not preserve the total held in a
// currency, and it must not. Bolívares go down, dollars go up, and the two
// magnitudes have no arithmetic relation to check.

// --- helpers ----------------------------------------------------------------

// exchangeWorld is a world with accounts in three currencies, which is the
// minimum an exchange test needs: a pair to trade between, a second account in
// one of those currencies to prove the rules are about currencies rather than
// about particular accounts, and a third currency so that a pair with no USD in
// it can be exercised.
type exchangeWorld struct {
	*world

	banescoVES   int64 // BANK, VES
	efectivoVES  int64 // CASH, VES
	mercantilUSD int64
	walletEUR    int64 // WALLET, EUR
}

func newExchangeWorld(t *testing.T) *exchangeWorld {
	t.Helper()

	w := newWorld(t)
	return &exchangeWorld{
		world:        w,
		banescoVES:   w.newTypedAccount(t, "Banesco VES", "VES", account.Bank, checking()),
		efectivoVES:  w.newTypedAccount(t, "Efectivo VES", "VES", account.Cash, nil),
		mercantilUSD: w.usdAccount,
		walletEUR:    w.newTypedAccount(t, "Wallet EUR", "EUR", account.Wallet, nil),
	}
}

// fund puts money into an account without going through an exchange.
func (w *exchangeWorld) fund(t *testing.T, accountID int64, amount string) {
	t.Helper()

	if _, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: accountID, Amount: amount, EffectiveDate: "2026-09-01",
	}); err != nil {
		t.Fatalf("funding account %d with %s: %v", accountID, amount, err)
	}
}

func (w *exchangeWorld) balance(t *testing.T, accountID int64) int64 {
	t.Helper()

	balance, err := w.ledger.GetAccountBalance(t.Context(), accountID)
	if err != nil {
		t.Fatalf("reading the balance of account %d: %v", accountID, err)
	}
	return balance.BalanceMinor
}

// assertExchangesAreWellFormed checks, in SQL, that every stored exchange has
// the shape the service promises: two movements, one negative and one positive,
// in two different accounts of DIFFERENT currencies, with no category and no
// payment method.
//
// Note what is deliberately absent: nothing compares the two magnitudes. A
// transfer's equivalent check sums them and demands zero; doing that here would
// assert that 8,422,067 céntimos and 10,000 cents are related, which they are
// not.
//
// It is asserted against the database rather than against the value the service
// returned, because what matters is what was written.
func (w *exchangeWorld) assertExchangesAreWellFormed(t *testing.T) {
	t.Helper()

	var broken int
	err := w.db.QueryRowContext(t.Context(), `
		SELECT count(*) FROM transactions t
		 WHERE t.transaction_type = 'FX_EXCHANGE'
		   AND (
		        (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id) <> 2
		     OR (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id AND m.amount_minor < 0) <> 1
		     OR (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id AND m.amount_minor > 0) <> 1
		     OR (SELECT count(DISTINCT m.account_id) FROM account_movements m WHERE m.transaction_id = t.id) <> 2
		     OR (SELECT count(DISTINCT a.currency_code)
		           FROM account_movements m JOIN accounts a ON a.id = m.account_id
		          WHERE m.transaction_id = t.id) <> 2
		     OR t.category_id IS NOT NULL
		     OR t.payment_method_id IS NOT NULL
		   )`).Scan(&broken)
	if err != nil {
		t.Fatalf("checking the shape of stored exchanges: %v", err)
	}
	if broken != 0 {
		t.Errorf("%d stored exchange(s) are not two opposite movements in two different currencies", broken)
	}
}

// --- recording --------------------------------------------------------------

func TestRecordExchangeBuyingDollarsWithBolivares(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	w.fund(t, w.banescoVES, "100000.00")

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		Description:          ptr("Compra de divisas"),
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	if view.Type != ledger.FXExchange {
		t.Errorf("the transaction type is %s, want %s", view.Type, ledger.FXExchange)
	}
	if view.CategoryID != nil {
		t.Errorf("the exchange has category %d; an exchange has none", *view.CategoryID)
	}
	if view.PaymentMethodID != nil {
		t.Errorf("the exchange has payment method %d; an exchange has none", *view.PaymentMethodID)
	}
	if view.Transfer != nil {
		t.Error("the exchange was assembled as a transfer")
	}

	detail := view.Exchange
	if detail == nil {
		t.Fatal("the view carries no exchange detail")
	}
	if detail.SourceAccountID != w.banescoVES || detail.SourceCurrencyCode != "VES" {
		t.Errorf("the source is account %d in %s, want %d in VES",
			detail.SourceAccountID, detail.SourceCurrencyCode, w.banescoVES)
	}
	if detail.DestinationAccountID != w.mercantilUSD || detail.DestinationCurrencyCode != "USD" {
		t.Errorf("the destination is account %d in %s, want %d in USD",
			detail.DestinationAccountID, detail.DestinationCurrencyCode, w.mercantilUSD)
	}
	if detail.SourceAmountMinor != 8500000 {
		t.Errorf("the delivered amount is %d minor units, want 8500000", detail.SourceAmountMinor)
	}
	if detail.DestinationAmountMinor != 10000 {
		t.Errorf("the received amount is %d minor units, want 10000", detail.DestinationAmountMinor)
	}
	if detail.SourceAmountFormatted != "85000.00" || detail.DestinationAmountFormatted != "100.00" {
		t.Errorf("the amounts are formatted %q and %q, want \"85000.00\" and \"100.00\"",
			detail.SourceAmountFormatted, detail.DestinationAmountFormatted)
	}

	// 1 USD = 850 VES, exactly, at the smallest scale that says so.
	rate := detail.ExecutionRate
	if rate.BaseCurrencyCode != "USD" || rate.QuoteCurrencyCode != "VES" {
		t.Errorf("the rate is quoted 1 %s = x %s, want 1 USD = x VES",
			rate.BaseCurrencyCode, rate.QuoteCurrencyCode)
	}
	if rate.RateFormatted != "850" {
		t.Errorf("the execution rate is %q, want \"850\"", rate.RateFormatted)
	}
	if !rate.Exact {
		t.Error("850 is exact and was reported as approximate")
	}

	// The movements themselves: negative in the source, positive in the
	// destination, and nothing relating the two magnitudes.
	movements := w.movementsOf(t, view.ID)
	if len(movements) != 2 {
		t.Fatalf("the exchange has %d movements, want 2", len(movements))
	}
	if movements[0].accountID != w.banescoVES || movements[0].amountMinor != -8500000 {
		t.Errorf("the first movement is %+v, want account %d at -8500000", movements[0], w.banescoVES)
	}
	if movements[1].accountID != w.mercantilUSD || movements[1].amountMinor != 10000 {
		t.Errorf("the second movement is %+v, want account %d at 10000", movements[1], w.mercantilUSD)
	}

	// The balances moved in opposite directions, in two different currencies.
	if got := w.balance(t, w.banescoVES); got != 1500000 {
		t.Errorf("Banesco holds %d after the exchange, want 1500000", got)
	}
	if got := w.balance(t, w.mercantilUSD); got != 10000 {
		t.Errorf("Mercantil holds %d after the exchange, want 10000", got)
	}

	w.assertExchangesAreWellFormed(t)
	w.assertNoOrphanTransactions(t)
}

func TestRecordExchangeSellingDollarsForBolivares(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	w.fund(t, w.mercantilUSD, "500.00")

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.mercantilUSD,
		SourceAmount:         "100.00",
		DestinationAccountID: w.banescoVES,
		DestinationAmount:    "84500.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	detail := view.Exchange
	if detail == nil {
		t.Fatal("the view carries no exchange detail")
	}
	if detail.SourceCurrencyCode != "USD" || detail.DestinationCurrencyCode != "VES" {
		t.Errorf("the exchange runs %s -> %s, want USD -> VES",
			detail.SourceCurrencyCode, detail.DestinationCurrencyCode)
	}

	// The direction of the accounts is what says this was a sale. The rate is
	// still quoted in bolívares per dollar, which is how it was quoted to the
	// user, so a purchase and a sale can be compared at a glance.
	rate := detail.ExecutionRate
	if rate.BaseCurrencyCode != "USD" || rate.QuoteCurrencyCode != "VES" {
		t.Errorf("the rate is quoted 1 %s = x %s, want 1 USD = x VES",
			rate.BaseCurrencyCode, rate.QuoteCurrencyCode)
	}
	if rate.RateFormatted != "845" {
		t.Errorf("the execution rate is %q, want \"845\"", rate.RateFormatted)
	}

	if got := w.balance(t, w.mercantilUSD); got != 40000 {
		t.Errorf("Mercantil holds %d after the sale, want 40000", got)
	}
	if got := w.balance(t, w.banescoVES); got != 8450000 {
		t.Errorf("Banesco holds %d after the sale, want 8450000", got)
	}

	w.assertExchangesAreWellFormed(t)
}

// A rate with a non-trivial decimal expansion, and the acceptance case of
// docs/m5.5-contract.md.
func TestRecordExchangeDerivesANonTrivialRateExactly(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "84220.67",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	rate := view.Exchange.ExecutionRate
	if rate.RateFormatted != "842.2067" {
		t.Errorf("the execution rate is %q, want \"842.2067\"", rate.RateFormatted)
	}
	if !rate.Exact {
		t.Error("842.2067 is exact and was reported as approximate")
	}
}

// A rate with no finite decimal expansion is reported as approximate rather than
// presented as a fact.
func TestRecordExchangeReportsARateThatCannotBeWrittenExactly(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "100.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "3.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	rate := view.Exchange.ExecutionRate
	if rate.Exact {
		t.Error("a third of a hundred has no finite decimal expansion and was reported as exact")
	}
	if !strings.HasPrefix(rate.RateFormatted, "33.3333") {
		t.Errorf("the execution rate is %q, want it to start 33.3333", rate.RateFormatted)
	}
}

// A pair with no USD in it is recorded, valued and quoted identically. The only
// difference is which side the rule picks as the base: the one that makes the
// rate readable.
func TestRecordExchangeBetweenTwoNonDollarCurrencies(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.walletEUR,
		SourceAmount:         "50.00",
		DestinationAccountID: w.banescoVES,
		DestinationAmount:    "49500.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	rate := view.Exchange.ExecutionRate
	if rate.BaseCurrencyCode != "EUR" || rate.QuoteCurrencyCode != "VES" {
		t.Errorf("the rate is quoted 1 %s = x %s, want 1 EUR = x VES",
			rate.BaseCurrencyCode, rate.QuoteCurrencyCode)
	}
	if rate.RateFormatted != "990" {
		t.Errorf("the execution rate is %q, want \"990\"", rate.RateFormatted)
	}

	w.assertExchangesAreWellFormed(t)
}

// --- what an exchange refuses -----------------------------------------------

func TestRecordExchangeRefusesTwoAccountsInTheSameCurrency(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	transactionsBefore, movementsBefore := w.counts(t)

	_, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.efectivoVES,
		DestinationAmount:    "85000.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrSameCurrency) {
		t.Fatalf("recording an exchange between two VES accounts returned %v, want ErrSameCurrency", err)
	}

	transactionsAfter, movementsAfter := w.counts(t)
	if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
		t.Errorf("the refused exchange wrote rows: %d/%d transactions/movements, was %d/%d",
			transactionsAfter, movementsAfter, transactionsBefore, movementsBefore)
	}
}

func TestRecordExchangeRefusesTheSameAccountTwice(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	_, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.banescoVES,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrSameAccount) {
		t.Fatalf("recording an exchange into its own source returned %v, want ErrSameAccount", err)
	}
}

func TestRecordExchangeRefusesAmountsThatAreNotPositive(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		source      string
		destination string
	}{
		"zero source":          {"0.00", "100.00"},
		"zero destination":     {"85000.00", "0.00"},
		"both zero":            {"0.00", "0.00"},
		"negative source":      {"-85000.00", "100.00"},
		"negative destination": {"85000.00", "-100.00"},
		"unparseable source":   {"85.000,00", "100.00"},
		"too many decimals":    {"85000.001", "100.00"},
		"empty destination":    {"85000.00", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newExchangeWorld(t)
			transactionsBefore, movementsBefore := w.counts(t)

			_, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
				SourceAccountID:      w.banescoVES,
				SourceAmount:         tc.source,
				DestinationAccountID: w.mercantilUSD,
				DestinationAmount:    tc.destination,
				EffectiveDate:        today,
			})
			if !errors.Is(err, ledger.ErrInvalidAmount) {
				t.Fatalf("recording %s returned %v, want ErrInvalidAmount", name, err)
			}

			transactionsAfter, movementsAfter := w.counts(t)
			if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
				t.Errorf("the refused exchange wrote rows")
			}
		})
	}
}

// The two amounts are never compared with each other, so a "suspicious" one is
// recorded exactly as given. A user who really did trade 85,000 VES for 1 USD
// has recorded something true about a very bad day.
func TestRecordExchangeDoesNotCompareTheTwoMagnitudes(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	view, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "0.01",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "999999.99",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording a lopsided exchange: %v", err)
	}
	if view.Exchange.SourceAmountMinor != 1 || view.Exchange.DestinationAmountMinor != 99999999 {
		t.Errorf("the amounts were altered: %d and %d",
			view.Exchange.SourceAmountMinor, view.Exchange.DestinationAmountMinor)
	}
}

func TestRecordExchangeReportsWhichAccountWasRejected(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	archived, err := w.accounts.SetActive(t.Context(), w.mercantilUSD, false)
	if err != nil {
		t.Fatalf("archiving the destination account: %v", err)
	}
	if archived.IsActive {
		t.Fatal("the account is still active after being archived")
	}

	_, err = w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})

	var accountErr *ledger.AccountSideError
	if !errors.As(err, &accountErr) {
		t.Fatalf("an archived destination returned %v, want an AccountSideError", err)
	}
	if accountErr.Side != ledger.DestinationSide {
		t.Errorf("the error names the %s account, want the destination", accountErr.Side)
	}
	if !errors.Is(err, ledger.ErrAccountInactive) {
		t.Errorf("the error does not match ErrAccountInactive: %v", err)
	}

	// The M4 spelling is an alias of the same type, so it still matches.
	var legacy *ledger.TransferAccountError
	if !errors.As(err, &legacy) {
		t.Error("the error does not match the M4 spelling of the type")
	}

	// And a missing source is reported against the source.
	_, err = w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      99999,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.banescoVES,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if !errors.As(err, &accountErr) || accountErr.Side != ledger.SourceSide {
		t.Fatalf("a missing source returned %v, want a source-side AccountSideError", err)
	}
	if !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("the error does not match ErrAccountNotFound: %v", err)
	}
}

func TestRecordExchangeRefusesABadDateAndABlankDescription(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	in := ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        "2026-02-31",
	}
	if _, err := w.ledger.RecordExchange(t.Context(), in); !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("a date that does not exist returned %v, want ErrInvalidInput", err)
	}

	in.EffectiveDate = today
	in.Description = ptr("   ")
	if _, err := w.ledger.RecordExchange(t.Context(), in); !errors.Is(err, ledger.ErrInvalidInput) {
		t.Errorf("a blank description returned %v, want ErrInvalidInput", err)
	}

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("a refused exchange wrote %d transactions and %d movements", transactions, movements)
	}
}

// --- editing ----------------------------------------------------------------

func TestUpdateExchangeRewritesBothAmountsAndKeepsTheMovementIDs(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	w.fund(t, w.banescoVES, "100000.00")
	w.fund(t, w.mercantilUSD, "100.00")

	created, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "84220.67",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}
	before := w.movementsOf(t, created.ID)

	updated, err := w.ledger.UpdateExchange(t.Context(), created.ID, ledger.UpdateExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		Description:          ptr("Corregido"),
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("updating the exchange: %v", err)
	}

	after := w.movementsOf(t, created.ID)
	if len(after) != 2 {
		t.Fatalf("the exchange has %d movements after the edit, want 2", len(after))
	}
	if after[0].id != before[0].id || after[1].id != before[1].id {
		t.Errorf("the movement ids changed: %d,%d were %d,%d",
			after[0].id, after[1].id, before[0].id, before[1].id)
	}
	if after[0].amountMinor != -8500000 {
		t.Errorf("the source movement is %d, want -8500000", after[0].amountMinor)
	}
	if after[1].amountMinor != 10000 {
		t.Errorf("the destination movement is %d, want 10000", after[1].amountMinor)
	}

	// The derived rate followed the amounts, with nothing stored to keep in
	// step.
	if got := updated.Exchange.ExecutionRate.RateFormatted; got != "850" {
		t.Errorf("the execution rate is %q after the edit, want \"850\"", got)
	}

	// The acceptance balances.
	if got := w.balance(t, w.banescoVES); got != 1500000 {
		t.Errorf("Banesco holds %d after the edit, want 1500000", got)
	}
	if got := w.balance(t, w.mercantilUSD); got != 20000 {
		t.Errorf("Mercantil holds %d after the edit, want 20000", got)
	}

	w.assertExchangesAreWellFormed(t)
}

func TestUpdateExchangeCanSwapTheTwoAccounts(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	created, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	// A purchase entered as a sale, corrected. No confirmation flag is needed:
	// both currencies and both amounts are named explicitly, so nothing is being
	// silently reinterpreted at another scale.
	updated, err := w.ledger.UpdateExchange(t.Context(), created.ID, ledger.UpdateExchangeInput{
		SourceAccountID:      w.mercantilUSD,
		SourceAmount:         "100.00",
		DestinationAccountID: w.banescoVES,
		DestinationAmount:    "85000.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("swapping the two accounts: %v", err)
	}

	detail := updated.Exchange
	if detail.SourceAccountID != w.mercantilUSD || detail.DestinationAccountID != w.banescoVES {
		t.Errorf("the exchange runs %d -> %d, want %d -> %d",
			detail.SourceAccountID, detail.DestinationAccountID, w.mercantilUSD, w.banescoVES)
	}
	if got := w.balance(t, w.mercantilUSD); got != -10000 {
		t.Errorf("Mercantil holds %d after the swap, want -10000", got)
	}
	if got := w.balance(t, w.banescoVES); got != 8500000 {
		t.Errorf("Banesco holds %d after the swap, want 8500000", got)
	}

	w.assertExchangesAreWellFormed(t)
}

func TestUpdateExchangeRevalidatesEverythingACreationChecks(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	created, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}
	before := w.movementsOf(t, created.ID)

	cases := map[string]struct {
		in   ledger.UpdateExchangeInput
		want error
	}{
		"same currency": {
			ledger.UpdateExchangeInput{
				SourceAccountID: w.banescoVES, SourceAmount: "85000.00",
				DestinationAccountID: w.efectivoVES, DestinationAmount: "85000.00",
				EffectiveDate: today,
			},
			ledger.ErrSameCurrency,
		},
		"same account": {
			ledger.UpdateExchangeInput{
				SourceAccountID: w.banescoVES, SourceAmount: "85000.00",
				DestinationAccountID: w.banescoVES, DestinationAmount: "100.00",
				EffectiveDate: today,
			},
			ledger.ErrSameAccount,
		},
		"zero destination": {
			ledger.UpdateExchangeInput{
				SourceAccountID: w.banescoVES, SourceAmount: "85000.00",
				DestinationAccountID: w.mercantilUSD, DestinationAmount: "0.00",
				EffectiveDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"impossible date": {
			ledger.UpdateExchangeInput{
				SourceAccountID: w.banescoVES, SourceAmount: "85000.00",
				DestinationAccountID: w.mercantilUSD, DestinationAmount: "100.00",
				EffectiveDate: "2026-02-31",
			},
			ledger.ErrInvalidInput,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.ledger.UpdateExchange(t.Context(), created.ID, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("%s returned %v, want %v", name, err, tc.want)
			}
		})
	}

	// Nothing survived any of them.
	after := w.movementsOf(t, created.ID)
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("movement %d is %+v after the refused edits, was %+v", i, after[i], before[i])
		}
	}
}

// --- the type guards --------------------------------------------------------

// Each type has its own edit use case, and every guard names what it ACCEPTS
// rather than what it excludes (TD-045). This is the test that would have caught
// the M4 bug if FX_EXCHANGE had been added without reading it.
func TestExchangeCannotBeEditedThroughAnyOtherUseCase(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)

	exchange, err := w.ledger.RecordExchange(t.Context(), ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}
	before := w.movementsOf(t, exchange.ID)

	// The generic income/expense editor.
	if _, err := w.ledger.UpdateTransaction(t.Context(), exchange.ID, ledger.UpdateTransactionInput{
		AccountID: w.banescoVES, Type: ledger.Expense,
		CategoryID: w.food, PaymentMethodID: methodCash,
		Amount: "85000.00", EffectiveDate: today,
	}); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateTransaction on an exchange returned %v, want ErrTypeChangeNotAllowed", err)
	}

	// The transfer editor.
	if _, err := w.ledger.UpdateTransfer(t.Context(), exchange.ID, ledger.UpdateTransferInput{
		SourceAccountID: w.banescoVES, DestinationAccountID: w.efectivoVES,
		Amount: "85000.00", EffectiveDate: today,
	}); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateTransfer on an exchange returned %v, want ErrTypeChangeNotAllowed", err)
	}

	// The opening balance editor.
	if _, err := w.ledger.UpdateOpeningBalance(t.Context(), exchange.ID, ledger.UpdateOpeningBalanceInput{
		Amount: "85000.00", EffectiveDate: today,
	}); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateOpeningBalance on an exchange returned %v, want ErrTypeChangeNotAllowed", err)
	}

	after := w.movementsOf(t, exchange.ID)
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("movement %d is %+v after the refused edits, was %+v", i, after[i], before[i])
		}
	}
	w.assertExchangesAreWellFormed(t)
}

// And the reverse: nothing that is not an exchange may be edited as one.
func TestUpdateExchangeRefusesEveryOtherType(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	w.fund(t, w.mercantilUSD, "500.00")

	income, err := w.ledger.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: w.mercantilUSD, CategoryID: w.salary, Amount: "100.00",
		PaymentMethodID: methodBankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording the income: %v", err)
	}

	transfer, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID: w.banescoVES, DestinationAccountID: w.efectivoVES,
		Amount: "50.00", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	in := ledger.UpdateExchangeInput{
		SourceAccountID: w.banescoVES, SourceAmount: "85000.00",
		DestinationAccountID: w.mercantilUSD, DestinationAmount: "100.00",
		EffectiveDate: today,
	}

	for name, id := range map[string]int64{"income": income.ID, "transfer": transfer.ID} {
		if _, err := w.ledger.UpdateExchange(t.Context(), id, in); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
			t.Errorf("UpdateExchange on an %s returned %v, want ErrTypeChangeNotAllowed", name, err)
		}
	}

	// An opening balance too, which is a single-movement type and so would have
	// taken a different path through the guard.
	var openingID int64
	if err := w.db.QueryRowContext(t.Context(),
		"SELECT id FROM transactions WHERE transaction_type = 'OPENING_BALANCE'").Scan(&openingID); err != nil {
		t.Fatalf("finding the opening balance: %v", err)
	}
	if _, err := w.ledger.UpdateExchange(t.Context(), openingID, in); !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateExchange on an opening balance returned %v, want ErrTypeChangeNotAllowed", err)
	}
}

// --- the read model refuses to render broken data ---------------------------

// A malformed exchange is an integrity failure, not a validation failure: no
// input can produce one, so reaching it means something else wrote to the
// database. It is reported rather than rendered half-way.
func TestReadingAMalformedExchangeIsAnIntegrityFailure(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// movements holds (account, amount) pairs written directly, bypassing
		// every rule the service enforces.
		movements func(w *exchangeWorld) [][2]int64
	}{
		"missing destination": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, -8500000}}
			},
		},
		"missing source": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.mercantilUSD, 10000}}
			},
		},
		"three movements": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, -8500000}, {w.mercantilUSD, 10000}, {w.efectivoVES, 100}}
			},
		},
		"both negative": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, -8500000}, {w.mercantilUSD, -10000}}
			},
		},
		"both positive": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, 8500000}, {w.mercantilUSD, 10000}}
			},
		},
		"one account twice": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, -8500000}, {w.banescoVES, 10000}}
			},
		},
		"both in one currency": {
			func(w *exchangeWorld) [][2]int64 {
				return [][2]int64{{w.banescoVES, -8500000}, {w.efectivoVES, 8500000}}
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newExchangeWorld(t)
			id := w.writeRawExchange(t, tc.movements(w))

			if _, err := w.ledger.GetTransaction(t.Context(), id); !errors.Is(err, ledger.ErrInconsistentExchange) {
				t.Errorf("reading a %s exchange returned %v, want ErrInconsistentExchange", name, err)
			}

			// And it must not vanish from the listing either: a broken row that
			// is silently skipped is worse than one that complains (TD-044).
			if _, err := w.ledger.ListTransactions(t.Context(), ledger.ListTransactionsInput{}); !errors.Is(err, ledger.ErrInconsistentExchange) {
				t.Errorf("listing with a %s exchange returned %v, want ErrInconsistentExchange", name, err)
			}
		})
	}
}

// writeRawExchange writes an FX_EXCHANGE transaction and whatever movements it
// is given, in SQL, bypassing the service entirely. It is the only way to
// produce the states the read model has to refuse.
func (w *exchangeWorld) writeRawExchange(t *testing.T, movements [][2]int64) int64 {
	t.Helper()
	ctx := t.Context()

	result, err := w.db.ExecContext(ctx,
		`INSERT INTO transactions (transaction_type, effective_date, created_at, updated_at)
		 VALUES ('FX_EXCHANGE', ?, '2026-09-05T10:00:00.000Z', '2026-09-05T10:00:00.000Z')`, today)
	if err != nil {
		t.Fatalf("writing a raw exchange: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("reading the id of the raw exchange: %v", err)
	}

	for _, m := range movements {
		if _, err := w.db.ExecContext(ctx,
			`INSERT INTO account_movements (transaction_id, account_id, amount_minor, created_at)
			 VALUES (?, ?, ?, '2026-09-05T10:00:00.000Z')`, id, m[0], m[1]); err != nil {
			t.Fatalf("writing a raw movement: %v", err)
		}
	}
	return id
}

// --- the boundary with reference FX -----------------------------------------

// Recording and editing an exchange touches exchange_rates not at all.
//
// This is the rule the whole milestone rests on: exchange_rates holds what a
// SOURCE said a currency was worth, for valuing balances; an FX_EXCHANGE holds
// what the user actually traded at. Neither reads or writes the other, and the
// day one starts to, this test says so.
func TestRecordingAnExchangeNeverTouchesTheReferenceRates(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	ctx := t.Context()

	if _, err := w.db.ExecContext(ctx, `
		INSERT INTO exchange_rates
		    (base_currency_code, quote_currency_code, rate, effective_date, source, fetched_at,
		     is_active, created_at, updated_at)
		VALUES ('USD','VES','842.20670000','2026-09-15','BCV_OFFICIAL','2026-09-15T12:00:00.000Z',
		        1,'2026-09-15T12:00:00.000Z','2026-09-15T12:00:00.000Z')`); err != nil {
		t.Fatalf("seeding a reference rate: %v", err)
	}
	before := w.rateRows(t)

	created, err := w.ledger.RecordExchange(ctx, ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "84220.67",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}
	if after := w.rateRows(t); after != before {
		t.Errorf("recording an exchange changed exchange_rates: %q, was %q", after, before)
	}

	if _, err := w.ledger.UpdateExchange(ctx, created.ID, ledger.UpdateExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	}); err != nil {
		t.Fatalf("updating the exchange: %v", err)
	}
	if after := w.rateRows(t); after != before {
		t.Errorf("editing an exchange changed exchange_rates: %q, was %q", after, before)
	}
}

// rateRows renders every reference rate as one comparable string.
func (w *exchangeWorld) rateRows(t *testing.T) string {
	t.Helper()

	var rows string
	err := w.db.QueryRowContext(t.Context(), `
		SELECT COALESCE(group_concat(
		         base_currency_code || '/' || quote_currency_code || '@' || rate ||
		         '|' || effective_date || '|' || source || '|' || is_active || '|' || updated_at, ';'), '')
		  FROM (SELECT * FROM exchange_rates ORDER BY id)`).Scan(&rows)
	if err != nil {
		t.Fatalf("reading exchange_rates: %v", err)
	}
	return rows
}

// --- the acceptance scenario ------------------------------------------------

// The scenario in docs/m5.5-contract.md, end to end, in one test.
//
// It is written out rather than assembled from the helpers above so that it can
// be read against the contract line by line: the two starting balances, the
// exchange, the two resulting balances, the two stored movements, the derived
// rate, the edit, and the rates table that never moved.
func TestExchangeAcceptanceScenario(t *testing.T) {
	t.Parallel()

	w := newExchangeWorld(t)
	ctx := t.Context()

	// A reference rate exists throughout, so that "it did not change" is a
	// statement about something rather than about an empty table.
	if _, err := w.db.ExecContext(ctx, `
		INSERT INTO exchange_rates
		    (base_currency_code, quote_currency_code, rate, effective_date, source, fetched_at,
		     is_active, created_at, updated_at)
		VALUES ('USD','VES','842.20670000','2026-09-15','BCV_OFFICIAL','2026-09-15T12:00:00.000Z',
		        1,'2026-09-15T12:00:00.000Z','2026-09-15T12:00:00.000Z')`); err != nil {
		t.Fatalf("seeding the reference rate: %v", err)
	}
	ratesBefore := w.rateRows(t)

	// Banesco VES: 100,000.00 VES. Mercantil USD: 100.00 USD.
	w.fund(t, w.banescoVES, "100000.00")
	w.fund(t, w.mercantilUSD, "100.00")

	// 84,220.67 VES for 100.00 USD.
	created, err := w.ledger.RecordExchange(ctx, ledger.RecordExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "84220.67",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the exchange: %v", err)
	}

	// Banesco VES: 15,779.33 VES. Mercantil USD: 200.00 USD.
	if got := w.balance(t, w.banescoVES); got != 1577933 {
		t.Errorf("Banesco holds %d, want 1577933 (15,779.33 VES)", got)
	}
	if got := w.balance(t, w.mercantilUSD); got != 20000 {
		t.Errorf("Mercantil holds %d, want 20000 (200.00 USD)", got)
	}

	// The ledger: -8,422,067 VES minor units and +10,000 USD minor units.
	movements := w.movementsOf(t, created.ID)
	if len(movements) != 2 {
		t.Fatalf("the exchange has %d movements, want 2", len(movements))
	}
	if movements[0].amountMinor != -8422067 || movements[1].amountMinor != 10000 {
		t.Errorf("the movements are %d and %d, want -8422067 and 10000",
			movements[0].amountMinor, movements[1].amountMinor)
	}

	// The derived execution rate: 1 USD = 842.2067 VES.
	if got := created.Exchange.ExecutionRate; got.BaseCurrencyCode != "USD" ||
		got.QuoteCurrencyCode != "VES" || got.RateFormatted != "842.2067" {
		t.Errorf("the execution rate is 1 %s = %s %s, want 1 USD = 842.2067 VES",
			got.BaseCurrencyCode, got.RateFormatted, got.QuoteCurrencyCode)
	}

	// Then the delivered amount is corrected to 85,000.00 VES.
	updated, err := w.ledger.UpdateExchange(ctx, created.ID, ledger.UpdateExchangeInput{
		SourceAccountID:      w.banescoVES,
		SourceAmount:         "85000.00",
		DestinationAccountID: w.mercantilUSD,
		DestinationAmount:    "100.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("editing the exchange: %v", err)
	}

	// Banesco VES: 15,000.00 VES. Mercantil USD: 200.00 USD. Rate: 1 USD = 850 VES.
	if got := w.balance(t, w.banescoVES); got != 1500000 {
		t.Errorf("Banesco holds %d after the edit, want 1500000 (15,000.00 VES)", got)
	}
	if got := w.balance(t, w.mercantilUSD); got != 20000 {
		t.Errorf("Mercantil holds %d after the edit, want 20000 (200.00 USD)", got)
	}
	if got := updated.Exchange.ExecutionRate.RateFormatted; got != "850" {
		t.Errorf("the execution rate is %q after the edit, want \"850\"", got)
	}

	// And exchange_rates did not change during any of it.
	if ratesAfter := w.rateRows(t); ratesAfter != ratesBefore {
		t.Errorf("exchange_rates is %q after the scenario, was %q", ratesAfter, ratesBefore)
	}

	w.assertExchangesAreWellFormed(t)
	w.assertNoOrphanTransactions(t)
}
