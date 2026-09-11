package adapter_test

import (
	"errors"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// The transfer half of the boundary: the whole detail must cross it, and so must
// the information a two-field form needs to mark the right field.

func newTransferAccounts(t *testing.T, svcs *web.Services) (source, destination, ves int64) {
	t.Helper()
	ctx := t.Context()

	checking := web.AccountSubtypeChecking
	mercantil, err := svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name: "Mercantil USD", Type: web.AccountTypeBank, Subtype: &checking,
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating the source account: %v", err)
	}
	efectivo, err := svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name: "Efectivo USD", Type: web.AccountTypeCash, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating the destination account: %v", err)
	}
	banesco, err := svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name: "Banesco VES", Type: web.AccountTypeCash, CurrencyCode: "VES",
	})
	if err != nil {
		t.Fatalf("creating the VES account: %v", err)
	}
	return mercantil.ID, efectivo.ID, banesco.ID
}

func TestRecordTransferCrossesTheBoundaryWhole(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svcs := newTestServices(t, db)
	ctx := t.Context()

	source, destination, _ := newTransferAccounts(t, svcs)

	description := "Pasar dinero a efectivo"
	view, err := svcs.Ledger.RecordTransfer(ctx, web.RecordTransferInput{
		SourceAccountID:      source,
		DestinationAccountID: destination,
		Amount:               "250.00",
		Description:          &description,
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	if view.Type != web.TransactionTypeTransfer {
		t.Errorf("type = %q, want %q", view.Type, web.TransactionTypeTransfer)
	}
	if view.CategoryID != nil || view.PaymentMethodID != nil {
		t.Error("the transfer crossed with a category or a payment method")
	}

	detail := view.Transfer
	if detail == nil {
		t.Fatal("the Transfer detail did not cross the boundary")
	}
	if detail.SourceAccountID != source || detail.SourceAccountName != "Mercantil USD" {
		t.Errorf("source = %d/%q", detail.SourceAccountID, detail.SourceAccountName)
	}
	if detail.DestinationAccountID != destination || detail.DestinationAccountName != "Efectivo USD" {
		t.Errorf("destination = %d/%q", detail.DestinationAccountID, detail.DestinationAccountName)
	}
	if detail.AmountMinor != 25000 || detail.AmountFormatted != "250.00" {
		t.Errorf("amount = %d/%q, want 25000/\"250.00\"", detail.AmountMinor, detail.AmountFormatted)
	}
	if detail.SourceMovementID == 0 || detail.DestinationMovementID == 0 {
		t.Errorf("movement ids = %d/%d, want both set",
			detail.SourceMovementID, detail.DestinationMovementID)
	}

	// It is one row in the listing, with its detail intact.
	views, err := svcs.Ledger.ListTransactions(ctx, web.ListTransactionsInput{})
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("the listing has %d rows, want 1", len(views))
	}
	if views[0].Transfer == nil || *views[0].Transfer != *detail {
		t.Errorf("listed detail = %v, want %+v", views[0].Transfer, *detail)
	}

	// And the balances moved without the currency total moving.
	totals, err := svcs.Ledger.ListCurrencyTotals(ctx, false)
	if err != nil {
		t.Fatalf("listing currency totals: %v", err)
	}
	for _, total := range totals {
		if total.CurrencyCode == "USD" && total.TotalMinor != 0 {
			t.Errorf("the USD total is %d, want 0 -- a transfer nets to nothing", total.TotalMinor)
		}
	}
}

func TestUpdateTransferCrossesTheBoundary(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svcs := newTestServices(t, db)
	ctx := t.Context()

	source, destination, _ := newTransferAccounts(t, svcs)

	created, err := svcs.Ledger.RecordTransfer(ctx, web.RecordTransferInput{
		SourceAccountID:      source,
		DestinationAccountID: destination,
		Amount:               "250.00",
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	updated, err := svcs.Ledger.UpdateTransfer(ctx, created.ID, web.UpdateTransferInput{
		SourceAccountID:      source,
		DestinationAccountID: destination,
		Amount:               "300.00",
		EffectiveDate:        "2026-09-07",
	})
	if err != nil {
		t.Fatalf("updating the transfer: %v", err)
	}
	if updated.Transfer.AmountMinor != 30000 {
		t.Errorf("amount = %d, want 30000", updated.Transfer.AmountMinor)
	}
	if updated.Transfer.SourceMovementID != created.Transfer.SourceMovementID ||
		updated.Transfer.DestinationMovementID != created.Transfer.DestinationMovementID {
		t.Error("the movement ids changed across an edit")
	}

	balance, err := svcs.Ledger.GetAccountBalance(ctx, destination)
	if err != nil {
		t.Fatalf("reading the balance: %v", err)
	}
	if balance.BalanceMinor != 30000 {
		t.Errorf("destination balance = %d, want 30000", balance.BalanceMinor)
	}
}

// The presentation layer must be able to tell WHICH account field to mark
// without importing the ledger package to do it.
func TestTransferErrorsTranslateIntoTheWebVocabulary(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svcs := newTestServices(t, db)
	ctx := t.Context()

	source, destination, ves := newTransferAccounts(t, svcs)

	archived, err := svcs.Accounts.Create(ctx, web.CreateAccountInput{
		Name: "Archivada USD", Type: web.AccountTypeCash, CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("creating the account to archive: %v", err)
	}
	if _, err := svcs.Accounts.SetActive(ctx, archived.ID, false); err != nil {
		t.Fatalf("archiving the account: %v", err)
	}

	tests := map[string]struct {
		in       web.RecordTransferInput
		want     error
		wantSide web.TransferSide // empty means no side is expected
	}{
		"same account": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: source,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrSameAccount, "",
		},
		"currency mismatch": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: ves,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrCurrencyMismatch, "",
		},
		"unknown source": {
			web.RecordTransferInput{SourceAccountID: 9999, DestinationAccountID: destination,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrAccountNotFound, web.TransferSideSource,
		},
		"unknown destination": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: 9999,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrAccountNotFound, web.TransferSideDestination,
		},
		"archived source": {
			web.RecordTransferInput{SourceAccountID: archived.ID, DestinationAccountID: destination,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrAccountInactive, web.TransferSideSource,
		},
		"archived destination": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: archived.ID,
				Amount: "250.00", EffectiveDate: "2026-09-06"},
			web.ErrAccountInactive, web.TransferSideDestination,
		},
		"invalid amount": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: destination,
				Amount: "-250.00", EffectiveDate: "2026-09-06"},
			web.ErrInvalidAmount, "",
		},
		"invalid date": {
			web.RecordTransferInput{SourceAccountID: source, DestinationAccountID: destination,
				Amount: "250.00", EffectiveDate: "2026-02-31"},
			web.ErrInvalidInput, "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := svcs.Ledger.RecordTransfer(ctx, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}

			var accountErr *web.TransferAccountError
			switch {
			case tc.wantSide == "":
				if errors.As(err, &accountErr) {
					t.Errorf("error = %v, want no side attached", err)
				}
			case !errors.As(err, &accountErr):
				t.Fatalf("error = %v, want a *web.TransferAccountError", err)
			case accountErr.Side != tc.wantSide:
				t.Errorf("side = %q, want %q", accountErr.Side, tc.wantSide)
			}
		})
	}
}

// The transfer use cases and the income-and-expense ones must not be reachable
// from each other's inputs, at the boundary as well as in the ledger.
func TestTheBoundaryKeepsTheUseCasesApart(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	svcs := newTestServices(t, db)
	ctx := t.Context()

	source, destination, _ := newTransferAccounts(t, svcs)

	category, err := svcs.Categories.Create(ctx, web.CreateCategoryInput{
		Name: "Salario", Type: web.CategoryTypeIncome,
	})
	if err != nil {
		t.Fatalf("creating the category: %v", err)
	}

	transfer, err := svcs.Ledger.RecordTransfer(ctx, web.RecordTransferInput{
		SourceAccountID:      source,
		DestinationAccountID: destination,
		Amount:               "250.00",
		EffectiveDate:        "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the transfer: %v", err)
	}

	// The generic editor refuses it.
	if _, err := svcs.Ledger.UpdateTransaction(ctx, transfer.ID, web.UpdateTransactionInput{
		AccountID: source, Type: web.TransactionTypeIncome, CategoryID: category.ID,
		PaymentMethodID: 1, Amount: "250.00", EffectiveDate: "2026-09-06",
	}); !errors.Is(err, web.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateTransaction on a transfer = %v, want ErrTypeChangeNotAllowed", err)
	}

	// So does the opening-balance editor.
	if _, err := svcs.Ledger.UpdateOpeningBalance(ctx, transfer.ID, web.UpdateOpeningBalanceInput{
		Amount: "250.00", EffectiveDate: "2026-09-06",
	}); !errors.Is(err, web.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateOpeningBalance on a transfer = %v, want ErrTypeChangeNotAllowed", err)
	}

	// And the transfer editor refuses an income.
	income, err := svcs.Ledger.RecordIncome(ctx, web.RecordIncomeInput{
		AccountID: source, CategoryID: category.ID, Amount: "500.00",
		PaymentMethodID: 1, EffectiveDate: "2026-09-06",
	})
	if err != nil {
		t.Fatalf("recording the income: %v", err)
	}
	if _, err := svcs.Ledger.UpdateTransfer(ctx, income.ID, web.UpdateTransferInput{
		SourceAccountID: source, DestinationAccountID: destination,
		Amount: "500.00", EffectiveDate: "2026-09-06",
	}); !errors.Is(err, web.ErrTypeChangeNotAllowed) {
		t.Errorf("UpdateTransfer on an income = %v, want ErrTypeChangeNotAllowed", err)
	}

	// The transfer is untouched by any of it.
	after, err := svcs.Ledger.GetTransaction(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("re-reading the transfer: %v", err)
	}
	if after.Type != web.TransactionTypeTransfer || after.Transfer == nil {
		t.Fatalf("the transfer became %q", after.Type)
	}
	if *after.Transfer != *transfer.Transfer {
		t.Errorf("detail = %+v, want %+v", *after.Transfer, *transfer.Transfer)
	}
}
