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

	// RecordTransfer and UpdateTransfer are the ONLY way a transfer is created
	// or corrected. RecordIncome, RecordExpense and UpdateTransaction refuse
	// TRANSFER, and UpdateTransfer refuses everything that is not one.
	RecordTransfer(ctx context.Context, in RecordTransferInput) (TransactionView, error)
	UpdateTransfer(ctx context.Context, id int64, in UpdateTransferInput) (TransactionView, error)

	// RecordExchange and UpdateExchange are the ONLY way an FX_EXCHANGE is
	// created or corrected. Every other use case refuses one, and UpdateExchange
	// refuses everything that is not one.
	//
	// Neither reads nor writes an exchange rate. The rates administered on the
	// exchange rates screen are REFERENCE rates used to value balances; what
	// these record is what the user actually traded at, stated by the two
	// amounts.
	RecordExchange(ctx context.Context, in RecordExchangeInput) (TransactionView, error)
	UpdateExchange(ctx context.Context, id int64, in UpdateExchangeInput) (TransactionView, error)

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

// ExchangeRateService is the M5 surface for reference exchange rates.
//
// Everything it returns is REFERENCE / VALUATION FX. Nothing here is the rate a
// transaction happened at, and no transaction carries one.
type ExchangeRateService interface {
	// List returns observations, newest effective date first.
	List(ctx context.Context, in ListExchangeRatesInput) ([]ExchangeRate, error)

	// Get returns one observation, archived or not.
	Get(ctx context.Context, id int64) (ExchangeRate, error)

	// CreateManual records a rate the user typed. The source is set by the
	// backend; see CreateManualRateInput.
	CreateManual(ctx context.Context, in CreateManualRateInput) (ExchangeRate, error)

	// UpdateManual corrects a manual rate's value. It returns
	// ErrRateNotEditable for an observation from an automatic source.
	UpdateManual(ctx context.Context, id int64, in UpdateManualRateInput) (ExchangeRate, error)

	// SetManualActive archives or restores a manual rate. Archiving is how an
	// override is withdrawn; there is no delete.
	SetManualActive(ctx context.Context, id int64, isActive bool) (ExchangeRate, error)

	// RefreshNow fetches the external source immediately.
	//
	// A failure here is ordinary. Report it on the page and leave everything
	// else working -- never a 500, and never a reason to hide the rates that are
	// already stored.
	RefreshNow(ctx context.Context) (RefreshRatesResult, error)

	// ProviderEnabled reports whether an automatic source is configured, so the
	// interface can omit a button that could not do anything.
	ProviderEnabled() bool
}

// SettingsService owns the application's own configuration.
type SettingsService interface {
	Get(ctx context.Context) (AppSettings, error)

	// SetBaseCurrency changes the valuation base. It changes no transaction, no
	// movement and no balance.
	SetBaseCurrency(ctx context.Context, code string) (AppSettings, error)
}

// ValuationService computes the consolidated net worth.
//
// The result is derived on every call and never stored. A handler must not cache
// it, and no template may add figures from it together -- the totals are already
// computed, in integer minor units, by the only code allowed to do arithmetic on
// money.
type ValuationService interface {
	GetCurrentNetWorth(ctx context.Context) (NetWorth, error)
}

// SavingsService is M7.1: the savings policy and the suggestion each eligible
// income produces.
//
// A suggestion is a target, never money: nothing here moves a balance or takes
// part in the net worth. Incomes are recorded and corrected through
// LedgerService as before; the adapter commits each one together with its
// suggestion. Actions on a suggestion name the INCOME, and act on that income's
// live revision.
type SavingsService interface {
	Overview(ctx context.Context) (SavingsOverview, error)

	GetPolicy(ctx context.Context) (SavingsPolicy, error)
	SetPolicy(ctx context.Context, in SetSavingsPolicyInput) error
	ListCategoryEligibility(ctx context.Context) ([]SavingsCategoryEligibility, error)
	SetCategoryEligibility(ctx context.Context, categoryID int64, eligible bool) error
	SetIncomeOverride(ctx context.Context, transactionID int64, eligible bool, reason string) error

	ListSuggestions(ctx context.Context) ([]SavingsSuggestion, error)
	IncomeDetail(ctx context.Context, transactionID int64) (SavingsIncomeDetail, error)
	ValueSuggestion(ctx context.Context, transactionID int64) error
	ValuePending(ctx context.Context) (int, error)
	RevalueSuggestion(ctx context.Context, transactionID int64) error
	DismissSuggestion(ctx context.Context, transactionID int64, reason string) error

	// PlanSync previews; ApplySync changes exactly the incomes named.
	PlanSync(ctx context.Context) (SavingsSyncPlan, error)
	ApplySync(ctx context.Context, transactionIDs []int64) (SavingsSyncResult, error)

	// M7.2: the fund. Reserve and Release are idempotent on their request key.
	GetFund(ctx context.Context) (SavingsFund, error)
	Reserve(ctx context.Context, in SavingsReserveInput) (SavingsOperation, error)
	Release(ctx context.Context, in SavingsReleaseInput) (SavingsOperation, error)
	ListOperations(ctx context.Context, limit int) ([]SavingsOperation, error)
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

	Rates     ExchangeRateService
	Settings  SettingsService
	Valuation ValuationService

	// Analytics is the M6 dashboard: the net worth's history and the period's
	// cash flow and spending.
	Analytics AnalyticsService

	// Savings is M7.1: the savings policy and its suggestions.
	Savings SavingsService
}
