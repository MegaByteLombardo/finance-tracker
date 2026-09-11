package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
)

// Transfers between two of the user's own accounts.
//
// The invariant every test here is ultimately about: a transfer moves money, it
// does not create or destroy any. The total held in a currency is the same
// before and after, always.

// --- helpers ----------------------------------------------------------------

// newTypedAccount opens an account of a given type. world.newAccount always
// makes a bank account; transfers must work between any pair of types, so these
// tests need the others too.
func (w *world) newTypedAccount(
	t *testing.T, name, currencyCode string, accountType account.Type, subtype *account.Subtype,
) int64 {
	t.Helper()

	institution := ptr("Mercantil")
	if accountType != account.Bank {
		// Cash held under a mattress is at no institution, and the schema's
		// CHECK does not care, but modelling it as one would be a lie in a
		// fixture that other assertions read.
		institution = nil
	}

	created, err := w.accounts.Create(t.Context(), account.CreateInput{
		Name: name, Type: accountType, Subtype: subtype,
		CurrencyCode: currencyCode, Institution: institution,
	})
	if err != nil {
		t.Fatalf("creating %s account %q: %v", accountType, name, err)
	}
	return created.ID
}

// currencyTotal is every minor unit held in one currency, across every account
// denominated in it.
//
// This is the figure a transfer must never change, and it is deliberately
// computed here in SQL rather than by calling ListCurrencyTotals: a test that
// asks the same code under test whether it is right proves less.
func (w *world) currencyTotal(t *testing.T, code string) int64 {
	t.Helper()

	var total int64
	err := w.db.QueryRowContext(t.Context(), `
		SELECT COALESCE(SUM(m.amount_minor), 0)
		  FROM account_movements m
		  JOIN accounts a ON a.id = m.account_id
		 WHERE a.currency_code = ?`, code).Scan(&total)
	if err != nil {
		t.Fatalf("totalling %s: %v", code, err)
	}
	return total
}

type storedMovement struct {
	id          int64
	accountID   int64
	amountMinor int64
}

// movementsOf returns a transaction's movements, lowest id first.
func (w *world) movementsOf(t *testing.T, transactionID int64) []storedMovement {
	t.Helper()

	rows, err := w.db.QueryContext(t.Context(),
		`SELECT id, account_id, amount_minor FROM account_movements
		  WHERE transaction_id = ? ORDER BY id`, transactionID)
	if err != nil {
		t.Fatalf("reading the movements of transaction %d: %v", transactionID, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing movement rows: %v", err)
		}
	}()

	var movements []storedMovement
	for rows.Next() {
		var m storedMovement
		if err := rows.Scan(&m.id, &m.accountID, &m.amountMinor); err != nil {
			t.Fatalf("scanning a movement: %v", err)
		}
		movements = append(movements, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating movements: %v", err)
	}
	return movements
}

// assertTransfersAreWellFormed checks, in SQL, that every stored transfer has
// the shape the service promises: two movements, summing to zero, in two
// different accounts of the same currency, with no category and no payment
// method.
//
// It is asserted against the database rather than against the value the service
// returned, because what matters is what was written.
func (w *world) assertTransfersAreWellFormed(t *testing.T) {
	t.Helper()
	ctx := t.Context()

	var broken int
	err := w.db.QueryRowContext(ctx, `
		SELECT count(*) FROM transactions t
		 WHERE t.transaction_type = 'TRANSFER'
		   AND (
		        (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id) <> 2
		     OR (SELECT COALESCE(SUM(m.amount_minor), 1) FROM account_movements m WHERE m.transaction_id = t.id) <> 0
		     OR (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id AND m.amount_minor < 0) <> 1
		     OR (SELECT count(*) FROM account_movements m WHERE m.transaction_id = t.id AND m.amount_minor > 0) <> 1
		     OR (SELECT count(DISTINCT m.account_id) FROM account_movements m WHERE m.transaction_id = t.id) <> 2
		     OR (SELECT count(DISTINCT a.currency_code)
		           FROM account_movements m JOIN accounts a ON a.id = m.account_id
		          WHERE m.transaction_id = t.id) <> 1
		     OR t.category_id IS NOT NULL
		     OR t.payment_method_id IS NOT NULL
		   )`).Scan(&broken)
	if err != nil {
		t.Fatalf("checking the shape of stored transfers: %v", err)
	}
	if broken != 0 {
		t.Errorf("%d stored transfer(s) are not two balanced movements in two same-currency accounts", broken)
	}
}

// transferWorld is a world with a second and third USD account, which is the
// minimum a transfer test needs.
type transferWorld struct {
	*world

	efectivoUSD int64 // CASH
	walletUSD   int64 // WALLET
	banescoVES  int64 // BANK, another currency
}

func newTransferWorld(t *testing.T) *transferWorld {
	t.Helper()

	w := newWorld(t)
	return &transferWorld{
		world:       w,
		efectivoUSD: w.newTypedAccount(t, "Efectivo USD", "USD", account.Cash, nil),
		walletUSD:   w.newTypedAccount(t, "Wallet USD", "USD", account.Wallet, nil),
		banescoVES:  w.newTypedAccount(t, "Banesco VES", "VES", account.Bank, checking()),
	}
}

// fund puts money into an account without going through a transfer.
func (w *transferWorld) fund(t *testing.T, accountID int64, amount string) {
	t.Helper()

	if _, err := w.ledger.RecordOpeningBalance(t.Context(), ledger.RecordOpeningBalanceInput{
		AccountID: accountID, Amount: amount, EffectiveDate: "2026-09-01",
	}); err != nil {
		t.Fatalf("funding account %d with %s: %v", accountID, amount, err)
	}
}

// --- recording --------------------------------------------------------------

func TestRecordTransferWritesOneEventAndTwoOppositeMovements(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "1000.00")
	w.fund(t, w.efectivoUSD, "100.00")

	view, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		Description:          ptr("Pasar dinero a efectivo"),
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	if view.Type != ledger.Transfer {
		t.Errorf("type = %q, want %q", view.Type, ledger.Transfer)
	}
	if view.CategoryID != nil || view.CategoryName != nil || view.CategoryType != nil {
		t.Errorf("the transfer has a category: %v/%v", view.CategoryID, view.CategoryName)
	}
	if view.PaymentMethodID != nil || view.PaymentMethodName != nil {
		t.Errorf("the transfer has a payment method: %v/%v", view.PaymentMethodID, view.PaymentMethodName)
	}
	if view.Description == nil || *view.Description != "Pasar dinero a efectivo" {
		t.Errorf("description = %v, want %q", view.Description, "Pasar dinero a efectivo")
	}
	if got := view.EffectiveDate.Format("2006-01-02"); got != "2026-09-06" {
		t.Errorf("effective date = %s, want 2026-09-06", got)
	}
	if view.CurrencyCode != "USD" {
		t.Errorf("currency = %s, want USD", view.CurrencyCode)
	}

	// The detail is the whole transfer, and it is what presentation reads.
	detail := view.Transfer
	if detail == nil {
		t.Fatal("the view carries no Transfer detail")
	}
	if detail.SourceAccountID != w.usdAccount || detail.SourceAccountName != "Mercantil USD" {
		t.Errorf("source = %d/%q, want %d/Mercantil USD",
			detail.SourceAccountID, detail.SourceAccountName, w.usdAccount)
	}
	if detail.DestinationAccountID != w.efectivoUSD || detail.DestinationAccountName != "Efectivo USD" {
		t.Errorf("destination = %d/%q, want %d/Efectivo USD",
			detail.DestinationAccountID, detail.DestinationAccountName, w.efectivoUSD)
	}
	if detail.AmountMinor != 25000 {
		t.Errorf("detail amount = %d, want 25000", detail.AmountMinor)
	}
	if detail.AmountFormatted != "250.00" {
		t.Errorf("detail amount formatted = %q, want \"250.00\"", detail.AmountFormatted)
	}

	// Exactly two movements, opposite and equal.
	movements := w.movementsOf(t, view.ID)
	if len(movements) != 2 {
		t.Fatalf("the transfer wrote %d movements, want exactly 2", len(movements))
	}
	source, destination := movements[0], movements[1]
	if source.accountID != w.usdAccount || source.amountMinor != -25000 {
		t.Errorf("source movement = account %d / %d, want %d / -25000",
			source.accountID, source.amountMinor, w.usdAccount)
	}
	if destination.accountID != w.efectivoUSD || destination.amountMinor != 25000 {
		t.Errorf("destination movement = account %d / %d, want %d / 25000",
			destination.accountID, destination.amountMinor, w.efectivoUSD)
	}
	if source.amountMinor+destination.amountMinor != 0 {
		t.Errorf("the two movements sum to %d, want 0", source.amountMinor+destination.amountMinor)
	}
	if detail.SourceMovementID != source.id || detail.DestinationMovementID != destination.id {
		t.Errorf("detail movement ids = %d/%d, want %d/%d",
			detail.SourceMovementID, detail.DestinationMovementID, source.id, destination.id)
	}

	// Both movements belong to the SAME event.
	var events int
	if err := w.db.QueryRowContext(ctx,
		`SELECT count(DISTINCT transaction_id) FROM account_movements WHERE transaction_id = ?`,
		view.ID).Scan(&events); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 1 {
		t.Errorf("the movements belong to %d events, want 1", events)
	}

	// The stored row really has no category and no payment method.
	var (
		categoryID    *int64
		paymentMethod *int64
	)
	if err := w.db.QueryRowContext(ctx,
		`SELECT category_id, payment_method_id FROM transactions WHERE id = ?`,
		view.ID).Scan(&categoryID, &paymentMethod); err != nil {
		t.Fatalf("reading the stored transfer: %v", err)
	}
	if categoryID != nil || paymentMethod != nil {
		t.Errorf("stored category/method = %v/%v, want NULL/NULL", categoryID, paymentMethod)
	}

	w.assertTransfersAreWellFormed(t)
	w.assertNoOrphanTransactions(t)
}

// account_type classifies where money sits and has no accounting meaning, so a
// transfer works between any two accounts that share a currency.
func TestTransfersWorkBetweenEveryPairOfAccountTypes(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	pairs := []struct {
		name              string
		source, dest      int64
		sourceKind, dest2 string
	}{
		{"bank to cash", w.usdAccount, w.efectivoUSD, "BANK", "CASH"},
		{"bank to wallet", w.usdAccount, w.walletUSD, "BANK", "WALLET"},
		{"cash to bank", w.efectivoUSD, w.usdAccount, "CASH", "BANK"},
		{"cash to wallet", w.efectivoUSD, w.walletUSD, "CASH", "WALLET"},
		{"wallet to cash", w.walletUSD, w.efectivoUSD, "WALLET", "CASH"},
		{"wallet to bank", w.walletUSD, w.usdAccount, "WALLET", "BANK"},
	}

	for _, pair := range pairs {
		view, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
			SourceAccountID:      pair.source,
			DestinationAccountID: pair.dest,
			Amount:               "10.00",
			EffectiveDate:        today,
		})
		if err != nil {
			t.Errorf("%s (%s to %s) was rejected: %v", pair.name, pair.sourceKind, pair.dest2, err)
			continue
		}
		if view.Transfer == nil {
			t.Errorf("%s produced no transfer detail", pair.name)
			continue
		}
		if view.Transfer.SourceAccountID != pair.source || view.Transfer.DestinationAccountID != pair.dest {
			t.Errorf("%s went %d->%d, want %d->%d", pair.name,
				view.Transfer.SourceAccountID, view.Transfer.DestinationAccountID, pair.source, pair.dest)
		}
	}

	w.assertTransfersAreWellFormed(t)
}

func TestTransferAmountIsReadExactly(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	amounts := map[string]int64{
		"0.01":      1,
		"1":         100,
		"1.5":       150,
		"250.00":    25000,
		"1000.50":   100050,
		"999999.99": 99999999,
	}

	for input, want := range amounts {
		t.Run(input, func(t *testing.T) {
			view, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
				SourceAccountID:      w.usdAccount,
				DestinationAccountID: w.efectivoUSD,
				Amount:               input,
				EffectiveDate:        today,
			})
			if err != nil {
				t.Fatalf("recording %s: %v", input, err)
			}
			if view.Transfer.AmountMinor != want {
				t.Errorf("%q became %d minor units, want %d", input, view.Transfer.AmountMinor, want)
			}
			movements := w.movementsOf(t, view.ID)
			if movements[0].amountMinor != -want || movements[1].amountMinor != want {
				t.Errorf("movements = %d/%d, want %d/%d",
					movements[0].amountMinor, movements[1].amountMinor, -want, want)
			}
		})
	}
}

func TestRecordTransferRejectsTheSameAccountOnBothEnds(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	transactionsBefore, movementsBefore := w.counts(t)

	_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.usdAccount,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrSameAccount) {
		t.Fatalf("error = %v, want ErrSameAccount", err)
	}

	transactionsAfter, movementsAfter := w.counts(t)
	if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
		t.Errorf("a rejected transfer wrote %d transactions and %d movements",
			transactionsAfter-transactionsBefore, movementsAfter-movementsBefore)
	}
}

func TestRecordTransferRejectsDifferentCurrencies(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	transactionsBefore, movementsBefore := w.counts(t)

	_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.banescoVES,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrCurrencyMismatch) {
		t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
	}
	// The message must say which currencies, because that is the whole content
	// of the failure for whoever reads it.
	if !strings.Contains(err.Error(), "USD") || !strings.Contains(err.Error(), "VES") {
		t.Errorf("the error does not name both currencies: %v", err)
	}

	transactionsAfter, movementsAfter := w.counts(t)
	if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
		t.Error("a cross-currency transfer wrote something")
	}
}

func TestRecordTransferRejectsUnknownAndInactiveAccountsAndSaysWhichSide(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	archived := w.newTypedAccount(t, "Archivada USD", "USD", account.Cash, nil)
	if _, err := w.accounts.SetActive(ctx, archived, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	tests := map[string]struct {
		source, destination int64
		want                error
		wantSide            ledger.TransferSide
	}{
		"unknown source":       {9999, w.efectivoUSD, ledger.ErrAccountNotFound, ledger.SourceSide},
		"unknown destination":  {w.usdAccount, 9999, ledger.ErrAccountNotFound, ledger.DestinationSide},
		"archived source":      {archived, w.efectivoUSD, ledger.ErrAccountInactive, ledger.SourceSide},
		"archived destination": {w.usdAccount, archived, ledger.ErrAccountInactive, ledger.DestinationSide},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			transactionsBefore, movementsBefore := w.counts(t)

			_, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
				SourceAccountID:      tc.source,
				DestinationAccountID: tc.destination,
				Amount:               "250.00",
				EffectiveDate:        today,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}

			// errors.As recovers which field a form should mark, without
			// splitting the two conditions into four sentinels.
			var accountErr *ledger.TransferAccountError
			if !errors.As(err, &accountErr) {
				t.Fatalf("error = %v, want a *TransferAccountError", err)
			}
			if accountErr.Side != tc.wantSide {
				t.Errorf("side = %q, want %q", accountErr.Side, tc.wantSide)
			}

			transactionsAfter, movementsAfter := w.counts(t)
			if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
				t.Error("a rejected transfer wrote something")
			}
		})
	}
}

func TestRecordTransferRejectsInvalidAmounts(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	// A transfer of nothing is not "record nothing" the way a zero opening
	// balance is: the user named two accounts and meant to move money between
	// them. A negative amount is not a way to reverse the direction either --
	// the direction is which account was named first.
	amounts := []string{"", "   ", "0", "0.00", "-250.00", "-0.01", "abc", "250.999", "1,5", "1e3", "+250"}

	for _, amount := range amounts {
		t.Run(amount, func(t *testing.T) {
			transactionsBefore, movementsBefore := w.counts(t)

			_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
				SourceAccountID:      w.usdAccount,
				DestinationAccountID: w.efectivoUSD,
				Amount:               amount,
				EffectiveDate:        today,
			})
			if !errors.Is(err, ledger.ErrInvalidAmount) {
				t.Fatalf("%q gave %v, want ErrInvalidAmount", amount, err)
			}

			transactionsAfter, movementsAfter := w.counts(t)
			if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
				t.Error("a rejected transfer wrote something")
			}
		})
	}
}

// An amount with more decimals than the currency has is refused, never rounded:
// rounding would discard money the user typed under a rule they never agreed to.
func TestOverPreciseTransferAmountIsRejectedRatherThanRounded(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "12.345",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Fatalf("error = %v, want ErrInvalidAmount", err)
	}
	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("12.345 was rounded and stored: %d transactions, %d movements", transactions, movements)
	}
}

func TestRecordTransferRejectsInvalidDatesAndDescriptions(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	t.Run("dates", func(t *testing.T) {
		for _, date := range []string{"", "   ", "2026-9-5", "2026-02-31", "2026-13-01", "05/09/2026",
			"2026-09-05T00:00:00Z", "yesterday"} {
			_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
				SourceAccountID:      w.usdAccount,
				DestinationAccountID: w.efectivoUSD,
				Amount:               "250.00",
				EffectiveDate:        date,
			})
			if !errors.Is(err, ledger.ErrInvalidInput) {
				t.Errorf("date %q gave %v, want ErrInvalidInput", date, err)
			}
		}
	})

	t.Run("blank description", func(t *testing.T) {
		// Absence is nil. An empty string means the caller believed they were
		// sending a description, and swallowing it would hide their bug.
		for _, description := range []string{"", "   "} {
			_, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
				SourceAccountID:      w.usdAccount,
				DestinationAccountID: w.efectivoUSD,
				Amount:               "250.00",
				Description:          ptr(description),
				EffectiveDate:        today,
			})
			if !errors.Is(err, ledger.ErrInvalidInput) {
				t.Errorf("description %q gave %v, want ErrInvalidInput", description, err)
			}
		}
	})

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("rejected transfers wrote %d transactions and %d movements", transactions, movements)
	}
}

func TestTransferDescriptionIsOptionalAndTrimmed(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	none, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "10.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording without a description: %v", err)
	}
	if none.Description != nil {
		t.Errorf("description = %v, want nil", none.Description)
	}

	trimmed, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "10.00",
		Description:          ptr("  Reserva  "),
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording with a padded description: %v", err)
	}
	if trimmed.Description == nil || *trimmed.Description != "Reserva" {
		t.Errorf("description = %v, want %q", trimmed.Description, "Reserva")
	}
}

// Retiring a currency stops it denominating new accounts. It does not freeze the
// accounts already using it, and money keeps moving between them.
func TestAnArchivedCurrencyDoesNotBlockATransfer(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	if _, err := w.currencies.SetActive(ctx, "USD", false); err != nil {
		t.Fatalf("archiving the currency: %v", err)
	}

	if _, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	}); err != nil {
		t.Errorf("archiving a currency froze transfers between existing accounts: %v", err)
	}
}

func TestRecordTransferRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	}); err == nil {
		t.Error("a transfer on a cancelled context succeeded")
	}

	if transactions, movements := w.counts(t); transactions != 0 || movements != 0 {
		t.Errorf("a cancelled transfer left %d transactions and %d movements", transactions, movements)
	}
}

// --- balances ---------------------------------------------------------------

func TestTransferMovesTheBalanceAndLeavesTheCurrencyTotalAlone(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "1000.00")
	w.fund(t, w.efectivoUSD, "100.00")
	w.fund(t, w.walletUSD, "50.00")
	w.fund(t, w.banescoVES, "700.00")

	totalUSDBefore := w.currencyTotal(t, "USD")
	totalVESBefore := w.currencyTotal(t, "VES")
	walletBefore := w.balance(t, w.walletUSD)

	if _, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	}); err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	if got := w.balance(t, w.usdAccount); got != 75000 {
		t.Errorf("source balance = %d, want 75000", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != 35000 {
		t.Errorf("destination balance = %d, want 35000", got)
	}

	// The uninvolved account of the same currency is untouched...
	if got := w.balance(t, w.walletUSD); got != walletBefore {
		t.Errorf("an uninvolved account moved: %d, was %d", got, walletBefore)
	}
	// ...and so is the other currency.
	if got := w.currencyTotal(t, "VES"); got != totalVESBefore {
		t.Errorf("the VES total moved: %d, was %d", got, totalVESBefore)
	}

	// The invariant.
	if got := w.currencyTotal(t, "USD"); got != totalUSDBefore {
		t.Errorf("the USD total is %d after the transfer, was %d; a transfer must not create or destroy money",
			got, totalUSDBefore)
	}

	// And the service agrees with the raw sum.
	totals, err := w.ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		t.Fatalf("listing currency totals: %v", err)
	}
	for _, total := range totals {
		if total.CurrencyCode != "USD" {
			continue
		}
		if total.TotalMinor != totalUSDBefore {
			t.Errorf("ListCurrencyTotals reports %d for USD, want %d", total.TotalMinor, totalUSDBefore)
		}
	}
}

// A transfer may leave an account overdrawn, and this is deliberate.
//
// The ledger records what happened. Bank statements show overdrafts; a cash
// account that goes negative is usually telling the user that a movement is
// missing rather than that they broke a rule. Refusing it would also be a rule
// that only sometimes applied, since a backdated transfer can make a past
// balance negative whatever is checked at the moment of writing.
func TestATransferMayLeaveAnAccountNegative(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "100.00")
	totalBefore := w.currencyTotal(t, "USD")

	if _, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "150.00",
		EffectiveDate:        today,
	}); err != nil {
		t.Fatalf("a transfer larger than the balance was refused: %v", err)
	}

	if got := w.balance(t, w.usdAccount); got != -5000 {
		t.Errorf("source balance = %d, want -5000", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != 15000 {
		t.Errorf("destination balance = %d, want 15000", got)
	}
	if got := w.currencyTotal(t, "USD"); got != totalBefore {
		t.Errorf("the USD total moved to %d, was %d", got, totalBefore)
	}
}

// --- read models ------------------------------------------------------------

func TestATransferIsOneRowInTheListing(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "1000.00")
	if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "500.00",
		PaymentMethodID: methodBankTransfer, EffectiveDate: "2026-09-02",
	}); err != nil {
		t.Fatalf("recording the income: %v", err)
	}
	transfer, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        "2026-09-03",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	views, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}

	// One opening balance, one income, one transfer: three rows, not four.
	if len(views) != 3 {
		t.Fatalf("the listing has %d rows, want 3 (a transfer is one event, not two)", len(views))
	}

	var seen int
	for _, v := range views {
		if v.ID != transfer.ID {
			if v.Transfer != nil {
				t.Errorf("transaction %d is a %s but carries a Transfer detail", v.ID, v.Type)
			}
			continue
		}
		seen++
		if v.Transfer == nil {
			t.Fatal("the listed transfer carries no detail")
		}
		if v.Transfer.SourceAccountName != "Mercantil USD" ||
			v.Transfer.DestinationAccountName != "Efectivo USD" {
			t.Errorf("listed transfer = %q -> %q, want Mercantil USD -> Efectivo USD",
				v.Transfer.SourceAccountName, v.Transfer.DestinationAccountName)
		}
		if v.Transfer.AmountFormatted != "250.00" {
			t.Errorf("listed amount = %q, want \"250.00\"", v.Transfer.AmountFormatted)
		}
		if v.CategoryName != nil || v.PaymentMethodName != nil {
			t.Error("the listed transfer pretends to have a category or a payment method")
		}
	}
	if seen != 1 {
		t.Errorf("the transfer appears %d times in the listing, want exactly 1", seen)
	}
}

func TestGetTransactionReturnsBothHalvesOfATransfer(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.efectivoUSD,
		DestinationAccountID: w.usdAccount,
		Amount:               "75.25",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	got, err := w.ledger.GetTransaction(ctx, created.ID)
	if err != nil {
		t.Fatalf("getting the transfer: %v", err)
	}
	if got.Transfer == nil {
		t.Fatal("GetTransaction returned a transfer with no detail")
	}
	if *got.Transfer != *created.Transfer {
		t.Errorf("GetTransaction detail = %+v, want %+v", *got.Transfer, *created.Transfer)
	}
	if got.Transfer.SourceAccountID != w.efectivoUSD {
		t.Errorf("source = %d, want %d", got.Transfer.SourceAccountID, w.efectivoUSD)
	}
	if got.Transfer.AmountMinor != 7525 {
		t.Errorf("amount = %d, want 7525", got.Transfer.AmountMinor)
	}
}

// The listing still shows income, expenses and opening balances exactly as it
// did, and none of them acquire a Transfer detail.
func TestTheListingIsUnchangedForEveryOtherType(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "1000.00")
	if _, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "500.00",
		PaymentMethodID: methodBankTransfer, Description: ptr("Sueldo"), EffectiveDate: "2026-09-02",
	}); err != nil {
		t.Fatalf("recording the income: %v", err)
	}
	if _, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.99",
		PaymentMethodID: methodPOS, EffectiveDate: "2026-09-03",
	}); err != nil {
		t.Fatalf("recording the expense: %v", err)
	}

	views, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("the listing has %d rows, want 3", len(views))
	}

	for _, v := range views {
		if v.Transfer != nil {
			t.Errorf("a %s carries a Transfer detail", v.Type)
		}
		switch v.Type {
		case ledger.Income, ledger.Expense:
			if v.CategoryID == nil || v.PaymentMethodID == nil {
				t.Errorf("a %s lost its category or payment method", v.Type)
			}
		case ledger.OpeningBalance:
			if v.CategoryID != nil || v.PaymentMethodID != nil {
				t.Errorf("an opening balance gained a category or payment method")
			}
		}
		if v.MovementID == 0 || v.AccountName == "" || v.AmountFormatted == "" {
			t.Errorf("transaction %d has an incomplete row: %+v", v.ID, v)
		}
	}
}

// --- editing ----------------------------------------------------------------

func TestUpdateTransferCorrectsEveryEditableField(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	w.fund(t, w.usdAccount, "1000.00")
	w.fund(t, w.efectivoUSD, "100.00")

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		Description:          ptr("Pasar dinero a efectivo"),
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	before := w.movementsOf(t, created.ID)
	totalBefore := w.currencyTotal(t, "USD")

	updated, err := w.ledger.UpdateTransfer(ctx, created.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.efectivoUSD, // swapped
		DestinationAccountID: w.walletUSD,   // and moved
		Amount:               "300.00",
		Description:          ptr("Corregido"),
		EffectiveDate:        "2026-09-07",
	})
	if err != nil {
		t.Fatalf("updating the transfer: %v", err)
	}

	if updated.Transfer == nil {
		t.Fatal("the updated transfer carries no detail")
	}
	if updated.Transfer.SourceAccountID != w.efectivoUSD {
		t.Errorf("source = %d, want %d", updated.Transfer.SourceAccountID, w.efectivoUSD)
	}
	if updated.Transfer.DestinationAccountID != w.walletUSD {
		t.Errorf("destination = %d, want %d", updated.Transfer.DestinationAccountID, w.walletUSD)
	}
	if updated.Transfer.AmountMinor != 30000 {
		t.Errorf("amount = %d, want 30000", updated.Transfer.AmountMinor)
	}
	if updated.Description == nil || *updated.Description != "Corregido" {
		t.Errorf("description = %v, want %q", updated.Description, "Corregido")
	}
	if got := updated.EffectiveDate.Format("2006-01-02"); got != "2026-09-07" {
		t.Errorf("effective date = %s, want 2026-09-07", got)
	}
	if updated.Type != ledger.Transfer {
		t.Errorf("type = %q, want %q", updated.Type, ledger.Transfer)
	}
	if updated.CategoryID != nil || updated.PaymentMethodID != nil {
		t.Error("the edit gave the transfer a category or a payment method")
	}

	// The two movements are the SAME ROWS, still one negative and one positive.
	after := w.movementsOf(t, created.ID)
	if len(after) != 2 {
		t.Fatalf("the edited transfer has %d movements, want 2", len(after))
	}
	if after[0].id != before[0].id || after[1].id != before[1].id {
		t.Errorf("movement ids changed: %d/%d, were %d/%d",
			after[0].id, after[1].id, before[0].id, before[1].id)
	}
	if after[0].amountMinor != -30000 || after[1].amountMinor != 30000 {
		t.Errorf("movements = %d/%d, want -30000/30000", after[0].amountMinor, after[1].amountMinor)
	}
	if after[0].accountID != w.efectivoUSD || after[1].accountID != w.walletUSD {
		t.Errorf("movement accounts = %d/%d, want %d/%d",
			after[0].accountID, after[1].accountID, w.efectivoUSD, w.walletUSD)
	}

	// Balances follow immediately, and the total is still the total.
	if got := w.balance(t, w.usdAccount); got != 100000 {
		t.Errorf("the original source is %d, want 100000 -- the edit did not undo its movement", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != -20000 {
		t.Errorf("efectivo = %d, want -20000", got)
	}
	if got := w.balance(t, w.walletUSD); got != 30000 {
		t.Errorf("wallet = %d, want 30000", got)
	}
	if got := w.currencyTotal(t, "USD"); got != totalBefore {
		t.Errorf("the USD total is %d after the edit, was %d", got, totalBefore)
	}

	w.assertTransfersAreWellFormed(t)
}

func TestUpdateTransferRevalidatesEverythingACreationChecks(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	archived := w.newTypedAccount(t, "Archivada USD", "USD", account.Cash, nil)
	if _, err := w.accounts.SetActive(ctx, archived, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	before := w.movementsOf(t, created.ID)

	tests := map[string]struct {
		in   ledger.UpdateTransferInput
		want error
	}{
		"same account": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.usdAccount,
				Amount: "250.00", EffectiveDate: today,
			},
			ledger.ErrSameAccount,
		},
		"different currencies": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.banescoVES,
				Amount: "250.00", EffectiveDate: today,
			},
			ledger.ErrCurrencyMismatch,
		},
		"unknown source": {
			ledger.UpdateTransferInput{
				SourceAccountID: 9999, DestinationAccountID: w.efectivoUSD,
				Amount: "250.00", EffectiveDate: today,
			},
			ledger.ErrAccountNotFound,
		},
		"archived source": {
			ledger.UpdateTransferInput{
				SourceAccountID: archived, DestinationAccountID: w.efectivoUSD,
				Amount: "250.00", EffectiveDate: today,
			},
			ledger.ErrAccountInactive,
		},
		"archived destination": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: archived,
				Amount: "250.00", EffectiveDate: today,
			},
			ledger.ErrAccountInactive,
		},
		"zero amount": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
				Amount: "0", EffectiveDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"negative amount": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
				Amount: "-250.00", EffectiveDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"over-precise amount": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
				Amount: "250.999", EffectiveDate: today,
			},
			ledger.ErrInvalidAmount,
		},
		"impossible date": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
				Amount: "250.00", EffectiveDate: "2026-02-31",
			},
			ledger.ErrInvalidInput,
		},
		"blank description": {
			ledger.UpdateTransferInput{
				SourceAccountID: w.usdAccount, DestinationAccountID: w.efectivoUSD,
				Amount: "250.00", Description: ptr(""), EffectiveDate: today,
			},
			ledger.ErrInvalidInput,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := w.ledger.UpdateTransfer(ctx, created.ID, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}

			// A refused edit must change nothing at all.
			after := w.movementsOf(t, created.ID)
			if len(after) != len(before) {
				t.Fatalf("the refused edit left %d movements, want %d", len(after), len(before))
			}
			for i := range before {
				if after[i] != before[i] {
					t.Errorf("movement %d = %+v after a refused edit, was %+v", i, after[i], before[i])
				}
			}
		})
	}
}

// Moving a transfer to a pair of accounts in another currency reinterprets its
// amount rather than converting it, so it must be asked for -- the same rule
// UpdateTransaction applies to a single movement.
func TestUpdateTransferRequiresConfirmationToChangeCurrency(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	otherVES := w.newTypedAccount(t, "Efectivo VES", "VES", account.Cash, nil)

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	before := w.movementsOf(t, created.ID)

	_, err = w.ledger.UpdateTransfer(ctx, created.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.banescoVES,
		DestinationAccountID: otherVES,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrCurrencyChangeNotConfirmed) {
		t.Fatalf("error = %v, want ErrCurrencyChangeNotConfirmed", err)
	}
	after := w.movementsOf(t, created.ID)
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("the unconfirmed edit wrote: movement %+v, was %+v", after[i], before[i])
		}
	}

	// With the flag it goes through, and the amount is read at the new scale.
	updated, err := w.ledger.UpdateTransfer(ctx, created.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.banescoVES,
		DestinationAccountID: otherVES,
		Amount:               "250.00",
		EffectiveDate:        today,
		AllowCurrencyChange:  true,
	})
	if err != nil {
		t.Fatalf("the confirmed edit was refused: %v", err)
	}
	if updated.CurrencyCode != "VES" {
		t.Errorf("currency = %s, want VES", updated.CurrencyCode)
	}
	if got := w.currencyTotal(t, "USD"); got != 0 {
		t.Errorf("USD still holds %d after the transfer left it entirely", got)
	}
	if got := w.currencyTotal(t, "VES"); got != 0 {
		t.Errorf("the VES total is %d, want 0 -- a transfer nets to nothing", got)
	}
}

// Staying within one currency needs no confirmation.
func TestUpdateTransferNeedsNoConfirmationWithinACurrency(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	if _, err := w.ledger.UpdateTransfer(ctx, created.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.walletUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	}); err != nil {
		t.Errorf("moving a transfer within one currency asked for confirmation: %v", err)
	}
}

func TestUpdateTransferRejectsUnknownTransactions(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	_, err := w.ledger.UpdateTransfer(t.Context(), 9999, ledger.UpdateTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestUpdateTransferRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)

	created, err := w.ledger.RecordTransfer(t.Context(), ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	before := w.movementsOf(t, created.ID)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := w.ledger.UpdateTransfer(ctx, created.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.efectivoUSD,
		DestinationAccountID: w.usdAccount,
		Amount:               "999.00",
		EffectiveDate:        today,
	}); err == nil {
		t.Error("an edit on a cancelled context succeeded")
	}

	after := w.movementsOf(t, created.ID)
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("a cancelled edit wrote: movement %+v, was %+v", after[i], before[i])
		}
	}
}

// --- the boundary between use cases -----------------------------------------

// A transfer must never reach the income-and-expense editor. That editor
// rewrites ONE movement, so letting a transfer through would leave a debit with
// no matching credit: money destroyed, silently.
func TestUpdateTransactionRefusesATransferAndTouchesNothing(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}
	before := w.movementsOf(t, created.ID)
	totalBefore := w.currencyTotal(t, "USD")

	_, err = w.ledger.UpdateTransaction(ctx, created.ID, ledger.UpdateTransactionInput{
		AccountID:       w.usdAccount,
		Type:            ledger.Income,
		CategoryID:      w.salary,
		PaymentMethodID: methodBankTransfer,
		Amount:          "250.00",
		EffectiveDate:   today,
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Fatalf("error = %v, want ErrTypeChangeNotAllowed", err)
	}

	after := w.movementsOf(t, created.ID)
	if len(after) != 2 {
		t.Fatalf("the transfer now has %d movements, want 2", len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("movement %+v changed, was %+v", after[i], before[i])
		}
	}
	if got := w.currencyTotal(t, "USD"); got != totalBefore {
		t.Errorf("the USD total is %d, was %d", got, totalBefore)
	}

	stored, err := w.ledger.GetTransaction(ctx, created.ID)
	if err != nil {
		t.Fatalf("re-reading the transfer: %v", err)
	}
	if stored.Type != ledger.Transfer {
		t.Errorf("the transfer became %q", stored.Type)
	}
	w.assertTransfersAreWellFormed(t)
}

// The generic editor must not accept TRANSFER as a target type either.
func TestUpdateTransactionRefusesToConvertIncomeIntoATransfer(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	income, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "500.00",
		PaymentMethodID: methodBankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording the income: %v", err)
	}

	_, err = w.ledger.UpdateTransaction(ctx, income.ID, ledger.UpdateTransactionInput{
		AccountID:       w.usdAccount,
		Type:            ledger.Transfer,
		CategoryID:      w.salary,
		PaymentMethodID: methodBankTransfer,
		Amount:          "500.00",
		EffectiveDate:   today,
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Fatalf("error = %v, want ErrTypeChangeNotAllowed", err)
	}

	stored, err := w.ledger.GetTransaction(ctx, income.ID)
	if err != nil {
		t.Fatalf("re-reading the income: %v", err)
	}
	if stored.Type != ledger.Income {
		t.Errorf("the income became %q", stored.Type)
	}
	if len(w.movementsOf(t, income.ID)) != 1 {
		t.Error("the income gained a second movement")
	}
}

func TestUpdateOpeningBalanceRefusesATransfer(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	created, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		EffectiveDate:        today,
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	_, err = w.ledger.UpdateOpeningBalance(ctx, created.ID, ledger.UpdateOpeningBalanceInput{
		Amount: "999.00", EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("error = %v, want ErrTypeChangeNotAllowed", err)
	}
	w.assertTransfersAreWellFormed(t)
}

// And the reverse: the transfer editor must refuse everything that is not one.
func TestUpdateTransferRefusesEveryOtherType(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	income, err := w.ledger.RecordIncome(ctx, ledger.RecordIncomeInput{
		AccountID: w.usdAccount, CategoryID: w.salary, Amount: "500.00",
		PaymentMethodID: methodBankTransfer, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording the income: %v", err)
	}
	expense, err := w.ledger.RecordExpense(ctx, ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.00",
		PaymentMethodID: methodPOS, EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("recording the expense: %v", err)
	}
	opening, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.walletUSD, Amount: "50.00", EffectiveDate: "2026-09-01",
	})
	if err != nil {
		t.Fatalf("recording the opening balance: %v", err)
	}

	for name, id := range map[string]int64{
		"income":          income.ID,
		"expense":         expense.ID,
		"opening balance": opening.ID,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := w.ledger.UpdateTransfer(ctx, id, ledger.UpdateTransferInput{
				SourceAccountID:      w.usdAccount,
				DestinationAccountID: w.efectivoUSD,
				Amount:               "250.00",
				EffectiveDate:        today,
			})
			if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
				t.Fatalf("error = %v, want ErrTypeChangeNotAllowed", err)
			}
			if len(w.movementsOf(t, id)) != 1 {
				t.Errorf("the %s gained a movement", name)
			}
		})
	}
}

// --- end to end -------------------------------------------------------------

// The scenario the milestone is specified by, start to finish.
func TestTransferEndToEndPreservesTheCurrencyTotal(t *testing.T) {
	t.Parallel()

	w := newTransferWorld(t)
	ctx := t.Context()

	// Mercantil USD 1000.00, Efectivo USD 100.00 -> TOTAL USD 1100.00
	w.fund(t, w.usdAccount, "1000.00")
	w.fund(t, w.efectivoUSD, "100.00")

	const wantTotal int64 = 110000
	if got := w.currencyTotal(t, "USD"); got != wantTotal {
		t.Fatalf("the opening USD total is %d, want %d", got, wantTotal)
	}

	// Transfer 250.00 Mercantil -> Efectivo.
	transfer, err := w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "250.00",
		Description:          ptr("Pasar dinero a efectivo"),
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	if got := w.balance(t, w.usdAccount); got != 75000 {
		t.Errorf("Mercantil = %d, want 75000 (750.00)", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != 35000 {
		t.Errorf("Efectivo = %d, want 35000 (350.00)", got)
	}
	if got := w.currencyTotal(t, "USD"); got != wantTotal {
		t.Errorf("the USD total is %d after the transfer, want %d", got, wantTotal)
	}

	// Edit it: 250.00 -> 300.00.
	if _, err := w.ledger.UpdateTransfer(ctx, transfer.ID, ledger.UpdateTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.efectivoUSD,
		Amount:               "300.00",
		Description:          ptr("Pasar dinero a efectivo"),
		EffectiveDate:        "2026-09-06",
	}); err != nil {
		t.Fatalf("editing the transfer: %v", err)
	}

	if got := w.balance(t, w.usdAccount); got != 70000 {
		t.Errorf("Mercantil = %d, want 70000 (700.00)", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != 40000 {
		t.Errorf("Efectivo = %d, want 40000 (400.00)", got)
	}
	if got := w.currencyTotal(t, "USD"); got != wantTotal {
		t.Errorf("the USD total is %d after the edit, want %d", got, wantTotal)
	}

	// A cross-currency transfer is refused cleanly, and changes nothing.
	transactionsBefore, movementsBefore := w.counts(t)
	_, err = w.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID:      w.usdAccount,
		DestinationAccountID: w.banescoVES,
		Amount:               "100.00",
		EffectiveDate:        "2026-09-06",
	})
	if !errors.Is(err, ledger.ErrCurrencyMismatch) {
		t.Fatalf("a USD -> VES transfer gave %v, want ErrCurrencyMismatch", err)
	}
	transactionsAfter, movementsAfter := w.counts(t)
	if transactionsAfter != transactionsBefore || movementsAfter != movementsBefore {
		t.Error("the refused cross-currency transfer wrote something")
	}
	if got := w.currencyTotal(t, "USD"); got != wantTotal {
		t.Errorf("the USD total is %d, want %d", got, wantTotal)
	}
	if got := w.balance(t, w.usdAccount); got != 70000 {
		t.Errorf("Mercantil = %d, want 70000", got)
	}
	if got := w.balance(t, w.efectivoUSD); got != 40000 {
		t.Errorf("Efectivo = %d, want 40000", got)
	}

	w.assertTransfersAreWellFormed(t)
	w.assertNoOrphanTransactions(t)
}
