package adapter

import (
	"context"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// M7.3: goals.

func (a *savingsAdapter) ListGoals(ctx context.Context, includeClosed bool) ([]web.SavingsGoal, error) {
	goals, err := a.svc.ListGoals(ctx, includeClosed)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsGoal, 0, len(goals))
	for _, g := range goals {
		out = append(out, toWebSavingsGoal(g))
	}
	return out, nil
}

func (a *savingsAdapter) GetGoal(ctx context.Context, id int64) (web.SavingsGoal, error) {
	g, err := a.svc.GetGoal(ctx, id)
	if err != nil {
		return web.SavingsGoal{}, TranslateSavingsError(err)
	}
	return toWebSavingsGoal(g), nil
}

func (a *savingsAdapter) CreateGoal(ctx context.Context, in web.SavingsGoalInput) (int64, error) {
	id, err := a.svc.CreateGoal(ctx, toDomainGoalInput(in))
	return id, TranslateSavingsError(err)
}

func (a *savingsAdapter) UpdateGoal(ctx context.Context, id int64, in web.SavingsGoalInput) error {
	return TranslateSavingsError(a.svc.UpdateGoal(ctx, id, toDomainGoalInput(in)))
}

func (a *savingsAdapter) PauseGoal(ctx context.Context, id int64) error {
	return TranslateSavingsError(a.svc.PauseGoal(ctx, id))
}

func (a *savingsAdapter) ResumeGoal(ctx context.Context, id int64) error {
	return TranslateSavingsError(a.svc.ResumeGoal(ctx, id))
}

func (a *savingsAdapter) CompleteGoal(ctx context.Context, id int64, in web.SavingsCloseGoalInput) error {
	return TranslateSavingsError(a.svc.CompleteGoal(ctx, id, savings.CloseGoalInput{RequestKey: in.RequestKey, Note: in.Note}))
}

func (a *savingsAdapter) CancelGoal(ctx context.Context, id int64, in web.SavingsCloseGoalInput) error {
	return TranslateSavingsError(a.svc.CancelGoal(ctx, id, savings.CloseGoalInput{RequestKey: in.RequestKey, Note: in.Note}))
}

func (a *savingsAdapter) Reallocate(ctx context.Context, in web.SavingsReallocateInput) (web.SavingsOperation, error) {
	op, err := a.svc.Reallocate(ctx, savings.ReallocateInput{
		RequestKey: in.RequestKey, AccountID: in.AccountID, FromGoalID: in.FromGoalID, ToGoalID: in.ToGoalID,
		Amount: in.Amount, Reason: in.Reason,
	})
	if err != nil {
		return web.SavingsOperation{}, TranslateSavingsError(err)
	}
	return toWebSavingsOperation(op), nil
}

func (a *savingsAdapter) SuggestDistribution(ctx context.Context, amount string) ([]web.SavingsDistributionLine, error) {
	minor, err := money.Parse(amount, usdMinorUnit)
	if err != nil {
		return nil, translateError(web.ErrInvalidInput, err)
	}
	lines, err := a.svc.SuggestDistribution(ctx, minor)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsDistributionLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, web.SavingsDistributionLine{
			GoalID: l.GoalID, GoalName: l.GoalName, Amount: usd(l.AmountMinor), Capped: l.Capped,
		})
	}
	return out, nil
}

func toDomainGoalInput(in web.SavingsGoalInput) savings.GoalInput {
	return savings.GoalInput{
		Name: in.Name, Type: savings.GoalType(in.Type), Target: in.Target,
		TargetDate: in.TargetDate, Distribution: in.Distribution,
	}
}

func toWebSavingsGoal(g savings.Goal) web.SavingsGoal {
	out := web.SavingsGoal{
		ID:                      g.ID,
		Name:                    g.Name,
		Type:                    string(g.Type),
		TypeLabel:               goalTypeLabel(g.Type),
		IsOneTime:               g.Type == savings.GoalOneTime,
		TargetFormatted:         usd(g.TargetUSDMinor),
		OriginalTargetFormatted: usd(g.OriginalTargetUSDMinor),
		TargetChanged:           g.TargetUSDMinor != g.OriginalTargetUSDMinor,
		TargetAmount:            usd(g.TargetUSDMinor),
		TargetDate:              g.TargetDate,
		DistributionFormatted:   money.Format(g.DistributionBP, 2),
		HasShare:                g.DistributionBP > 0,
		Status:                  string(g.Status),
		StatusLabel:             goalStatusLabel(g.Status),
		FundingLabel:            fundingLabel(g.Funding),
		FundingBadge:            fundingBadge(g.Funding),
		ReservedFormatted:       usd(g.ReservedMinor),
		RemainingFormatted:      usd(g.RemainingMinor),
		ClosedAt:                g.ClosedAt,
		ClosingNote:             g.ClosingNote,
		Closed:                  g.Status.Closed(),
		AcceptsMoney:            g.Status == savings.GoalActive,
		HasMoney:                g.ReservedMinor > 0,
		CanEdit:                 !g.Status.Closed(),
		CanPause:                g.Status == savings.GoalActive,
		CanResume:               g.Status == savings.GoalPaused,
		CanComplete:             !g.Status.Closed() && g.Type == savings.GoalOneTime,
		CanCancel:               !g.Status.Closed(),
	}
	if g.Progress.Defined {
		out.ProgressFormatted = money.Format(g.Progress.BasisPoints, 2)
		out.ProgressWidth = int(min(max(g.Progress.BasisPoints/100, 0), 100))
	}
	if g.SuggestedContributionMinor != nil {
		out.SuggestedContributionFormatted = usd(*g.SuggestedContributionMinor)
	}
	for _, a := range g.Accounts {
		out.Accounts = append(out.Accounts, web.SavingsGoalAccount{
			AccountID: a.AccountID, AccountName: a.AccountName, ReservedFormatted: usd(a.ReservedMinor),
		})
	}
	for _, t := range g.TargetRevisions {
		out.TargetRevisions = append(out.TargetRevisions, web.SavingsGoalTarget{TargetFormatted: usd(t.TargetUSDMinor), CreatedAt: t.CreatedAt})
	}
	for _, op := range g.Operations {
		out.Operations = append(out.Operations, toWebSavingsOperation(op))
	}
	addGoalCost(&out, g)
	return out
}

func goalTypeLabel(t savings.GoalType) string {
	switch t {
	case savings.GoalPermanent:
		return "Permanente"
	case savings.GoalOneTime:
		return "Puntual"
	default:
		return string(t)
	}
}

func goalStatusLabel(s savings.GoalStatus) string {
	switch s {
	case savings.GoalActive:
		return "Activa"
	case savings.GoalPaused:
		return "Pausada"
	case savings.GoalCompleted:
		return "Completada"
	case savings.GoalCancelled:
		return "Cancelada"
	default:
		return string(s)
	}
}

func fundingLabel(f savings.Funding) string {
	switch f {
	case savings.FundingNone:
		return "Sin fondos"
	case savings.FundingPartial:
		return "En progreso"
	case savings.FundingFull:
		return "Financiada"
	case savings.FundingOver:
		return "Sobrefinanciada"
	default:
		return string(f)
	}
}

func fundingBadge(f savings.Funding) string {
	switch f {
	case savings.FundingFull, savings.FundingOver:
		return "badge-income"
	case savings.FundingPartial:
		return "badge-info"
	default:
		return "badge-neutral"
	}
}
