package adapter

import (
	"context"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/creditcard"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// M8.2: statements, obligations and payments. Every figure arrives derived by
// internal/creditcard; this file only formats the integers it was given and
// names statuses and targets for a screen.

func (a *cardAdapter) Standing(ctx context.Context, accountID int64) (web.CardStandingView, error) {
	v, err := a.svc.Standing(ctx, accountID)
	if err != nil {
		return web.CardStandingView{}, TranslateCardError(err)
	}
	return toWebStanding(v), nil
}

func (a *cardAdapter) RecordPayment(ctx context.Context, in web.RecordCardPaymentInput) (web.TransactionView, error) {
	view, err := a.app.RecordCardPayment(ctx, application.RecordCardPaymentInput{
		RequestKey: in.RequestKey, CardAccountID: in.CardAccountID, SourceAccountID: in.SourceAccountID,
		Amount: in.Amount, SourceAmount: in.SourceAmount, Description: in.Description,
		EffectiveDate: in.EffectiveDate, Savings: toDomainExpensePlan(in.Savings),
	})
	if err != nil {
		if translated := TranslateCardError(err); translated != err {
			return web.TransactionView{}, translated
		}
		return web.TransactionView{}, TranslateSavingsError(err)
	}
	return *toWebTransactionView(&view), nil
}

func (a *cardAdapter) NextStatementDates(ctx context.Context, accountID int64) (string, string, error) {
	start, closing, err := a.svc.NextStatementDates(ctx, accountID)
	if err != nil {
		return "", "", TranslateCardError(err)
	}
	return database.FormatDate(start), database.FormatDate(closing), nil
}

func (a *cardAdapter) Statement(ctx context.Context, accountID, statementID int64) (web.CardStatementInput, error) {
	view, err := a.svc.Get(ctx, accountID)
	if err != nil {
		return web.CardStatementInput{}, TranslateCardError(err)
	}
	s, err := a.svc.GetStatement(ctx, accountID, statementID)
	if err != nil {
		return web.CardStatementInput{}, TranslateCardError(err)
	}
	return toWebStatementInput(s, view.MinorUnit), nil
}

func (a *cardAdapter) RecordStatement(ctx context.Context, accountID int64, in web.CardStatementInput) (int64, error) {
	s, err := a.svc.RecordStatement(ctx, accountID, toDomainStatementInput(in))
	if err != nil {
		return 0, TranslateCardError(err)
	}
	return s.ID, nil
}

func (a *cardAdapter) UpdateStatement(ctx context.Context, accountID, statementID int64, in web.CardStatementInput) error {
	_, err := a.svc.UpdateStatement(ctx, accountID, statementID, toDomainStatementInput(in))
	return TranslateCardError(err)
}

func (a *cardAdapter) Reconcile(ctx context.Context, accountID, statementID int64) (web.CardReconciliationView, error) {
	v, r, err := a.svc.Reconcile(ctx, accountID, statementID)
	if err != nil {
		return web.CardReconciliationView{}, TranslateCardError(err)
	}
	unit := v.MinorUnit
	out := web.CardReconciliationView{
		Card:                toWebCreditCard(v.View),
		Cycle:               toWebCycle(r.Cycle, unit),
		Statement:           toWebStatementInput(r.Statement, unit),
		BankOwedFormatted:   money.Format(r.BankOwedMinor, unit),
		LedgerOwedFormatted: money.Format(r.LedgerOwedMinor, unit),
		DifferenceFormatted: money.Format(r.DifferenceMinor, unit),
		Reconciled:          r.DifferenceMinor == 0,
		Previous:            toWebComponent(r.Previous, unit),
		Payments:            toWebComponent(r.Payments, unit),
		Debits:              toWebComponent(r.Debits, unit),
		PastDue:             toWebComponent(r.PastDue, unit),
		Warnings:            toWebWarnings(v.Standing.Warnings, unit),
	}
	if r.Unexplained != nil {
		out.Unexplained = money.Format(*r.Unexplained, unit)
	}
	for _, p := range r.Members {
		out.Members = append(out.Members, toWebPlaced(p, r.Statement.ClosingDate, unit))
	}
	for _, p := range r.Candidates {
		out.Candidates = append(out.Candidates, toWebPlaced(p, r.Statement.ClosingDate, unit))
	}
	return out, nil
}

func (a *cardAdapter) AssertMembership(ctx context.Context, accountID, transactionID int64, statementID *int64) error {
	return TranslateCardError(a.svc.AssertMembership(ctx, accountID, transactionID, statementID))
}

func toDomainStatementInput(in web.CardStatementInput) creditcard.StatementInput {
	return creditcard.StatementInput{
		PeriodStart: in.PeriodStart, ClosingDate: in.ClosingDate, DueDate: in.DueDate,
		ClosingOwed: in.ClosingOwed, MinimumPayment: in.MinimumPayment, PreviousOwed: in.PreviousOwed,
		Payments: in.Payments, Debits: in.Debits, PastDue: in.PastDue,
	}
}

// toWebStatementInput puts a statement back the way it was typed: balances
// owed positive.
func toWebStatementInput(s creditcard.Statement, unit int) web.CardStatementInput {
	in := web.CardStatementInput{
		PeriodStart: database.FormatDate(s.PeriodStart),
		ClosingDate: database.FormatDate(s.ClosingDate),
		ClosingOwed: money.Format(s.OwedMinor(), unit),
	}
	if s.DueDate != nil {
		in.DueDate = database.FormatDate(*s.DueDate)
	}
	optional := func(v *int64, negate bool) string {
		if v == nil {
			return ""
		}
		if negate {
			return money.Format(-*v, unit)
		}
		return money.Format(*v, unit)
	}
	in.MinimumPayment = optional(s.MinimumPaymentMinor, false)
	in.PreviousOwed = optional(s.PreviousBalanceMinor, true)
	in.Payments = optional(s.PaymentsMinor, false)
	in.Debits = optional(s.DebitsMinor, false)
	in.PastDue = optional(s.PastDueMinor, false)
	return in
}

func toWebStanding(v creditcard.StandingView) web.CardStandingView {
	st, unit := v.Standing, v.MinorUnit
	out := web.CardStandingView{
		Card:                         toWebCreditCard(v.View),
		Open:                         toWebCycle(st.Open(), unit),
		RefundPolicy:                 string(st.RefundPolicy),
		BilledPendingMinor:           st.BilledPendingMinor,
		BilledPendingFormatted:       money.Format(st.BilledPendingMinor, unit),
		BilledNetOfRefundsFormatted:  money.Format(st.BilledNetOfRefundsMinor, unit),
		HasRefundsAfterClosing:       st.RefundsAfterClosingMinor > 0,
		RefundsAfterClosingFormatted: money.Format(st.RefundsAfterClosingMinor, unit),
		PastDueMinor:                 st.PastDueMinor,
		PastDueFormatted:             money.Format(st.PastDueMinor, unit),
		PastDueCycles:                st.PastDueCycles,
		PostClosingDebitsFormatted:   money.Format(st.PostClosingDebitsMinor, unit),
		PrepaidMinor:                 st.PrepaidMinor,
		PrepaidFormatted:             money.Format(st.PrepaidMinor, unit),
		Warnings:                     toWebWarnings(st.Warnings, unit),
	}
	if current, ok := st.Current(); ok {
		out.HasCurrent, out.Current = true, toWebCycle(current, unit)
	}
	if st.MinimumPendingMinor != nil {
		out.MinimumKnown = true
		out.MinimumPendingMinor = *st.MinimumPendingMinor
		out.MinimumPendingFormatted = money.Format(*st.MinimumPendingMinor, unit)
	}
	for i := len(st.Cycles) - 1; i >= 0; i-- {
		out.Cycles = append(out.Cycles, toWebCycle(st.Cycles[i], unit))
	}
	for i := len(st.Payments) - 1; i >= 0; i-- {
		out.Payments = append(out.Payments, toWebAllocation(st.Payments[i], unit))
	}
	return out
}

func toWebCycle(c creditcard.Cycle, unit int) web.CardCycleView {
	out := web.CardCycleView{
		Registered:          c.Registered(),
		PeriodStart:         c.PeriodStart,
		ClosingDate:         c.ClosingDate,
		DueDate:             c.DueDate,
		DueEstimated:        c.DueEstimated,
		OwedFormatted:       money.Format(c.OwedMinor, unit),
		LedgerOwedFormatted: money.Format(c.LedgerOwedMinor, unit),
		BilledFormatted:     money.Format(c.BilledMinor, unit),
		PaidAfterFormatted:  money.Format(c.PaidAfterMinor, unit),
		DebitsFormatted:     money.Format(c.DebitsMinor, unit),
		PaymentsFormatted:   money.Format(c.PaymentsMinor, unit),
		CreditsFormatted:    money.Format(c.CreditsMinor, unit),
		MovementCount:       c.MovementCount,
		Status:              string(c.Status),
		StatusLabel:         web.CardCycleStatusLabel(string(c.Status)),
	}
	if c.Statement != nil {
		out.StatementID = c.Statement.ID
		out.Reconciled = c.OwedMinor == c.LedgerOwedMinor
		out.DifferenceFormatted = money.Format(c.OwedMinor-c.LedgerOwedMinor, unit)
	}
	if c.MinimumMinor != nil {
		out.MinimumFormatted = money.Format(*c.MinimumMinor, unit)
	}
	if c.MissedMinimumMinor != nil {
		out.MissedMinimumFormatted = money.Format(*c.MissedMinimumMinor, unit)
	}
	if c.UnpaidAtDueMinor != nil {
		out.UnpaidAtDueFormatted = money.Format(*c.UnpaidAtDueMinor, unit)
	}
	return out
}

func toWebAllocation(a creditcard.PaymentAllocation, unit int) web.CardPaymentAllocationView {
	out := web.CardPaymentAllocationView{
		TransactionID:       a.TransactionID,
		Date:                a.Date,
		AmountFormatted:     money.Format(a.AmountMinor, unit),
		AgainstClosing:      a.Against,
		Clamped:             a.Clamped,
		PaidBeforeFormatted: money.Format(a.PaidBeforeMinor, unit),
		OverdueFormatted:    money.Format(a.Thresholds.OverdueMinor, unit),
		MinimumFormatted:    money.Format(a.Thresholds.MinimumMinor, unit),
		BilledFormatted:     money.Format(a.Thresholds.BilledMinor, unit),
		UnbilledFormatted:   money.Format(a.Thresholds.UnbilledMinor, unit),
	}
	for _, l := range a.Lines {
		out.Lines = append(out.Lines, web.CardAllocationLineView{
			Target: string(l.Target), Label: web.CardAllocationLabel(string(l.Target)),
			AmountFormatted: money.Format(l.AmountMinor, unit), Cycles: l.Cycles,
		})
	}
	return out
}

func toWebComponent(c *creditcard.Component, unit int) *web.CardReconComponent {
	if c == nil {
		return nil
	}
	return &web.CardReconComponent{
		BankFormatted:       money.Format(c.BankMinor, unit),
		LedgerFormatted:     money.Format(c.LedgerMinor, unit),
		DifferenceFormatted: money.Format(c.DifferenceMinor, unit),
		Matches:             c.DifferenceMinor == 0,
	}
}

// toWebPlaced shows a movement beside the statement closing on closing: it
// can be asserted into it only if it happened on or before that day.
func toWebPlaced(p creditcard.Placed, closing time.Time, unit int) web.CardPlacedView {
	out := web.CardPlacedView{
		TransactionID: p.TransactionID,
		Date:          p.Date,
		RoleLabel:     cardRoleLabel(p.Role),
		Credit:        p.AmountMinor > 0,
		ClosingDate:   p.ClosingDate,
		Asserted:      p.Asserted,
		CanAssert:     !p.Date.After(closing),
	}
	out.AmountFormatted = money.Format(p.AmountMinor, unit)
	if p.AmountMinor > 0 {
		out.AmountFormatted = "+" + out.AmountFormatted
	}
	if p.Description != nil {
		out.Description = *p.Description
	}
	return out
}

// cardRoleLabel names what a movement is to the card's obligations.
func cardRoleLabel(role creditcard.Role) string {
	switch role {
	case creditcard.RolePayment:
		return "Pago"
	case creditcard.RoleRefund:
		return "Devolución"
	case creditcard.RoleDebit:
		return "Consumo o cargo"
	case creditcard.RoleCredit:
		return "Abono"
	default:
		return string(role)
	}
}

func toWebWarnings(ws []creditcard.Warning, _ int) []web.CardWarningView {
	out := make([]web.CardWarningView, 0, len(ws))
	for _, w := range ws {
		v := web.CardWarningView{Kind: string(w.Kind), TransactionID: w.TransactionID}
		switch w.Kind {
		case creditcard.WarningAssertionBeforeMovement:
			v.Text = fmt.Sprintf("El movimiento #%d está asignado a un estado de cuenta que cerró antes de su fecha (se corrigió la fecha después). Se usa la regla del calendario hasta que lo reasignes.", w.TransactionID)
		case creditcard.WarningAssertionOrphan:
			v.Text = fmt.Sprintf("Una asignación nombra la transacción #%d, que ya no mueve esta tarjeta. No se tiene en cuenta.", w.TransactionID)
		case creditcard.WarningThresholdsClamped:
			v.Text = fmt.Sprintf("Las cifras del estado de cuenta del %s no están en el orden esperado (el mínimo es menor que lo vencido o mayor que lo facturado). El pago #%d se ubicó acotándolas: revisa la conciliación.",
				database.FormatDate(*w.ClosingDate), w.TransactionID)
		default:
			v.Text = string(w.Kind)
		}
		out = append(out, v)
	}
	return out
}
