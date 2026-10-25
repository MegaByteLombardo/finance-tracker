package adapter

import (
	"context"

	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// M7.4: the ledger meets the fund.

func (a *savingsAdapter) AccountImpact(ctx context.Context, accountID int64, amount string) (web.SavingsAccountImpact, error) {
	impact, err := a.svc.AccountImpact(ctx, accountID, amount)
	if err != nil {
		return web.SavingsAccountImpact{}, TranslateSavingsError(err)
	}
	out := web.SavingsAccountImpact{
		Applies:            impact.Applies,
		AccountName:        impact.AccountName,
		BalanceFormatted:   usd(impact.BalanceMinor),
		ReservedFormatted:  usd(impact.ReservedMinor),
		FreeFormatted:      usd(impact.FreeMinor),
		ShortfallFormatted: usd(impact.ShortfallMinor),
		HasShortfall:       impact.ShortfallMinor > 0,
		HasReservations:    len(impact.Slices) > 0,
	}
	for _, s := range impact.Slices {
		out.Slices = append(out.Slices, web.SavingsSlice{GoalID: s.GoalID, GoalName: s.GoalName, ReservedFormatted: usd(s.ReservedMinor)})
	}
	return out, nil
}

func (a *savingsAdapter) ListIssues(ctx context.Context) ([]web.SavingsIssue, error) {
	issues, err := a.svc.ListIssues(ctx)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsIssue, 0, len(issues))
	for _, i := range issues {
		w := web.SavingsIssue{
			Key: i.Key, Kind: string(i.Kind), KindLabel: issueKindLabel(i.Kind),
			AccountName: i.AccountName, IncomeTransactionID: i.IncomeTransactionID, TransactionID: i.TransactionID,
			AmountFormatted: usd(i.AmountMinor), Detail: i.Detail,
		}
		if i.Acknowledged != nil {
			w.Acknowledged, w.AckNote, w.AckAt = true, i.Acknowledged.Note, i.Acknowledged.At
		}
		out = append(out, w)
	}
	return out, nil
}

func (a *savingsAdapter) AcknowledgeIssue(ctx context.Context, key, note string) error {
	return TranslateSavingsError(a.svc.AcknowledgeIssue(ctx, key, note))
}

func (a *savingsAdapter) FundHistory(ctx context.Context) ([]web.SavingsFundMonth, error) {
	months, err := a.svc.FundHistory(ctx)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsFundMonth, 0, len(months))
	for _, m := range months {
		out = append(out, web.SavingsFundMonth{
			Month: m.Month, ContributionsFormatted: usd(m.ContributionsMinor), WithdrawalsFormatted: usd(m.WithdrawalsMinor),
			NetFormatted: usd(m.NetMinor), ClosingFormatted: usd(m.ClosingMinor),
		})
	}
	return out, nil
}

func issueKindLabel(k savings.IssueKind) string {
	switch k {
	case savings.IssueOverallocated:
		return "Sobreasignación"
	case savings.IssueOverConfirmed:
		return "Confirmado de más"
	case savings.IssueSpendMismatch:
		return "Gasto vinculado modificado"
	case savings.IssueArchivedBacking:
		return "Cuenta archivada con ahorro"
	default:
		return string(k)
	}
}

func toDomainUses(uses []web.SavingsUse) []savings.SavingsUse {
	out := make([]savings.SavingsUse, 0, len(uses))
	for _, u := range uses {
		out = append(out, savings.SavingsUse{GoalID: u.GoalID, Amount: u.Amount})
	}
	return out
}

func toDomainExpensePlan(p *web.SavingsExpensePlan) savings.ExpensePlan {
	if p == nil {
		return savings.ExpensePlan{}
	}
	return savings.ExpensePlan{RequestKey: p.RequestKey, Spend: toDomainUses(p.Spend), Release: toDomainUses(p.Release), Reason: p.Reason}
}

func toDomainTransferPlan(p *web.SavingsTransferPlan) savings.TransferPlan {
	if p == nil {
		return savings.TransferPlan{}
	}
	return savings.TransferPlan{RequestKey: p.RequestKey, Move: toDomainUses(p.Move)}
}

func addGoalCost(out *web.SavingsGoal, g savings.Goal) {
	for _, m := range g.Months {
		out.Months = append(out.Months, web.SavingsGoalMonth{
			Month: m.Month, InFormatted: usd(m.InMinor), OutFormatted: usd(m.OutMinor), ClosingFormatted: usd(m.ClosingMinor),
		})
	}
	for _, e := range g.LinkedExpenses {
		out.LinkedExpenses = append(out.LinkedExpenses, web.SavingsLinkedExpense{
			TransactionID: e.TransactionID, Date: e.Date, Description: e.Description,
			AmountFormatted: usd(e.AmountMinor), SpentFormatted: usd(e.SpentMinor),
		})
	}
	out.SpentFormatted = usd(g.SpentMinor)
	out.ActualCostFormatted = usd(g.ActualCostMinor)
	if g.DeviationMinor != nil {
		out.HasDeviation = true
		out.CostOverBudget = *g.DeviationMinor > 0
		out.DeviationFormatted = usd(*g.DeviationMinor)
		if *g.DeviationMinor > 0 {
			out.DeviationFormatted = "+" + out.DeviationFormatted
		}
	}
}
