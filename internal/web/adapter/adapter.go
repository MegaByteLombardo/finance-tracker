package adapter

import (
	"context"
	"database/sql"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/analytics"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/settings"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// Currency adapter

type currencyAdapter struct {
	svc *currency.Service
}

func (a *currencyAdapter) List(ctx context.Context, includeInactive bool) ([]web.Currency, error) {
	list, err := a.svc.List(ctx, includeInactive)
	if err != nil {
		return nil, TranslateCurrencyError(err)
	}
	return toWebCurrencies(list), nil
}

func (a *currencyAdapter) Get(ctx context.Context, code string) (web.Currency, error) {
	c, err := a.svc.Get(ctx, code)
	if err != nil {
		return web.Currency{}, TranslateCurrencyError(err)
	}
	return toWebCurrency(c), nil
}

// Account adapter

type accountAdapter struct {
	svc *account.Service
}

func (a *accountAdapter) List(ctx context.Context, includeInactive bool) ([]web.Account, error) {
	list, err := a.svc.List(ctx, includeInactive)
	if err != nil {
		return nil, TranslateAccountError(err)
	}
	return toWebAccounts(list), nil
}

func (a *accountAdapter) Get(ctx context.Context, id int64) (web.Account, error) {
	acct, err := a.svc.Get(ctx, id)
	if err != nil {
		return web.Account{}, TranslateAccountError(err)
	}
	return toWebAccount(acct), nil
}

func (a *accountAdapter) Create(ctx context.Context, in web.CreateAccountInput) (web.Account, error) {
	acct, err := a.svc.Create(ctx, toDomainCreateAccountInput(in))
	if err != nil {
		return web.Account{}, TranslateAccountError(err)
	}
	return toWebAccount(acct), nil
}

func (a *accountAdapter) UpdateMetadata(ctx context.Context, id int64, in web.UpdateAccountMetadataInput) (web.Account, error) {
	acct, err := a.svc.UpdateMetadata(ctx, id, toDomainUpdateAccountMetadataInput(in))
	if err != nil {
		return web.Account{}, TranslateAccountError(err)
	}
	return toWebAccount(acct), nil
}

func (a *accountAdapter) SetActive(ctx context.Context, id int64, isActive bool) (web.Account, error) {
	acct, err := a.svc.SetActive(ctx, id, isActive)
	if err != nil {
		return web.Account{}, TranslateAccountError(err)
	}
	return toWebAccount(acct), nil
}

// Category adapter

type categoryAdapter struct {
	svc *category.Service
}

func (a *categoryAdapter) List(ctx context.Context, in web.ListCategoriesInput) ([]web.Category, error) {
	list, err := a.svc.List(ctx, toDomainListCategoriesInput(in))
	if err != nil {
		return nil, TranslateCategoryError(err)
	}
	return toWebCategories(list), nil
}

func (a *categoryAdapter) Get(ctx context.Context, id int64) (web.Category, error) {
	cat, err := a.svc.Get(ctx, id)
	if err != nil {
		return web.Category{}, TranslateCategoryError(err)
	}
	return toWebCategory(cat), nil
}

func (a *categoryAdapter) Create(ctx context.Context, in web.CreateCategoryInput) (web.Category, error) {
	cat, err := a.svc.Create(ctx, toDomainCreateCategoryInput(in))
	if err != nil {
		return web.Category{}, TranslateCategoryError(err)
	}
	return toWebCategory(cat), nil
}

func (a *categoryAdapter) UpdateMetadata(ctx context.Context, id int64, in web.UpdateCategoryMetadataInput) (web.Category, error) {
	cat, err := a.svc.UpdateMetadata(ctx, id, toDomainUpdateCategoryMetadataInput(in))
	if err != nil {
		return web.Category{}, TranslateCategoryError(err)
	}
	return toWebCategory(cat), nil
}

func (a *categoryAdapter) SetActive(ctx context.Context, id int64, isActive bool) (web.Category, error) {
	cat, err := a.svc.SetActive(ctx, id, isActive)
	if err != nil {
		return web.Category{}, TranslateCategoryError(err)
	}
	return toWebCategory(cat), nil
}

// Ledger adapter

type ledgerAdapter struct {
	svc *ledger.Service

	// app records and corrects incomes, so that every income the web layer
	// writes commits together with its savings suggestion (M7, TD-080). The
	// adapter is the only production implementation of web.LedgerService, so
	// routing here means no handler -- desktop or mobile -- can write an income
	// that bypasses the suggestion.
	app *application.Service
}

func (a *ledgerAdapter) RecordIncome(ctx context.Context, in web.RecordIncomeInput) (web.TransactionView, error) {
	view, err := a.app.RecordIncome(ctx, toDomainRecordIncomeInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) RecordExpense(ctx context.Context, in web.RecordExpenseInput) (web.TransactionView, error) {
	// Through the application workflow: an expense and what it does to
	// savings commit together (M7.4). Without a plan it is the ledger's
	// RecordExpense, unchanged.
	view, err := a.app.RecordExpense(ctx, toDomainRecordExpenseInput(in), toDomainExpensePlan(in.Savings))
	if err != nil {
		return web.TransactionView{}, translateMovementError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) RecordOpeningBalance(ctx context.Context, in web.RecordOpeningBalanceInput) (*web.TransactionView, error) {
	view, err := a.svc.RecordOpeningBalance(ctx, toDomainRecordOpeningBalanceInput(in))
	if err != nil {
		return nil, TranslateLedgerError(err)
	}
	return toWebTransactionView(view), nil
}

func (a *ledgerAdapter) RecordTransfer(ctx context.Context, in web.RecordTransferInput) (web.TransactionView, error) {
	view, err := a.app.RecordTransfer(ctx, toDomainRecordTransferInput(in), toDomainTransferPlan(in.Savings))
	if err != nil {
		return web.TransactionView{}, translateMovementError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) UpdateTransfer(ctx context.Context, id int64, in web.UpdateTransferInput) (web.TransactionView, error) {
	view, err := a.svc.UpdateTransfer(ctx, id, toDomainUpdateTransferInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) RecordExchange(ctx context.Context, in web.RecordExchangeInput) (web.TransactionView, error) {
	view, err := a.svc.RecordExchange(ctx, toDomainRecordExchangeInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) UpdateExchange(ctx context.Context, id int64, in web.UpdateExchangeInput) (web.TransactionView, error) {
	view, err := a.svc.UpdateExchange(ctx, id, toDomainUpdateExchangeInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) UpdateTransaction(ctx context.Context, id int64, in web.UpdateTransactionInput) (web.TransactionView, error) {
	view, err := a.app.UpdateTransaction(ctx, id, toDomainUpdateTransactionInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) UpdateOpeningBalance(ctx context.Context, id int64, in web.UpdateOpeningBalanceInput) (web.TransactionView, error) {
	view, err := a.svc.UpdateOpeningBalance(ctx, id, toDomainUpdateOpeningBalanceInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) ListTransactions(ctx context.Context, in web.ListTransactionsInput) ([]web.TransactionView, error) {
	list, err := a.svc.ListTransactions(ctx, toDomainListTransactionsInput(in))
	if err != nil {
		return nil, TranslateLedgerError(err)
	}
	return toWebTransactionViews(list), nil
}

func (a *ledgerAdapter) GetTransaction(ctx context.Context, id int64) (web.TransactionView, error) {
	view, err := a.svc.GetTransaction(ctx, id)
	if err != nil {
		return web.TransactionView{}, TranslateLedgerError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *ledgerAdapter) GetAccountBalance(ctx context.Context, accountID int64) (web.AccountBalance, error) {
	bal, err := a.svc.GetAccountBalance(ctx, accountID)
	if err != nil {
		return web.AccountBalance{}, TranslateLedgerError(err)
	}
	return toWebAccountBalance(bal), nil
}

func (a *ledgerAdapter) ListAccountBalances(ctx context.Context, includeInactive bool) ([]web.AccountBalance, error) {
	list, err := a.svc.ListAccountBalances(ctx, includeInactive)
	if err != nil {
		return nil, TranslateLedgerError(err)
	}
	return toWebAccountBalances(list), nil
}

func (a *ledgerAdapter) ListCurrencyTotals(ctx context.Context, includeInactive bool) ([]web.CurrencyTotal, error) {
	list, err := a.svc.ListCurrencyTotals(ctx, includeInactive)
	if err != nil {
		return nil, TranslateLedgerError(err)
	}
	return toWebCurrencyTotals(list), nil
}

// Application adapter

type applicationAdapter struct {
	svc *application.Service
}

func (a *applicationAdapter) OpenAccount(ctx context.Context, in web.OpenAccountInput) (web.OpenAccountResult, error) {
	res, err := a.svc.OpenAccount(ctx, toDomainOpenAccountInput(in))
	if err != nil {
		return web.OpenAccountResult{}, TranslateApplicationError(err)
	}
	return toWebOpenAccountResult(res), nil
}

type paymentMethodAdapter struct {
	svc *paymentmethod.Service
}

func (a *paymentMethodAdapter) List(ctx context.Context, in web.ListPaymentMethodsInput) ([]web.PaymentMethod, error) {
	list, err := a.svc.List(ctx, toDomainListPaymentMethodsInput(in))
	if err != nil {
		return nil, TranslatePaymentMethodError(err)
	}
	return toWebPaymentMethods(list), nil
}

func (a *paymentMethodAdapter) Get(ctx context.Context, id int64) (web.PaymentMethod, error) {
	m, err := a.svc.Get(ctx, id)
	if err != nil {
		return web.PaymentMethod{}, TranslatePaymentMethodError(err)
	}
	return toWebPaymentMethod(m), nil
}

func (a *paymentMethodAdapter) Create(ctx context.Context, in web.CreatePaymentMethodInput) (web.PaymentMethod, error) {
	m, err := a.svc.Create(ctx, toDomainCreatePaymentMethodInput(in))
	if err != nil {
		return web.PaymentMethod{}, TranslatePaymentMethodError(err)
	}
	return toWebPaymentMethod(m), nil
}

func (a *paymentMethodAdapter) UpdateMetadata(
	ctx context.Context, id int64, in web.UpdatePaymentMethodMetadataInput,
) (web.PaymentMethod, error) {
	m, err := a.svc.UpdateMetadata(ctx, id, toDomainUpdatePaymentMethodMetadataInput(in))
	if err != nil {
		return web.PaymentMethod{}, TranslatePaymentMethodError(err)
	}
	return toWebPaymentMethod(m), nil
}

func (a *paymentMethodAdapter) SetActive(ctx context.Context, id int64, isActive bool) (web.PaymentMethod, error) {
	m, err := a.svc.SetActive(ctx, id, isActive)
	if err != nil {
		return web.PaymentMethod{}, TranslatePaymentMethodError(err)
	}
	return toWebPaymentMethod(m), nil
}

// Backup adapter

type backupAdapter struct {
	svc *backup.Service
}

func (a *backupAdapter) List(ctx context.Context) ([]web.Backup, error) {
	if a.svc == nil {
		return nil, nil
	}
	list, err := a.svc.List(ctx)
	if err != nil {
		return nil, TranslateBackupError(err)
	}
	return toWebBackups(list), nil
}

func (a *backupAdapter) CreateManual(ctx context.Context) (web.Backup, error) {
	if a.svc == nil {
		return web.Backup{}, web.ErrInvalidInput
	}
	b, err := a.svc.Create(ctx, backup.Manual)
	if err != nil {
		return web.Backup{}, TranslateBackupError(err)
	}
	return toWebBackup(b), nil
}

// Compile-time interface checks
var (
	_ web.CurrencyService      = (*currencyAdapter)(nil)
	_ web.AccountService       = (*accountAdapter)(nil)
	_ web.CategoryService      = (*categoryAdapter)(nil)
	_ web.LedgerService        = (*ledgerAdapter)(nil)
	_ web.BackupService        = (*backupAdapter)(nil)
	_ web.ApplicationService   = (*applicationAdapter)(nil)
	_ web.PaymentMethodService = (*paymentMethodAdapter)(nil)
	_ web.AnalyticsService     = (*analyticsAdapter)(nil)
)

// New builds the presentation layer's services over a real database and backup service.
func New(db *sql.DB, backups *backup.Service, rates *fx.Service) *web.Services {
	ledgerService := ledger.NewService(db)
	settingsService := settings.NewService(db)
	currencyService := currency.NewService(db)
	valuationService := valuation.NewService(ledgerService, rates, settingsService, currencyService)
	savingsService := savings.NewService(db)
	applicationService := application.NewService(db, savingsService)

	return &web.Services{
		Currencies:  &currencyAdapter{svc: currencyService},
		Accounts:    &accountAdapter{svc: account.NewService(db)},
		Categories:  &categoryAdapter{svc: category.NewService(db)},
		Methods:     &paymentMethodAdapter{svc: paymentmethod.NewService(db)},
		Ledger:      &ledgerAdapter{svc: ledgerService, app: applicationService},
		Backups:     &backupAdapter{svc: backups},
		Application: &applicationAdapter{svc: applicationService},

		// M5. The rate service arrives from the composition root rather than
		// being built here, because it holds the provider -- which is
		// configuration, and which the caller may deliberately have switched
		// off.
		Rates:     &exchangeRateAdapter{svc: rates, now: time.Now},
		Settings:  &settingsAdapter{svc: settingsService},
		Valuation: &valuationAdapter{svc: valuationService},

		// M6. The dashboard reads the same valuation, so the headline figure
		// and the chart come from one engine. Its clock is the real one; a
		// test that needs a fixed date builds analytics.Service itself.
		Analytics: &analyticsAdapter{
			svc: analytics.NewService(valuationService, ledgerService, time.Now),
		},

		// M7.1. The savings policy and the suggestion of each eligible income.
		Savings: &savingsAdapter{svc: savingsService},

		// M8.1. Credit cards: every write through the application workflows.
		Cards: &cardAdapter{svc: creditcard.NewService(db), ledger: ledgerService, app: applicationService},
	}
}

// translateMovementError translates what recording a movement with a savings
// plan can fail with: the ledger's errors first, then the fund's.
func translateMovementError(err error) error {
	if translated := TranslateLedgerError(err); translated != err {
		return translated
	}
	return TranslateSavingsError(err)
}
