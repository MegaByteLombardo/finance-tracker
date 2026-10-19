package adapter

import (
	"context"
	"errors"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
	"github.com/MegaByteLombardo/finance-tracker/internal/web"
)

// savingsAdapter is M7.1. It formats every amount and decides every action a
// screen may offer, so templates only print.
type savingsAdapter struct {
	svc *savings.Service
}

// usdMinorUnit is the scale of the savings currency. USD has two decimals, and
// migration 00002 seeds it so; formatting needs no query for it.
const usdMinorUnit = 2

func (a *savingsAdapter) Overview(ctx context.Context) (web.SavingsOverview, error) {
	o, err := a.svc.Overview(ctx)
	if err != nil {
		return web.SavingsOverview{}, TranslateSavingsError(err)
	}
	out := web.SavingsOverview{
		PeriodFrom:              o.From,
		PeriodTo:                o.To,
		Policy:                  toWebSavingsPolicy(o.Policy),
		EligibleIncomeFormatted: money.Format(o.EligibleIncomeUSDMinor, usdMinorUnit),
		SuggestedFormatted:      money.Format(o.SuggestedUSDMinor, usdMinorUnit),
		OpenFormatted:           money.Format(o.OpenUSDMinor, usdMinorUnit),
		DismissedFormatted:      money.Format(o.DismissedUSDMinor, usdMinorUnit),
		OpenCount:               o.OpenCount,
		DismissedCount:          o.DismissedCount,
		PendingValuationCount:   o.PendingValuationCount,
		RateChangedCount:        o.RateChangedCount,
		UnsyncedCount:           o.UnsyncedCount,
		IssueCount:              o.IssueCount,
		IssueTotal:              o.IssueTotal,

		ConfirmedFormatted:          usd(o.ConfirmedUSDMinor),
		OverConfirmedFormatted:      usd(o.OverConfirmedUSDMinor),
		OverConfirmed:               o.OverConfirmedUSDMinor > 0,
		GrossContributionsFormatted: usd(o.GrossContributionsMinor),
		WithdrawalsFormatted:        usd(o.WithdrawalsMinor),
		NetChangeFormatted:          usd(o.NetChangeMinor),
		Fund:                        toWebSavingsFund(o.Fund),
	}
	if o.ConfirmationProgress.Defined {
		out.ProgressFormatted = money.Format(o.ConfirmationProgress.BasisPoints, 2)
	}
	for _, p := range o.PendingByCurrency {
		out.PendingByCurrency = append(out.PendingByCurrency, web.SavingsCurrencyAmount{
			CurrencyCode:    p.CurrencyCode,
			AmountFormatted: money.Format(p.AmountMinor, p.MinorUnit),
		})
	}
	return out, nil
}

func (a *savingsAdapter) GetPolicy(ctx context.Context) (web.SavingsPolicy, error) {
	p, err := a.svc.GetPolicy(ctx)
	if err != nil {
		return web.SavingsPolicy{}, TranslateSavingsError(err)
	}
	return toWebSavingsPolicy(p), nil
}

func (a *savingsAdapter) SetPolicy(ctx context.Context, in web.SetSavingsPolicyInput) error {
	return TranslateSavingsError(a.svc.SetPolicy(ctx, savings.SetPolicyInput{
		Rate:          in.Rate,
		EffectiveFrom: in.EffectiveFrom,
	}))
}

func (a *savingsAdapter) ListCategoryEligibility(ctx context.Context) ([]web.SavingsCategoryEligibility, error) {
	list, err := a.svc.ListCategoryEligibility(ctx)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsCategoryEligibility, 0, len(list))
	for _, c := range list {
		out = append(out, web.SavingsCategoryEligibility{
			CategoryID: c.CategoryID,
			Name:       c.Name,
			IsActive:   c.IsActive,
			Eligible:   c.Eligible,
			Explicit:   c.Explicit,
		})
	}
	return out, nil
}

func (a *savingsAdapter) SetCategoryEligibility(ctx context.Context, categoryID int64, eligible bool) error {
	return TranslateSavingsError(a.svc.SetCategoryEligibility(ctx, categoryID, eligible))
}

func (a *savingsAdapter) SetIncomeOverride(ctx context.Context, transactionID int64, eligible bool, reason string) error {
	return TranslateSavingsError(a.svc.SetIncomeOverride(ctx, transactionID, eligible, reason))
}

func (a *savingsAdapter) ListSuggestions(ctx context.Context) ([]web.SavingsSuggestion, error) {
	list, err := a.svc.ListSuggestions(ctx)
	if err != nil {
		return nil, TranslateSavingsError(err)
	}
	out := make([]web.SavingsSuggestion, 0, len(list))
	for _, s := range list {
		out = append(out, toWebSavingsSuggestion(s))
	}
	return out, nil
}

func (a *savingsAdapter) IncomeDetail(ctx context.Context, transactionID int64) (web.SavingsIncomeDetail, error) {
	d, err := a.svc.IncomeDetail(ctx, transactionID)
	if err != nil {
		return web.SavingsIncomeDetail{}, TranslateSavingsError(err)
	}
	out := web.SavingsIncomeDetail{
		TransactionID:   d.TransactionID,
		Description:     d.Description,
		Date:            d.Date,
		AccountName:     d.AccountName,
		CategoryName:    d.CategoryName,
		CurrencyCode:    d.CurrencyCode,
		AmountFormatted: money.Format(d.AmountMinor, d.MinorUnit),
		Eligible:        d.Eligible,
		Reason:          d.Reason,
		Revisions:       make([]web.SavingsSuggestion, 0, len(d.Revisions)),
	}
	if d.Override != nil {
		out.Override = &web.SavingsIncomeOverride{
			Eligible:  d.Override.Eligible,
			Reason:    d.Override.Reason,
			UpdatedAt: d.Override.UpdatedAt,
		}
	}
	for _, r := range d.Revisions {
		out.Revisions = append(out.Revisions, toWebSavingsSuggestion(r))
	}
	return out, nil
}

func (a *savingsAdapter) ValueSuggestion(ctx context.Context, transactionID int64) error {
	return TranslateSavingsError(a.svc.ValueSuggestion(ctx, transactionID))
}

func (a *savingsAdapter) ValuePending(ctx context.Context) (int, error) {
	n, err := a.svc.ValuePending(ctx)
	return n, TranslateSavingsError(err)
}

func (a *savingsAdapter) RevalueSuggestion(ctx context.Context, transactionID int64) error {
	return TranslateSavingsError(a.svc.RevalueSuggestion(ctx, transactionID))
}

func (a *savingsAdapter) DismissSuggestion(ctx context.Context, transactionID int64, reason string) error {
	return TranslateSavingsError(a.svc.DismissSuggestion(ctx, transactionID, reason))
}

func (a *savingsAdapter) PlanSync(ctx context.Context) (web.SavingsSyncPlan, error) {
	plan, err := a.svc.PlanSync(ctx)
	if err != nil {
		return web.SavingsSyncPlan{}, TranslateSavingsError(err)
	}
	out := web.SavingsSyncPlan{Items: make([]web.SavingsSyncItem, 0, len(plan.Items))}
	for _, item := range plan.Items {
		w := web.SavingsSyncItem{
			Action:          string(item.Action),
			ActionLabel:     syncActionLabel(item.Action),
			TransactionID:   item.TransactionID,
			Date:            item.Date,
			Description:     item.Description,
			AccountName:     item.AccountName,
			CategoryName:    item.CategoryName,
			CurrencyCode:    item.CurrencyCode,
			AmountFormatted: money.Format(item.AmountMinor, item.MinorUnit),
			Reason:          item.Reason,
		}
		if item.Action != savings.SyncInvalidate {
			w.RateFormatted = money.Format(item.RateBP, 2)
		}
		out.Items = append(out.Items, w)
	}
	return out, nil
}

func (a *savingsAdapter) ApplySync(ctx context.Context, transactionIDs []int64) (web.SavingsSyncResult, error) {
	r, err := a.svc.ApplySync(ctx, transactionIDs)
	if err != nil {
		return web.SavingsSyncResult{}, TranslateSavingsError(err)
	}
	return web.SavingsSyncResult{Created: r.Created, Revised: r.Revised, Invalidated: r.Invalidated}, nil
}

func toWebSavingsPolicy(p savings.Policy) web.SavingsPolicy {
	out := web.SavingsPolicy{Versions: make([]web.SavingsPolicyVersion, 0, len(p.Versions)), Start: p.Start}
	for _, v := range p.Versions {
		w := web.SavingsPolicyVersion{
			ID:              v.ID,
			RateFormatted:   money.Format(v.RateBP, 2),
			EffectiveFrom:   v.EffectiveFrom,
			SuggestionCount: v.SuggestionCount,
			CreatedAt:       v.CreatedAt,
			InForce:         p.Current != nil && p.Current.ID == v.ID,
			Scheduled:       p.Scheduled != nil && p.Scheduled.ID == v.ID,
		}
		out.Versions = append(out.Versions, w)
		if w.InForce {
			current := w
			out.Current = &current
		}
		if w.Scheduled {
			scheduled := w
			out.Scheduled = &scheduled
		}
	}
	return out
}

func toWebSavingsSuggestion(s savings.Suggestion) web.SavingsSuggestion {
	out := web.SavingsSuggestion{
		ID:                    s.ID,
		IncomeTransactionID:   s.IncomeTransactionID,
		Revision:              s.Revision,
		RateFormatted:         money.Format(s.RateBP, 2),
		PolicyEffectiveFrom:   s.PolicyEffectiveFrom,
		IncomeAccountName:     s.IncomeAccountName,
		IncomeCategoryName:    s.IncomeCategoryName,
		IncomeDescription:     s.IncomeDescription,
		IncomeAmountFormatted: money.Format(s.IncomeAmountMinor, s.IncomeMinorUnit),
		IncomeCurrencyCode:    s.IncomeCurrencyCode,
		IncomeEffectiveDate:   s.IncomeEffectiveDate,
		ValuedAt:              s.ValuedAt,
		Status:                string(s.Status),
		StatusLabel:           suggestionStatusLabel(s.Status),
		StatusReason:          s.StatusReason,
		CreatedAt:             s.CreatedAt,
		RateChanged:           s.RateChanged,
		Live:                  s.Status.Live(),
		CanDismiss:            s.Status == savings.StatusOpen && s.RemainingUSDMinor > 0,
		CanConfirm:            s.Status == savings.StatusOpen && s.RemainingUSDMinor > 0,
		ConfirmedFormatted:    usd(s.ConfirmedUSDMinor),
		HasConfirmed:          s.IncomeConfirmedUSDMinor > 0,
		OverConfirmed:         s.OverConfirmedUSDMinor > 0,
		CanValue:              s.Status == savings.StatusPendingValuation && s.Pending == savings.PendingNoRate,
		CanRevalue:            s.Status.Live() && s.RateChanged,
	}
	if s.SuggestedUSDMinor != nil && s.IncomeUSDMinor != nil {
		out.Valued = true
		out.IncomeUSDFormatted = money.Format(*s.IncomeUSDMinor, usdMinorUnit)
		out.SuggestedUSDFormatted = money.Format(*s.SuggestedUSDMinor, usdMinorUnit)
		out.ShowUSDEquivalent = s.IncomeCurrencyCode != savings.FundCurrency
	}
	out.BadgeClass = suggestionBadgeClass(s.Status)
	if s.Status.Live() && s.SuggestedUSDMinor != nil {
		out.IncomeConfirmedFormatted = usd(s.IncomeConfirmedUSDMinor)
		out.RemainingFormatted = usd(s.RemainingUSDMinor)
		out.RemainingAmount = usd(s.RemainingUSDMinor)
		out.OverConfirmedFormatted = usd(s.OverConfirmedUSDMinor)
		out.ConfirmationLabel = confirmationLabel(s.Confirmation)
		if s.Status == savings.StatusOpen && out.ConfirmationLabel != "" {
			out.StatusLabel = out.ConfirmationLabel
		}
	}
	if s.FXMethod != nil {
		out.MethodLabel = methodLabel(*s.FXMethod)
	}
	switch s.Pending {
	case savings.PendingNoRate:
		out.PendingLabel = "Falta la tasa " + s.IncomeCurrencyCode + "/" + savings.FundCurrency +
			" vigente el " + database.FormatDate(s.IncomeEffectiveDate)
	case savings.PendingFutureDate:
		out.PendingLabel = "Su fecha (" + database.FormatDate(s.IncomeEffectiveDate) +
			") aún no llega; se valora ese día"
	}
	for _, leg := range s.Legs {
		out.Legs = append(out.Legs, web.SavingsRateLeg{
			ExchangeRateID: leg.ExchangeRateID,
			Pair:           leg.BaseCurrencyCode + "/" + leg.QuoteCurrencyCode,
			Rate:           leg.Rate.String(),
			EffectiveDate:  leg.EffectiveDate,
			SourceLabel:    sourceLabel(leg.Source),
			Inverted:       leg.Inverted,
		})
	}
	return out
}

// Each label names a value the switch ACCEPTS; an unknown one is shown as it
// is rather than silently relabelled as something it is not (TD-045).

func suggestionStatusLabel(s savings.Status) string {
	switch s {
	case savings.StatusPendingValuation:
		return "Sin valorar"
	case savings.StatusOpen:
		return "Pendiente de confirmar"
	case savings.StatusDismissed:
		return "Descartada"
	case savings.StatusSuperseded:
		return "Reemplazada"
	case savings.StatusInvalidated:
		return "Anulada"
	default:
		return string(s)
	}
}

func suggestionBadgeClass(s savings.Status) string {
	switch s {
	case savings.StatusOpen:
		return "badge-income"
	case savings.StatusPendingValuation:
		return "badge-warning"
	default:
		return "badge-neutral"
	}
}

func methodLabel(m fx.Method) string {
	switch m {
	case fx.MethodIdentity:
		return "En USD, sin conversión"
	case fx.MethodDirect:
		return "Tasa directa"
	case fx.MethodInverted:
		return "Tasa invertida"
	case fx.MethodCross:
		return "Tasa cruzada"
	default:
		return string(m)
	}
}

func sourceLabel(s fx.Source) string {
	switch s {
	case fx.SourceManual:
		return "Manual"
	case fx.SourceBCVOfficial:
		return "BCV"
	default:
		return string(s)
	}
}

func syncActionLabel(a savings.SyncAction) string {
	switch a {
	case savings.SyncCreate:
		return "Crear sugerencia"
	case savings.SyncRevise:
		return "Actualizar sugerencia"
	case savings.SyncInvalidate:
		return "Anular sugerencia"
	default:
		return string(a)
	}
}

// TranslateSavingsError maps savings sentinels onto the web layer's, keeping
// the domain message.
func TranslateSavingsError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	// Both wrap ErrInvalidInput in the domain; translated so that a handler
	// can match either the specific sentinel or ErrInvalidInput.
	case errors.Is(err, savings.ErrReasonRequired):
		return translateError(web.ErrSavingsReasonRequired, translateError(web.ErrInvalidInput, err))
	case errors.Is(err, savings.ErrExceedsMovement):
		return translateError(web.ErrSavingsExceedsMovement, translateError(web.ErrInvalidInput, err))
	case errors.Is(err, savings.ErrInvalidInput):
		return translateError(web.ErrInvalidInput, err)
	case errors.Is(err, savings.ErrNotFound):
		return translateError(web.ErrNotFound, err)
	case errors.Is(err, savings.ErrIncomeNotFound):
		return translateError(web.ErrSavingsIncomeNotFound, err)
	case errors.Is(err, savings.ErrCategoryNotFound):
		return translateError(web.ErrCategoryNotFound, err)
	case errors.Is(err, savings.ErrCategoryNotIncome):
		return translateError(web.ErrSavingsCategoryNotIncome, err)
	case errors.Is(err, savings.ErrRetroactivePolicy):
		return translateError(web.ErrSavingsRetroactivePolicy, err)
	case errors.Is(err, savings.ErrPolicyVersionInUse):
		return translateError(web.ErrSavingsPolicyVersionInUse, err)
	case errors.Is(err, savings.ErrInvalidState):
		return translateError(web.ErrSavingsInvalidState, err)
	case errors.Is(err, savings.ErrRateUnavailable):
		return translateError(web.ErrSavingsRateUnavailable, err)
	case errors.Is(err, savings.ErrNotYetEffective):
		return translateError(web.ErrSavingsNotYetEffective, err)
	case errors.Is(err, savings.ErrRateUnchanged):
		return translateError(web.ErrSavingsRateUnchanged, err)
	case errors.Is(err, savings.ErrInsufficientFree):
		return translateError(web.ErrSavingsInsufficientFree, err)
	case errors.Is(err, savings.ErrInsufficientReserved):
		return translateError(web.ErrSavingsInsufficientReserved, err)
	case errors.Is(err, savings.ErrExceedsSuggestion):
		return translateError(web.ErrSavingsExceedsSuggestion, err)
	case errors.Is(err, savings.ErrNotUSDAccount):
		return translateError(web.ErrSavingsNotUSDAccount, err)
	case errors.Is(err, savings.ErrInvalidRequestKey):
		return translateError(web.ErrSavingsInvalidRequestKey, err)
	case errors.Is(err, savings.ErrAccountNotFound):
		return translateError(web.ErrAccountNotFound, err)
	case errors.Is(err, savings.ErrAccountInactive):
		return translateError(web.ErrAccountInactive, err)
	case errors.Is(err, savings.ErrGoalNotFound):
		return translateError(web.ErrSavingsGoalNotFound, err)
	case errors.Is(err, savings.ErrGoalNotAccepting):
		return translateError(web.ErrSavingsGoalNotAccepting, err)
	case errors.Is(err, savings.ErrDistributionExceeded):
		return translateError(web.ErrSavingsDistributionExceeded, err)
	case errors.Is(err, savings.ErrNotLinkable):
		return translateError(web.ErrSavingsNotLinkable, err)
	default:
		return err
	}
}
