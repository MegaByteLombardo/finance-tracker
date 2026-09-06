package ledger_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
)

// balance reads what the account is worth right now, from the movements.
func (w *world) balance(t *testing.T, accountID int64) int64 {
	t.Helper()

	b, err := w.ledger.GetAccountBalance(t.Context(), accountID)
	if err != nil {
		t.Fatalf("GetAccountBalance(%d): %v", accountID, err)
	}
	return b.BalanceMinor
}

// anExpense records one expense and returns it.
func (w *world) anExpense(t *testing.T) ledger.TransactionView {
	t.Helper()

	view, err := w.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: w.usdAccount, CategoryID: w.food, Amount: "25.99",
		PaymentMethodID: methodPOS, Description: ptr("Almuerzo"), EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordExpense: %v", err)
	}
	return view
}

func TestUpdateTransactionCorrectsEveryEditableField(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	original := w.anExpense(t)
	other := w.newAccount(t, "Banesco USD", "USD")
	rent := w.newCategory(t, "Alquiler", category.Expense)

	updated, err := w.ledger.UpdateTransaction(ctx, original.ID, ledger.UpdateTransactionInput{
		AccountID:       other,
		Type:            ledger.Expense,
		CategoryID:      rent,
		PaymentMethodID: methodBankTransfer,
		Amount:          "410.50",
		Description:     ptr("  Alquiler de septiembre  "),
		EffectiveDate:   "2026-09-02",
	})
	if err != nil {
		t.Fatalf("UpdateTransaction: %v", err)
	}

	checks := []struct {
		field string
		ok    bool
	}{
		{"ID is preserved", updated.ID == original.ID},
		{"AccountID", updated.AccountID == other},
		{"CategoryID", updated.CategoryID != nil && *updated.CategoryID == rent},
		{"PaymentMethodID", updated.PaymentMethodID != nil && *updated.PaymentMethodID == methodBankTransfer},
		{"PaymentMethodName", updated.PaymentMethodName != nil && *updated.PaymentMethodName == "Bank transfer"},
		{"AmountMinor is negative", updated.AmountMinor == -41050},
		{"AmountFormatted", updated.AmountFormatted == "-410.50"},
		{"Description is trimmed", updated.Description != nil && *updated.Description == "Alquiler de septiembre"},
		{"EffectiveDate", updated.EffectiveDate.Format("2006-01-02") == "2026-09-02"},
		{"MovementID is preserved", updated.MovementID == original.MovementID},
	}
	for _, check := range checks {
		if !check.ok {
			t.Errorf("%s is wrong after the edit: %+v", check.field, updated)
		}
	}

	// One transaction and one movement, still.
	if transactions, movements := w.counts(t); transactions != 1 || movements != 1 {
		t.Errorf("after editing there are %d transactions and %d movements, want 1 and 1",
			transactions, movements)
	}
	w.assertNoOrphanTransactions(t)

	// The money moved with it.
	if got := w.balance(t, w.usdAccount); got != 0 {
		t.Errorf("the original account still holds %d, want 0", got)
	}
	if got := w.balance(t, other); got != -41050 {
		t.Errorf("the new account holds %d, want -41050", got)
	}
}

// Entering an expense as income is a plausible mistake, so correcting it must
// work -- and the sign must come from the new type, never from the old row.
func TestUpdateTransactionFlipsTheSignWithTheType(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	expense := w.anExpense(t)
	if before := w.balance(t, w.usdAccount); before != -2599 {
		t.Fatalf("balance before the edit = %d, want -2599", before)
	}

	income, err := w.ledger.UpdateTransaction(ctx, expense.ID, ledger.UpdateTransactionInput{
		AccountID:       w.usdAccount,
		Type:            ledger.Income,
		CategoryID:      w.salary,
		PaymentMethodID: methodBankTransfer,
		Amount:          "25.99",
		EffectiveDate:   today,
	})
	if err != nil {
		t.Fatalf("EXPENSE -> INCOME: %v", err)
	}
	if income.Type != ledger.Income {
		t.Errorf("Type = %q, want %q", income.Type, ledger.Income)
	}
	if income.AmountMinor != 2599 {
		t.Errorf("AmountMinor = %d, want +2599", income.AmountMinor)
	}
	if got := w.balance(t, w.usdAccount); got != 2599 {
		t.Errorf("balance after EXPENSE -> INCOME = %d, want +2599", got)
	}

	// And back the other way.
	back, err := w.ledger.UpdateTransaction(ctx, expense.ID, ledger.UpdateTransactionInput{
		AccountID:       w.usdAccount,
		Type:            ledger.Expense,
		CategoryID:      w.food,
		PaymentMethodID: methodPOS,
		Amount:          "25.99",
		EffectiveDate:   today,
	})
	if err != nil {
		t.Fatalf("INCOME -> EXPENSE: %v", err)
	}
	if back.AmountMinor != -2599 {
		t.Errorf("AmountMinor = %d, want -2599", back.AmountMinor)
	}
	if got := w.balance(t, w.usdAccount); got != -2599 {
		t.Errorf("balance after INCOME -> EXPENSE = %d, want -2599", got)
	}
}

// An edit must be held to exactly the same invariants as a creation. If it were
// not, every rule this package maintains would have a second door.
func TestUpdateTransactionRevalidatesEverything(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	archivedAccount := w.newAccount(t, "Cerrada USD", "USD")
	if _, err := w.accounts.SetActive(ctx, archivedAccount, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}
	archivedCategory := w.newCategory(t, "Vieja", category.Expense)
	if _, err := w.categories.SetActive(ctx, archivedCategory, false); err != nil {
		t.Fatalf("archiving the category: %v", err)
	}
	methods := paymentmethod.NewService(w.db)
	archivedMethod, err := methods.Create(ctx, paymentmethod.CreateInput{Name: "Cheque"})
	if err != nil {
		t.Fatalf("creating the method: %v", err)
	}
	if _, err := methods.SetActive(ctx, archivedMethod.ID, false); err != nil {
		t.Fatalf("archiving the method: %v", err)
	}

	valid := ledger.UpdateTransactionInput{
		AccountID:       w.usdAccount,
		Type:            ledger.Expense,
		CategoryID:      w.food,
		PaymentMethodID: methodPOS,
		Amount:          "10.00",
		EffectiveDate:   today,
	}

	tests := map[string]struct {
		mutate func(*ledger.UpdateTransactionInput)
		want   error
	}{
		"unknown account":  {func(in *ledger.UpdateTransactionInput) { in.AccountID = 4242 }, ledger.ErrAccountNotFound},
		"unknown category": {func(in *ledger.UpdateTransactionInput) { in.CategoryID = 4242 }, ledger.ErrCategoryNotFound},
		// The subtest builds its own world, so w.salary here refers to the id
		// this fixture also assigns to the income category -- 1 in both.
		"category of the wrong type": {
			func(in *ledger.UpdateTransactionInput) { in.CategoryID = w.salary }, ledger.ErrCategoryTypeMismatch,
		},
		"unknown method":     {func(in *ledger.UpdateTransactionInput) { in.PaymentMethodID = 4242 }, ledger.ErrPaymentMethodNotFound},
		"negative amount":    {func(in *ledger.UpdateTransactionInput) { in.Amount = "-10.00" }, ledger.ErrInvalidAmount},
		"zero amount":        {func(in *ledger.UpdateTransactionInput) { in.Amount = "0" }, ledger.ErrInvalidAmount},
		"over-precise":       {func(in *ledger.UpdateTransactionInput) { in.Amount = "10.005" }, ledger.ErrInvalidAmount},
		"unparseable amount": {func(in *ledger.UpdateTransactionInput) { in.Amount = "mucho" }, ledger.ErrInvalidAmount},
		"impossible date":    {func(in *ledger.UpdateTransactionInput) { in.EffectiveDate = "2026-02-31" }, ledger.ErrInvalidInput},
		"blank description":  {func(in *ledger.UpdateTransactionInput) { in.Description = ptr("   ") }, ledger.ErrInvalidInput},
	}

	for label, tc := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			// A world of its own, so the rollback assertion means something.
			w := newWorld(t)
			original := w.anExpense(t)
			before := w.balance(t, w.usdAccount)

			in := ledger.UpdateTransactionInput{
				AccountID:       w.usdAccount,
				Type:            ledger.Expense,
				CategoryID:      w.food,
				PaymentMethodID: methodPOS,
				Amount:          "10.00",
				EffectiveDate:   today,
			}
			tc.mutate(&in)

			_, err := w.ledger.UpdateTransaction(t.Context(), original.ID, in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("UpdateTransaction = %v, want %v", err, tc.want)
			}

			// Nothing moved.
			after, err := w.ledger.GetTransaction(t.Context(), original.ID)
			if err != nil {
				t.Fatalf("GetTransaction: %v", err)
			}
			if after.AmountMinor != original.AmountMinor {
				t.Errorf("a rejected edit changed the amount: %d, was %d",
					after.AmountMinor, original.AmountMinor)
			}
			if after.AccountID != original.AccountID {
				t.Errorf("a rejected edit changed the account")
			}
			if got := w.balance(t, w.usdAccount); got != before {
				t.Errorf("a rejected edit changed the balance: %d, was %d", got, before)
			}
		})
	}

	// The archived cases run against the world that owns those ids, rather than
	// against a fresh one per subtest.
	original := w.anExpense(t)
	before := w.balance(t, w.usdAccount)

	archived := map[string]struct {
		mutate func(*ledger.UpdateTransactionInput)
		want   error
	}{
		"archived account":  {func(in *ledger.UpdateTransactionInput) { in.AccountID = archivedAccount }, ledger.ErrAccountInactive},
		"archived category": {func(in *ledger.UpdateTransactionInput) { in.CategoryID = archivedCategory }, ledger.ErrCategoryInactive},
		"archived method":   {func(in *ledger.UpdateTransactionInput) { in.PaymentMethodID = archivedMethod.ID }, ledger.ErrPaymentMethodInactive},
	}
	for label, tc := range archived {
		in := valid
		tc.mutate(&in)
		if _, err := w.ledger.UpdateTransaction(ctx, original.ID, in); !errors.Is(err, tc.want) {
			t.Errorf("%s: UpdateTransaction = %v, want %v", label, err, tc.want)
		}
	}
	if got := w.balance(t, w.usdAccount); got != before {
		t.Errorf("a rejected edit changed the balance: %d, was %d", got, before)
	}
}

// Moving a transaction to an account in another currency reinterprets its
// amount rather than converting it, so it has to be asked for explicitly.
func TestUpdateTransactionRequiresConfirmationToChangeCurrency(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	ves := w.newAccount(t, "Mercantil VES", "VES")
	original := w.anExpense(t)
	before := w.balance(t, w.usdAccount)

	in := ledger.UpdateTransactionInput{
		AccountID:       ves,
		Type:            ledger.Expense,
		CategoryID:      w.food,
		PaymentMethodID: methodPOS,
		Amount:          "25.99",
		EffectiveDate:   today,
	}

	_, err := w.ledger.UpdateTransaction(ctx, original.ID, in)
	if !errors.Is(err, ledger.ErrCurrencyChangeNotConfirmed) {
		t.Fatalf("UpdateTransaction across currencies = %v, want ErrCurrencyChangeNotConfirmed", err)
	}
	if got := w.balance(t, w.usdAccount); got != before {
		t.Errorf("the refused edit still moved money: balance %d, was %d", got, before)
	}

	// With the flag it goes through, and the amount is read at the destination's
	// scale rather than converted.
	in.AllowCurrencyChange = true
	moved, err := w.ledger.UpdateTransaction(ctx, original.ID, in)
	if err != nil {
		t.Fatalf("UpdateTransaction with confirmation: %v", err)
	}
	if moved.CurrencyCode != "VES" {
		t.Errorf("CurrencyCode = %q, want VES", moved.CurrencyCode)
	}
	if moved.AmountMinor != -2599 {
		t.Errorf("AmountMinor = %d, want -2599 read at the VES scale", moved.AmountMinor)
	}
	if got := w.balance(t, w.usdAccount); got != 0 {
		t.Errorf("the USD account still holds %d, want 0", got)
	}
	if got := w.balance(t, ves); got != -2599 {
		t.Errorf("the VES account holds %d, want -2599", got)
	}
}

// Moving between accounts of the SAME currency is an ordinary correction and
// needs no confirmation.
func TestUpdateTransactionNeedsNoConfirmationWithinACurrency(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	other := w.newAccount(t, "Banesco USD", "USD")
	original := w.anExpense(t)

	if _, err := w.ledger.UpdateTransaction(t.Context(), original.ID, ledger.UpdateTransactionInput{
		AccountID:       other,
		Type:            ledger.Expense,
		CategoryID:      w.food,
		PaymentMethodID: methodPOS,
		Amount:          "25.99",
		EffectiveDate:   today,
	}); err != nil {
		t.Errorf("moving between USD accounts asked for confirmation: %v", err)
	}
}

// An opening balance is a statement about where an account started, not an
// operation, so it cannot become income and income cannot become one.
func TestOpeningBalancesCannotChangeType(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	opening, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}

	_, err = w.ledger.UpdateTransaction(ctx, opening.ID, ledger.UpdateTransactionInput{
		AccountID: w.usdAccount, Type: ledger.Income, CategoryID: w.salary,
		PaymentMethodID: methodBankTransfer, Amount: "1000", EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("opening balance -> income = %v, want ErrTypeChangeNotAllowed", err)
	}

	// And the reverse: an ordinary transaction cannot be edited as one.
	expense := w.anExpense(t)
	_, err = w.ledger.UpdateOpeningBalance(ctx, expense.ID, ledger.UpdateOpeningBalanceInput{
		Amount: "50", EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrTypeChangeNotAllowed) {
		t.Errorf("editing an expense as an opening balance = %v, want ErrTypeChangeNotAllowed", err)
	}

	if got := w.balance(t, w.usdAccount); got != 100000-2599 {
		t.Errorf("a rejected type change moved money: balance %d", got)
	}
}

func TestUpdateOpeningBalance(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	opening, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}
	if before := w.balance(t, w.usdAccount); before != 100000 {
		t.Fatalf("balance before = %d, want 100000", before)
	}

	updated, err := w.ledger.UpdateOpeningBalance(ctx, opening.ID, ledger.UpdateOpeningBalanceInput{
		Amount: "1250.75", EffectiveDate: "2026-08-31",
	})
	if err != nil {
		t.Fatalf("UpdateOpeningBalance: %v", err)
	}
	if updated.AmountMinor != 125075 {
		t.Errorf("AmountMinor = %d, want 125075", updated.AmountMinor)
	}
	if updated.EffectiveDate.Format("2006-01-02") != "2026-08-31" {
		t.Errorf("EffectiveDate = %v, want 2026-08-31", updated.EffectiveDate)
	}
	if updated.Type != ledger.OpeningBalance {
		t.Errorf("Type = %q, want %q", updated.Type, ledger.OpeningBalance)
	}
	// Still uncategorised and with no channel.
	if updated.CategoryID != nil || updated.PaymentMethodID != nil {
		t.Errorf("an opening balance acquired a category or a payment method: %+v", updated)
	}
	if got := w.balance(t, w.usdAccount); got != 125075 {
		t.Errorf("balance after = %d, want 125075", got)
	}

	// A negative starting balance is meaningful.
	if _, err := w.ledger.UpdateOpeningBalance(ctx, opening.ID, ledger.UpdateOpeningBalanceInput{
		Amount: "-40.00", EffectiveDate: today,
	}); err != nil {
		t.Errorf("a negative opening balance was rejected: %v", err)
	}
	if got := w.balance(t, w.usdAccount); got != -4000 {
		t.Errorf("balance after going negative = %d, want -4000", got)
	}
}

// Zero means "record nothing" when creating. On an edit it would leave a
// transaction with no financial effect, and M3 has no delete.
func TestUpdateOpeningBalanceRejectsZero(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	opening, err := w.ledger.RecordOpeningBalance(ctx, ledger.RecordOpeningBalanceInput{
		AccountID: w.usdAccount, Amount: "1000", EffectiveDate: today,
	})
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}

	for _, amount := range []string{"0", "0.00", "-0"} {
		_, err := w.ledger.UpdateOpeningBalance(ctx, opening.ID, ledger.UpdateOpeningBalanceInput{
			Amount: amount, EffectiveDate: today,
		})
		if !errors.Is(err, ledger.ErrInvalidAmount) {
			t.Errorf("UpdateOpeningBalance(%q) = %v, want ErrInvalidAmount", amount, err)
		}
	}

	if got := w.balance(t, w.usdAccount); got != 100000 {
		t.Errorf("a rejected edit changed the balance to %d, want 100000", got)
	}
}

func TestUpdateUnknownTransaction(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.ledger.UpdateTransaction(t.Context(), 4242, ledger.UpdateTransactionInput{
		AccountID: w.usdAccount, Type: ledger.Expense, CategoryID: w.food,
		PaymentMethodID: methodPOS, Amount: "10", EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("editing an unknown transaction = %v, want ErrNotFound", err)
	}

	_, err = w.ledger.UpdateOpeningBalance(t.Context(), 4242, ledger.UpdateOpeningBalanceInput{
		Amount: "10", EffectiveDate: today,
	})
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("editing an unknown opening balance = %v, want ErrNotFound", err)
	}
}

// Editing metadata elsewhere must not disturb a balance. Accounts and
// categories carry no money, so correcting their names cannot move any.
func TestEditingAccountAndCategoryMetadataLeavesBalancesAlone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	w.anExpense(t)
	before := w.balance(t, w.usdAccount)

	if _, err := w.accounts.UpdateMetadata(ctx, w.usdAccount, account.UpdateMetadataInput{
		Name: "Mercantil Ahorros", Type: account.Bank,
		Subtype: subtypePtr(account.Savings), Institution: ptr("Mercantil"),
	}); err != nil {
		t.Fatalf("UpdateMetadata on the account: %v", err)
	}
	if _, err := w.categories.UpdateMetadata(ctx, w.food, category.UpdateMetadataInput{
		Name: "Alimentación",
	}); err != nil {
		t.Fatalf("UpdateMetadata on the category: %v", err)
	}

	if got := w.balance(t, w.usdAccount); got != before {
		t.Errorf("editing metadata changed the balance: %d, was %d", got, before)
	}

	// And the transaction still reads correctly, through the new names.
	listed, err := w.ledger.ListTransactions(ctx, ledger.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListTransactions returned %d rows, want 1", len(listed))
	}
	if listed[0].AccountName != "Mercantil Ahorros" {
		t.Errorf("AccountName = %q, want the corrected name", listed[0].AccountName)
	}
	if listed[0].CategoryName == nil || *listed[0].CategoryName != "Alimentación" {
		t.Errorf("CategoryName = %v, want the corrected name", listed[0].CategoryName)
	}
}

func subtypePtr(s account.Subtype) *account.Subtype { return &s }
