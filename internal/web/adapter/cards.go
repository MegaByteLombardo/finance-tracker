package adapter

import (
	"context"
	"errors"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// cardAdapter is M8.1. Every write goes through internal/application, which
// commits the ledger's rows with the card's and with the request key.
type cardAdapter struct {
	svc    *creditcard.Service
	ledger *ledger.Service
	app    *application.Service
}

var _ web.CardService = (*cardAdapter)(nil)

func (a *cardAdapter) List(ctx context.Context, includeInactive bool) ([]web.CreditCardView, error) {
	views, err := a.svc.List(ctx, includeInactive)
	if err != nil {
		return nil, TranslateCardError(err)
	}
	out := make([]web.CreditCardView, 0, len(views))
	for _, v := range views {
		out = append(out, toWebCreditCard(v))
	}
	return out, nil
}

func (a *cardAdapter) Get(ctx context.Context, accountID int64) (web.CreditCardView, error) {
	v, err := a.svc.Get(ctx, accountID)
	if err != nil {
		return web.CreditCardView{}, TranslateCardError(err)
	}
	return toWebCreditCard(v), nil
}

func (a *cardAdapter) Open(ctx context.Context, in web.OpenCreditCardInput) (int64, error) {
	result, err := a.app.OpenCreditCard(ctx, application.OpenCreditCardInput{
		RequestKey:      in.RequestKey,
		Name:            in.Name,
		Institution:     in.Institution,
		CurrencyCode:    in.CurrencyCode,
		Terms:           toDomainTerms(in.Terms),
		OpeningDebt:     in.OpeningDebt,
		OpeningDebtDate: in.OpeningDebtDate,
	})
	if err != nil {
		return 0, TranslateCardError(err)
	}
	return result.Account.ID, nil
}

func (a *cardAdapter) UpdateTerms(ctx context.Context, accountID int64, in web.CreditCardTermsInput) error {
	_, err := a.svc.UpdateTerms(ctx, accountID, toDomainTerms(in))
	return TranslateCardError(err)
}

func (a *cardAdapter) Movements(ctx context.Context, accountID int64, limit int) ([]web.CardMovement, error) {
	views, err := a.ledger.ListAccountTransactions(ctx, accountID, ledger.ListTransactionsInput{Limit: limit})
	if err != nil {
		return nil, TranslateCardError(err)
	}
	charges, err := a.svc.ChargesOfAccount(ctx, accountID)
	if err != nil {
		return nil, TranslateCardError(err)
	}
	out := make([]web.CardMovement, 0, len(views))
	for i := range views {
		out = append(out, toCardMovement(accountID, views[i], charges))
	}
	return out, nil
}

func (a *cardAdapter) RecordPurchase(ctx context.Context, requestKey string, in web.RecordExpenseInput) (web.TransactionView, error) {
	view, err := a.app.RecordCardPurchase(ctx, requestKey, toDomainRecordExpenseInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateCardError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *cardAdapter) RecordCharge(ctx context.Context, requestKey, kind string, in web.RecordExpenseInput) (web.TransactionView, error) {
	view, err := a.app.RecordCardCharge(ctx, requestKey, creditcard.ChargeKind(kind), toDomainRecordExpenseInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateCardError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *cardAdapter) UpdateCharge(ctx context.Context, id int64, kind string, in web.UpdateTransactionInput) (web.TransactionView, error) {
	view, err := a.app.UpdateCardCharge(ctx, id, creditcard.ChargeKind(kind), toDomainUpdateTransactionInput(in))
	if err != nil {
		return web.TransactionView{}, TranslateCardError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *cardAdapter) ChargeKind(ctx context.Context, id int64) (string, bool, error) {
	kind, ok, err := a.svc.ChargeKindOf(ctx, id)
	if err != nil {
		return "", false, TranslateCardError(err)
	}
	return string(kind), ok, nil
}

func (a *cardAdapter) RecordRefund(ctx context.Context, requestKey string, in web.RecordRefundInput) (web.TransactionView, error) {
	view, err := a.app.RecordRefund(ctx, requestKey, ledger.RecordRefundInput{
		AccountID: in.AccountID, CategoryID: in.CategoryID, Amount: in.Amount, PaymentMethodID: in.PaymentMethodID,
		Description: in.Description, EffectiveDate: in.EffectiveDate, OriginalTransactionID: in.OriginalTransactionID,
	})
	if err != nil {
		return web.TransactionView{}, TranslateCardError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *cardAdapter) UpdateRefund(ctx context.Context, id int64, in web.UpdateRefundInput) (web.TransactionView, error) {
	view, err := a.app.UpdateRefund(ctx, id, ledger.UpdateRefundInput{
		AccountID: in.AccountID, CategoryID: in.CategoryID, Amount: in.Amount, PaymentMethodID: in.PaymentMethodID,
		Description: in.Description, EffectiveDate: in.EffectiveDate, OriginalTransactionID: in.OriginalTransactionID,
		AllowCurrencyChange: in.AllowCurrencyChange,
	})
	if err != nil {
		return web.TransactionView{}, TranslateCardError(err)
	}
	return *toWebTransactionView(&view), nil
}

func toDomainTerms(in web.CreditCardTermsInput) creditcard.TermsInput {
	return creditcard.TermsInput{
		ProductName:            in.ProductName,
		CreditLimit:            in.CreditLimit,
		StatementDay:           in.StatementDay,
		DueDaysAfterStatement:  in.DueDaysAfterStatement,
		PaymentProcessingDays:  in.PaymentProcessingDays,
		Denomination:           creditcard.Denomination(in.Denomination),
		DenominationConfirmed:  in.DenominationConfirmed,
		DefaultPaymentMethodID: in.DefaultPaymentMethodID,
	}
}

// toWebCreditCard carries a card across. Every figure was derived by
// internal/creditcard; this only formats the integers it was given.
func toWebCreditCard(v creditcard.View) web.CreditCardView {
	c, p := v.Card, v.Position
	out := web.CreditCardView{
		AccountID:              v.Account.ID,
		Name:                   v.Account.Name,
		Institution:            v.Account.Institution,
		CurrencyCode:           v.Account.CurrencyCode,
		MinorUnit:              v.MinorUnit,
		IsActive:               v.Account.IsActive,
		ProductName:            c.ProductName,
		StatementDay:           c.StatementDay,
		DueDaysAfterStatement:  c.DueDaysAfterStatement,
		PaymentProcessingDays:  c.PaymentProcessingDays,
		Denomination:           string(c.Denomination),
		DenominationConfirmed:  c.DenominationConfirmed,
		DefaultPaymentMethodID: c.DefaultPaymentMethodID,
		BalanceMinor:           p.BalanceMinor,
		BalanceFormatted:       money.Format(p.BalanceMinor, v.MinorUnit),
		DebtMinor:              p.DebtMinor,
		DebtFormatted:          money.Format(p.DebtMinor, v.MinorUnit),
		CreditBalanceMinor:     p.CreditBalanceMinor,
		CreditBalanceFormatted: money.Format(p.CreditBalanceMinor, v.MinorUnit),
		HasLimit:               p.HasLimit,
		FutureMovementCount:    p.FutureMovementCount,
	}
	if c.CreditLimitMinor != nil {
		out.CreditLimitFormatted = money.Format(*c.CreditLimitMinor, v.MinorUnit)
	}
	if p.HasLimit {
		out.AvailableMinor = p.AvailableMinor
		out.AvailableFormatted = money.Format(p.AvailableMinor, v.MinorUnit)
	}
	return out
}

// toCardMovement says what a transaction is FOR THE CARD, from its type, its
// direction and whether it is a charge -- structurally, never from a category
// or a description (TD-044). Each case names the type it accepts (TD-045); a
// type added later is shown as what its sign says, never as a purchase.
func toCardMovement(cardID int64, v ledger.TransactionView, charges map[int64]creditcard.ChargeKind) web.CardMovement {
	m := web.CardMovement{Transaction: *toWebTransactionView(&v)}
	signed := func(amountFormatted string, credit bool) {
		m.Credit = credit
		if credit {
			m.AmountFormatted = "+" + amountFormatted
		} else {
			m.AmountFormatted = "-" + amountFormatted
		}
	}

	switch v.Type {
	case ledger.Expense:
		if kind, ok := charges[v.ID]; ok {
			m.Kind, m.ChargeKind = web.CardMovementCharge, string(kind)
			m.KindLabel = web.ChargeKindLabel(string(kind))
		} else {
			m.Kind, m.KindLabel = web.CardMovementPurchase, "Compra"
		}
		m.AmountFormatted, m.Credit = v.AmountFormatted, false
	case ledger.Refund:
		m.Kind, m.KindLabel = web.CardMovementRefund, "Devolución"
		signed(v.AmountFormatted, true)
	case ledger.Income:
		m.Kind, m.KindLabel = web.CardMovementOtherCredit, "Abono (p. ej. cashback)"
		signed(v.AmountFormatted, true)
	case ledger.OpeningBalance:
		m.Kind, m.KindLabel = web.CardMovementOpening, "Saldo inicial"
		m.AmountFormatted, m.Credit = v.AmountFormatted, v.AmountMinor > 0
	case ledger.Transfer:
		if v.Transfer != nil && v.Transfer.DestinationAccountID == cardID {
			m.Kind, m.KindLabel = web.CardMovementPayment, "Pago desde "+v.Transfer.SourceAccountName
			signed(v.Transfer.AmountFormatted, true)
		} else if v.Transfer != nil {
			m.Kind, m.KindLabel = web.CardMovementCashAdvance, "Avance de efectivo a "+v.Transfer.DestinationAccountName
			signed(v.Transfer.AmountFormatted, false)
		}
	case ledger.FXExchange:
		if v.Exchange != nil && v.Exchange.DestinationAccountID == cardID {
			m.Kind, m.KindLabel = web.CardMovementPayment, "Pago en "+v.Exchange.SourceCurrencyCode+" desde "+v.Exchange.SourceAccountName
			signed(v.Exchange.DestinationAmountFormatted, true)
		} else if v.Exchange != nil {
			m.Kind, m.KindLabel = web.CardMovementOtherOutflow, "Cambio hacia "+v.Exchange.DestinationAccountName
			signed(v.Exchange.SourceAmountFormatted, false)
		}
	}
	if m.Kind == "" {
		m.Kind, m.KindLabel = web.CardMovementOtherOutflow, string(v.Type)
		m.AmountFormatted, m.Credit = v.AmountFormatted, v.AmountMinor > 0
	}
	return m
}

// TranslateCardError maps everything a card workflow can fail with: the card's
// own errors, then the account's and the ledger's it composes.
func TranslateCardError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, creditcard.ErrNotFound):
		return translateError(web.ErrNotACreditCard, translateError(web.ErrNotFound, err))
	case errors.Is(err, creditcard.ErrNotACreditCard):
		return translateError(web.ErrNotACreditCard, err)
	case errors.Is(err, creditcard.ErrAccountNotFound):
		return translateError(web.ErrAccountNotFound, translateError(web.ErrNotFound, err))
	case errors.Is(err, creditcard.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, creditcard.ErrPaymentMethodNotFound):
		return translateError(web.ErrPaymentMethodNotFound, err)
	case errors.Is(err, creditcard.ErrPaymentMethodInactive):
		return translateError(web.ErrPaymentMethodInactive, err)
	case errors.Is(err, creditcard.ErrNotACharge):
		return translateError(web.ErrNotACharge, err)
	case errors.Is(err, creditcard.ErrChargeEditedAsCharge):
		return translateError(web.ErrChargeEditedAsCharge, err)
	case errors.Is(err, ledger.ErrRefundOriginal):
		return translateError(web.ErrRefundOriginal, err)
	case errors.Is(err, ledger.ErrRefundExceedsOriginal):
		return translateError(web.ErrRefundExceedsOriginal, err)
	case errors.Is(err, ledger.ErrInvalidRequestKey):
		return translateError(web.ErrInvalidRequestKey, err)
	case errors.Is(err, account.ErrNatureChange):
		return translateError(web.ErrNatureChange, err)
	}
	if translated := TranslateAccountError(err); translated != err {
		return translated
	}
	return TranslateLedgerError(err)
}
