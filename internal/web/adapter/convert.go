package adapter

import (
	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/backup"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/currency"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/paymentmethod"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// Currency conversions

func toWebCurrency(c currency.Currency) web.Currency {
	return web.Currency{
		Code:      c.Code,
		Name:      c.Name,
		Symbol:    c.Symbol,
		MinorUnit: c.MinorUnit,
		IsActive:  c.IsActive,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

func toWebCurrencies(cs []currency.Currency) []web.Currency {
	if cs == nil {
		return nil
	}
	res := make([]web.Currency, len(cs))
	for i, c := range cs {
		res[i] = toWebCurrency(c)
	}
	return res
}

// Account conversions

func toWebAccountType(t account.Type) web.AccountType {
	return web.AccountType(t)
}

func toDomainAccountType(t web.AccountType) account.Type {
	return account.Type(t)
}

func toWebAccountSubtype(s *account.Subtype) *web.AccountSubtype {
	if s == nil {
		return nil
	}
	st := web.AccountSubtype(*s)
	return &st
}

func toDomainAccountSubtype(s *web.AccountSubtype) *account.Subtype {
	if s == nil {
		return nil
	}
	st := account.Subtype(*s)
	return &st
}

func toWebAccount(a account.Account) web.Account {
	return web.Account{
		ID:           a.ID,
		Name:         a.Name,
		Type:         toWebAccountType(a.Type),
		Subtype:      toWebAccountSubtype(a.Subtype),
		CurrencyCode: a.CurrencyCode,
		Institution:  a.Institution,
		IsActive:     a.IsActive,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

func toWebAccounts(as []account.Account) []web.Account {
	if as == nil {
		return nil
	}
	res := make([]web.Account, len(as))
	for i, a := range as {
		res[i] = toWebAccount(a)
	}
	return res
}

func toDomainCreateAccountInput(in web.CreateAccountInput) account.CreateInput {
	return account.CreateInput{
		Name:         in.Name,
		Type:         toDomainAccountType(in.Type),
		Subtype:      toDomainAccountSubtype(in.Subtype),
		CurrencyCode: in.CurrencyCode,
		Institution:  in.Institution,
	}
}

func toDomainUpdateAccountMetadataInput(in web.UpdateAccountMetadataInput) account.UpdateMetadataInput {
	return account.UpdateMetadataInput{
		Name:        in.Name,
		Type:        toDomainAccountType(in.Type),
		Subtype:     toDomainAccountSubtype(in.Subtype),
		Institution: in.Institution,
	}
}

// Category conversions

func toWebCategoryType(t category.Type) web.CategoryType {
	return web.CategoryType(t)
}

func toDomainCategoryType(t web.CategoryType) category.Type {
	return category.Type(t)
}

func toWebCategoryTypePtr(t *category.Type) *web.CategoryType {
	if t == nil {
		return nil
	}
	ct := web.CategoryType(*t)
	return &ct
}

func toDomainCategoryTypePtr(t *web.CategoryType) *category.Type {
	if t == nil {
		return nil
	}
	ct := category.Type(*t)
	return &ct
}

func toWebCategory(c category.Category) web.Category {
	return web.Category{
		ID:        c.ID,
		Name:      c.Name,
		Type:      toWebCategoryType(c.Type),
		IsActive:  c.IsActive,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

func toWebCategories(cs []category.Category) []web.Category {
	if cs == nil {
		return nil
	}
	res := make([]web.Category, len(cs))
	for i, c := range cs {
		res[i] = toWebCategory(c)
	}
	return res
}

func toDomainCreateCategoryInput(in web.CreateCategoryInput) category.CreateInput {
	return category.CreateInput{
		Name: in.Name,
		Type: toDomainCategoryType(in.Type),
	}
}

func toDomainUpdateCategoryMetadataInput(in web.UpdateCategoryMetadataInput) category.UpdateMetadataInput {
	return category.UpdateMetadataInput{
		Name: in.Name,
	}
}

func toDomainListCategoriesInput(in web.ListCategoriesInput) category.ListInput {
	return category.ListInput{
		Type:            toDomainCategoryTypePtr(in.Type),
		IncludeInactive: in.IncludeInactive,
	}
}

// Ledger conversions

func toWebTransactionType(t ledger.TransactionType) web.TransactionType {
	return web.TransactionType(t)
}

// Payment method conversions
//
// A payment method is a row now, not an enum value, so a transaction carries an
// id and the listing resolves the name for display.

func toWebPaymentMethod(m paymentmethod.PaymentMethod) web.PaymentMethod {
	return web.PaymentMethod{
		ID:        m.ID,
		Name:      m.Name,
		IsActive:  m.IsActive,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func toWebPaymentMethods(ms []paymentmethod.PaymentMethod) []web.PaymentMethod {
	if ms == nil {
		return nil
	}
	res := make([]web.PaymentMethod, len(ms))
	for i, m := range ms {
		res[i] = toWebPaymentMethod(m)
	}
	return res
}

func toDomainListPaymentMethodsInput(in web.ListPaymentMethodsInput) paymentmethod.ListInput {
	return paymentmethod.ListInput{IncludeInactive: in.IncludeInactive}
}

func toDomainCreatePaymentMethodInput(in web.CreatePaymentMethodInput) paymentmethod.CreateInput {
	return paymentmethod.CreateInput{Name: in.Name}
}

func toDomainUpdatePaymentMethodMetadataInput(
	in web.UpdatePaymentMethodMetadataInput,
) paymentmethod.UpdateMetadataInput {
	return paymentmethod.UpdateMetadataInput{Name: in.Name}
}

func toWebTransactionView(v *ledger.TransactionView) *web.TransactionView {
	if v == nil {
		return nil
	}
	return &web.TransactionView{
		ID:                v.ID,
		Type:              toWebTransactionType(v.Type),
		EffectiveDate:     v.EffectiveDate,
		Description:       v.Description,
		PaymentMethodID:   v.PaymentMethodID,
		PaymentMethodName: v.PaymentMethodName,
		CategoryID:        v.CategoryID,
		CategoryName:      v.CategoryName,
		CategoryType:      toWebCategoryTypePtr(v.CategoryType),
		Transfer:          toWebTransferDetail(v.Transfer),
		Exchange:          toWebExchangeDetail(v.Exchange),
		MovementID:        v.MovementID,
		AccountID:         v.AccountID,
		AccountName:       v.AccountName,
		AccountType:       toWebAccountType(v.AccountType),
		AccountSubtype:    toWebAccountSubtype(v.AccountSubtype),
		Institution:       v.Institution,
		CurrencyCode:      v.CurrencyCode,
		CurrencySymbol:    v.CurrencySymbol,
		MinorUnit:         v.MinorUnit,
		AmountMinor:       v.AmountMinor,
		AmountFormatted:   v.AmountFormatted,
		CreatedAt:         v.CreatedAt,
	}
}

func toWebTransactionViews(vs []ledger.TransactionView) []web.TransactionView {
	if vs == nil {
		return nil
	}
	res := make([]web.TransactionView, len(vs))
	for i := range vs {
		res[i] = *toWebTransactionView(&vs[i])
	}
	return res
}

func toWebAccountBalance(b ledger.AccountBalance) web.AccountBalance {
	return web.AccountBalance{
		AccountID:        b.AccountID,
		AccountName:      b.AccountName,
		AccountType:      toWebAccountType(b.AccountType),
		AccountSubtype:   toWebAccountSubtype(b.AccountSubtype),
		Institution:      b.Institution,
		IsActive:         b.IsActive,
		CurrencyCode:     b.CurrencyCode,
		CurrencySymbol:   b.CurrencySymbol,
		MinorUnit:        b.MinorUnit,
		BalanceMinor:     b.BalanceMinor,
		BalanceFormatted: b.BalanceFormatted,

		FutureMovementCount: b.FutureMovementCount,
	}
}

func toWebAccountBalances(bs []ledger.AccountBalance) []web.AccountBalance {
	if bs == nil {
		return nil
	}
	res := make([]web.AccountBalance, len(bs))
	for i, b := range bs {
		res[i] = toWebAccountBalance(b)
	}
	return res
}

func toWebCurrencyTotal(t ledger.CurrencyTotal) web.CurrencyTotal {
	return web.CurrencyTotal{
		CurrencyCode:   t.CurrencyCode,
		CurrencyName:   t.CurrencyName,
		CurrencySymbol: t.CurrencySymbol,
		MinorUnit:      t.MinorUnit,
		AccountCount:   t.AccountCount,
		TotalMinor:     t.TotalMinor,
		TotalFormatted: t.TotalFormatted,
	}
}

func toWebCurrencyTotals(ts []ledger.CurrencyTotal) []web.CurrencyTotal {
	if ts == nil {
		return nil
	}
	res := make([]web.CurrencyTotal, len(ts))
	for i, t := range ts {
		res[i] = toWebCurrencyTotal(t)
	}
	return res
}

func toDomainRecordIncomeInput(in web.RecordIncomeInput) ledger.RecordIncomeInput {
	return ledger.RecordIncomeInput{
		AccountID:       in.AccountID,
		CategoryID:      in.CategoryID,
		Amount:          in.Amount,
		PaymentMethodID: in.PaymentMethodID,
		Description:     in.Description,
		EffectiveDate:   in.EffectiveDate,
	}
}

func toDomainRecordExpenseInput(in web.RecordExpenseInput) ledger.RecordExpenseInput {
	return ledger.RecordExpenseInput{
		AccountID:       in.AccountID,
		CategoryID:      in.CategoryID,
		Amount:          in.Amount,
		PaymentMethodID: in.PaymentMethodID,
		Description:     in.Description,
		EffectiveDate:   in.EffectiveDate,
	}
}

func toDomainRecordOpeningBalanceInput(in web.RecordOpeningBalanceInput) ledger.RecordOpeningBalanceInput {
	return ledger.RecordOpeningBalanceInput{
		AccountID:     in.AccountID,
		Amount:        in.Amount,
		EffectiveDate: in.EffectiveDate,
	}
}

func toDomainListTransactionsInput(in web.ListTransactionsInput) ledger.ListTransactionsInput {
	return ledger.ListTransactionsInput{
		Limit: in.Limit,
	}
}

// Application conversions

func toDomainOpenAccountInput(in web.OpenAccountInput) application.OpenAccountInput {
	return application.OpenAccountInput{
		Account:            toDomainCreateAccountInput(in.Account),
		OpeningBalance:     in.OpeningBalance,
		OpeningBalanceDate: in.OpeningBalanceDate,
	}
}

func toWebOpenAccountResult(res application.OpenAccountResult) web.OpenAccountResult {
	return web.OpenAccountResult{
		Account:        toWebAccount(res.Account),
		OpeningBalance: toWebTransactionView(res.OpeningBalance),
	}
}

func toDomainUpdateTransactionInput(in web.UpdateTransactionInput) ledger.UpdateTransactionInput {
	return ledger.UpdateTransactionInput{
		AccountID:           in.AccountID,
		Type:                toDomainTransactionType(in.Type),
		CategoryID:          in.CategoryID,
		PaymentMethodID:     in.PaymentMethodID,
		Amount:              in.Amount,
		Description:         in.Description,
		EffectiveDate:       in.EffectiveDate,
		AllowCurrencyChange: in.AllowCurrencyChange,
	}
}

func toDomainTransactionType(t web.TransactionType) ledger.TransactionType {
	return ledger.TransactionType(t)
}

// Transfer conversions
//
// The detail is nil for everything that is not a transfer, and that nil is
// meaningful: it is how a template knows this row is not one.

func toWebTransferDetail(d *ledger.TransferDetail) *web.TransferDetail {
	if d == nil {
		return nil
	}
	return &web.TransferDetail{
		SourceMovementID:       d.SourceMovementID,
		SourceAccountID:        d.SourceAccountID,
		SourceAccountName:      d.SourceAccountName,
		DestinationMovementID:  d.DestinationMovementID,
		DestinationAccountID:   d.DestinationAccountID,
		DestinationAccountName: d.DestinationAccountName,
		AmountMinor:            d.AmountMinor,
		AmountFormatted:        d.AmountFormatted,
	}
}

func toDomainRecordTransferInput(in web.RecordTransferInput) ledger.RecordTransferInput {
	return ledger.RecordTransferInput{
		SourceAccountID:      in.SourceAccountID,
		DestinationAccountID: in.DestinationAccountID,
		Amount:               in.Amount,
		Description:          in.Description,
		EffectiveDate:        in.EffectiveDate,
	}
}

func toDomainUpdateTransferInput(in web.UpdateTransferInput) ledger.UpdateTransferInput {
	return ledger.UpdateTransferInput{
		SourceAccountID:      in.SourceAccountID,
		DestinationAccountID: in.DestinationAccountID,
		Amount:               in.Amount,
		Description:          in.Description,
		EffectiveDate:        in.EffectiveDate,
		AllowCurrencyChange:  in.AllowCurrencyChange,
	}
}

// Exchange conversions
//
// The detail is nil for everything that is not an FX_EXCHANGE, and that nil is
// meaningful: it is how a template knows this row is not one.
//
// The rate crosses as a STRING. money.Decimal stops here: the presentation layer
// displays a rate and never computes with one, so handing it an arithmetic type
// would be handing it an invitation.

func toWebExchangeDetail(d *ledger.ExchangeDetail) *web.ExchangeDetail {
	if d == nil {
		return nil
	}
	return &web.ExchangeDetail{
		SourceMovementID:     d.SourceMovementID,
		SourceAccountID:      d.SourceAccountID,
		SourceAccountName:    d.SourceAccountName,
		SourceCurrencyCode:   d.SourceCurrencyCode,
		SourceCurrencySymbol: d.SourceCurrencySymbol,
		SourceMinorUnit:      d.SourceMinorUnit,

		SourceAmountMinor:     d.SourceAmountMinor,
		SourceAmountFormatted: d.SourceAmountFormatted,

		DestinationMovementID:     d.DestinationMovementID,
		DestinationAccountID:      d.DestinationAccountID,
		DestinationAccountName:    d.DestinationAccountName,
		DestinationCurrencyCode:   d.DestinationCurrencyCode,
		DestinationCurrencySymbol: d.DestinationCurrencySymbol,
		DestinationMinorUnit:      d.DestinationMinorUnit,

		DestinationAmountMinor:     d.DestinationAmountMinor,
		DestinationAmountFormatted: d.DestinationAmountFormatted,

		ExecutionRate: web.ExecutionRate{
			BaseCurrencyCode:  d.ExecutionRate.BaseCurrencyCode,
			QuoteCurrencyCode: d.ExecutionRate.QuoteCurrencyCode,
			RateFormatted:     d.ExecutionRate.RateFormatted,
			Exact:             d.ExecutionRate.Exact,
		},
	}
}

func toDomainRecordExchangeInput(in web.RecordExchangeInput) ledger.RecordExchangeInput {
	return ledger.RecordExchangeInput{
		SourceAccountID:      in.SourceAccountID,
		SourceAmount:         in.SourceAmount,
		DestinationAccountID: in.DestinationAccountID,
		DestinationAmount:    in.DestinationAmount,
		Description:          in.Description,
		EffectiveDate:        in.EffectiveDate,
	}
}

func toDomainUpdateExchangeInput(in web.UpdateExchangeInput) ledger.UpdateExchangeInput {
	return ledger.UpdateExchangeInput{
		SourceAccountID:      in.SourceAccountID,
		SourceAmount:         in.SourceAmount,
		DestinationAccountID: in.DestinationAccountID,
		DestinationAmount:    in.DestinationAmount,
		Description:          in.Description,
		EffectiveDate:        in.EffectiveDate,
	}
}

func toDomainUpdateOpeningBalanceInput(in web.UpdateOpeningBalanceInput) ledger.UpdateOpeningBalanceInput {
	return ledger.UpdateOpeningBalanceInput{
		Amount:        in.Amount,
		EffectiveDate: in.EffectiveDate,
	}
}

// Backup conversions

func toWebBackup(b backup.Backup) web.Backup {
	return web.Backup{
		Name:      b.Name,
		Reason:    string(b.Reason),
		CreatedAt: b.CreatedAt,
		SizeBytes: b.SizeBytes,
	}
}

func toWebBackups(bs []backup.Backup) []web.Backup {
	if bs == nil {
		return nil
	}
	res := make([]web.Backup, len(bs))
	for i, b := range bs {
		res[i] = toWebBackup(b)
	}
	return res
}
