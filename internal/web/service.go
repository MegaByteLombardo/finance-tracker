package web

import (
	"context"
)

// Consumer interfaces required by the presentation layer as defined in docs/m2-contract.md.

type CurrencyService interface {
	List(ctx context.Context, includeInactive bool) ([]Currency, error)
	Get(ctx context.Context, code string) (Currency, error)
}

type AccountService interface {
	List(ctx context.Context, includeInactive bool) ([]Account, error)
	Get(ctx context.Context, id int64) (Account, error)
	Create(ctx context.Context, in CreateAccountInput) (Account, error)
	UpdateMetadata(ctx context.Context, id int64, in UpdateAccountMetadataInput) (Account, error)
	SetActive(ctx context.Context, id int64, isActive bool) (Account, error)
}

type CategoryService interface {
	List(ctx context.Context, in ListCategoriesInput) ([]Category, error)
	Get(ctx context.Context, id int64) (Category, error)
	Create(ctx context.Context, in CreateCategoryInput) (Category, error)
	UpdateMetadata(ctx context.Context, id int64, in UpdateCategoryMetadataInput) (Category, error)
	SetActive(ctx context.Context, id int64, isActive bool) (Category, error)
}

type LedgerService interface {
	RecordIncome(ctx context.Context, in RecordIncomeInput) (TransactionView, error)
	RecordExpense(ctx context.Context, in RecordExpenseInput) (TransactionView, error)
	RecordOpeningBalance(ctx context.Context, in RecordOpeningBalanceInput) (*TransactionView, error)

	UpdateTransaction(ctx context.Context, id int64, in UpdateTransactionInput) (TransactionView, error)
	UpdateOpeningBalance(ctx context.Context, id int64, in UpdateOpeningBalanceInput) (TransactionView, error)

	ListTransactions(ctx context.Context, in ListTransactionsInput) ([]TransactionView, error)
	GetTransaction(ctx context.Context, id int64) (TransactionView, error)

	GetAccountBalance(ctx context.Context, accountID int64) (AccountBalance, error)
	ListAccountBalances(ctx context.Context, includeInactive bool) ([]AccountBalance, error)
	ListCurrencyTotals(ctx context.Context, includeInactive bool) ([]CurrencyTotal, error)
}

type PaymentMethodService interface {
	List(ctx context.Context, in ListPaymentMethodsInput) ([]PaymentMethod, error)
	Get(ctx context.Context, id int64) (PaymentMethod, error)
	Create(ctx context.Context, in CreatePaymentMethodInput) (PaymentMethod, error)
	UpdateMetadata(ctx context.Context, id int64, in UpdatePaymentMethodMetadataInput) (PaymentMethod, error)
	SetActive(ctx context.Context, id int64, isActive bool) (PaymentMethod, error)
}

type BackupService interface {
	List(ctx context.Context) ([]Backup, error)
	CreateManual(ctx context.Context) (Backup, error)
}

type ApplicationService interface {
	OpenAccount(ctx context.Context, in OpenAccountInput) (OpenAccountResult, error)
}

// Services bundles all services needed by the presentation layer.
type Services struct {
	Currencies  CurrencyService
	Accounts    AccountService
	Categories  CategoryService
	Methods     PaymentMethodService
	Ledger      LedgerService
	Backups     BackupService
	Application ApplicationService
}
