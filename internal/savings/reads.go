package savings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// ListSuggestions returns every live revision, newest income first, with why a
// pending one has no valuation and whether a valued one's rate has changed.
// The reference rates are read once, whatever the number of suggestions.
func (s *Service) ListSuggestions(ctx context.Context) ([]Suggestion, error) {
	var list []Suggestion
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		lives, err := NewRepository(exec).ListLiveSuggestions(ctx)
		if err != nil {
			return err
		}
		sc := &syncContext{exec: exec, today: s.today()}
		if err := sc.annotate(ctx, lives); err != nil {
			return err
		}
		if err := confirmAll(ctx, exec, lives); err != nil {
			return err
		}
		list = lives
		return nil
	})
	return list, err
}

// confirmAll fills in what reservations confirmed for each revision.
func confirmAll(ctx context.Context, exec database.DBTX, list []Suggestion) error {
	repo := NewRepository(exec)
	byIncome, err := repo.ConfirmedByIncome(ctx)
	if err != nil {
		return err
	}
	bySuggestion, err := repo.ConfirmedBySuggestion(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		applyConfirmation(&list[i], byIncome, bySuggestion)
	}
	return nil
}

// annotate fills in the derived fields of live revisions.
func (sc *syncContext) annotate(ctx context.Context, list []Suggestion) error {
	for i := range list {
		list[i].Pending = sc.pendingReason(list[i])
		changed, err := sc.rateChanged(ctx, list[i])
		if err != nil {
			return err
		}
		list[i].RateChanged = changed
	}
	return nil
}

// IncomeDetail returns one income as the savings side sees it: whether it is
// eligible and why, its override, and every suggestion revision it has had.
//
// A transaction that was an income once and is not any more -- it was
// corrected into an expense -- still has a history worth reading, so it is
// answered from its last revision rather than refused.
func (s *Service) IncomeDetail(ctx context.Context, transactionID int64) (IncomeDetail, error) {
	var detail IncomeDetail
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		repo := NewRepository(exec)
		revisions, err := repo.ListRevisions(ctx, transactionID)
		if err != nil {
			return err
		}
		sc := &syncContext{exec: exec, today: s.today()}
		var live []Suggestion
		for i := range revisions {
			if revisions[i].Status.Live() {
				live = append(live, revisions[i])
			}
		}
		if err := sc.annotate(ctx, live); err != nil {
			return err
		}
		for i := range revisions {
			for _, l := range live {
				if l.ID == revisions[i].ID {
					revisions[i] = l
				}
			}
		}

		if err := confirmAll(ctx, exec, revisions); err != nil {
			return err
		}

		override, err := repo.Override(ctx, transactionID)
		if err != nil {
			return err
		}
		detail = IncomeDetail{TransactionID: transactionID, Override: override, Revisions: revisions}

		item, err := ledger.NewRepository(exec).GetIncome(ctx, transactionID)
		switch {
		case err == nil:
			r, err := loadRules(ctx, exec)
			if err != nil {
				return err
			}
			d := r.decide(item)
			detail.Description = item.Description
			detail.Date = item.Date
			detail.AccountName = item.AccountName
			detail.CategoryID = item.CategoryID
			detail.CategoryName = item.CategoryName
			detail.CurrencyCode = item.CurrencyCode
			detail.MinorUnit = item.MinorUnit
			detail.AmountMinor = item.AmountMinor
			detail.Eligible = d.eligible
			detail.Reason = d.reason
			return nil

		case errors.Is(err, ledger.ErrNotFound):
			if len(revisions) == 0 {
				return fmt.Errorf("transaction %d: %w", transactionID, ErrIncomeNotFound)
			}
			last := revisions[len(revisions)-1]
			detail.Description = last.IncomeDescription
			detail.Date = last.IncomeEffectiveDate
			detail.AccountName = last.IncomeAccountName
			detail.CategoryID = last.IncomeCategoryID
			detail.CategoryName = last.IncomeCategoryName
			detail.CurrencyCode = last.IncomeCurrencyCode
			detail.MinorUnit = last.IncomeMinorUnit
			detail.AmountMinor = last.IncomeAmountMinor
			detail.Reason = "La transacción ya no es un ingreso"
			return nil

		default:
			return fmt.Errorf("reading income %d: %w", transactionID, err)
		}
	})
	return detail, err
}

// PlanSync previews what a sync would do: every income since the policy began
// whose suggestion is missing, out of date or no longer warranted. It writes
// nothing (TD-080).
//
// This is the backfill preview after a retroactive first version, and the
// preview after an eligibility rule changes. It is also the safety net for an
// income changed outside the application -- a restored backup, the sqlite3 CLI.
func (s *Service) PlanSync(ctx context.Context) (SyncPlan, error) {
	var plan SyncPlan
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		var err error
		plan, err = s.planSync(ctx, exec)
		return err
	})
	return plan, err
}

func (s *Service) planSync(ctx context.Context, exec database.DBTX) (SyncPlan, error) {
	plan := SyncPlan{Items: make([]SyncItem, 0)}
	r, err := loadRules(ctx, exec)
	if err != nil {
		return plan, err
	}
	repo := NewRepository(exec)
	lives, err := repo.ListLiveSuggestions(ctx)
	if err != nil {
		return plan, err
	}
	liveByIncome := make(map[int64]*Suggestion, len(lives))
	for i := range lives {
		liveByIncome[lives[i].IncomeTransactionID] = &lives[i]
	}

	var incomes []ledger.IncomeItem
	if start, ok := r.start(); ok {
		if incomes, err = ledger.NewRepository(exec).ListIncomesSince(ctx, start); err != nil {
			return plan, err
		}
	}

	seen := make(map[int64]bool, len(incomes))
	for i := range incomes {
		item := incomes[i]
		seen[item.TransactionID] = true
		d := r.decide(item)
		action, reason := planFor(d, &item, liveByIncome[item.TransactionID])
		if action == "" {
			continue
		}
		plan.Items = append(plan.Items, SyncItem{
			Action:        action,
			TransactionID: item.TransactionID,
			Date:          item.Date,
			Description:   item.Description,
			AccountName:   item.AccountName,
			CategoryName:  item.CategoryName,
			CurrencyCode:  item.CurrencyCode,
			MinorUnit:     item.MinorUnit,
			AmountMinor:   item.AmountMinor,
			RateBP:        d.version.RateBP,
			Reason:        reason,
		})
	}

	// A live revision whose income the list above did not contain: it is no
	// longer an income, or it was moved before the policy's start.
	for _, live := range lives {
		if seen[live.IncomeTransactionID] {
			continue
		}
		reason := "La transacción ya no es un ingreso"
		item, err := ledger.NewRepository(exec).GetIncome(ctx, live.IncomeTransactionID)
		switch {
		case err == nil:
			reason = r.decide(item).reason
		case !errors.Is(err, ledger.ErrNotFound):
			return plan, fmt.Errorf("reading income %d: %w", live.IncomeTransactionID, err)
		}
		plan.Items = append(plan.Items, SyncItem{
			Action:        SyncInvalidate,
			TransactionID: live.IncomeTransactionID,
			Date:          live.IncomeEffectiveDate,
			Description:   live.IncomeDescription,
			AccountName:   live.IncomeAccountName,
			CategoryName:  live.IncomeCategoryName,
			CurrencyCode:  live.IncomeCurrencyCode,
			MinorUnit:     live.IncomeMinorUnit,
			AmountMinor:   live.IncomeAmountMinor,
			Reason:        reason,
		})
	}

	sort.SliceStable(plan.Items, func(i, j int) bool {
		a, b := plan.Items[i], plan.Items[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		return a.TransactionID < b.TransactionID
	})
	return plan, nil
}

// ApplySync syncs exactly the incomes named -- the ones the user saw in the
// preview -- in one transaction, and reports what it did.
//
// Each income is re-planned inside the transaction, so what is applied is what
// is true at commit, not what was true when the preview was drawn; an income
// that needs nothing any more is left alone. Applying the same list twice does
// nothing the second time. An income that was not previewed is never touched.
func (s *Service) ApplySync(ctx context.Context, transactionIDs []int64) (SyncResult, error) {
	var result SyncResult
	err := s.inTx(ctx, "applying the savings sync", func(exec database.DBTX) error {
		result = SyncResult{}
		sc, err := s.newSyncContext(ctx, exec)
		if err != nil {
			return err
		}
		done := make(map[int64]bool, len(transactionIDs))
		for _, id := range transactionIDs {
			if done[id] {
				continue
			}
			done[id] = true
			action, err := sc.syncIncome(ctx, id)
			if err != nil {
				return err
			}
			switch action {
			case SyncCreate:
				result.Created++
			case SyncRevise:
				result.Revised++
			case SyncInvalidate:
				result.Invalidated++
			}
		}
		return nil
	})
	return result, err
}

// Overview returns the policy's figures for the current calendar month.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	today := s.today()
	from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	return s.OverviewFor(ctx, from, to)
}

// OverviewFor returns the policy's figures for the incomes dated between two
// days inclusive. A suggestion belongs to the period of its income's effective
// date: when it was created or valued plays no part.
func (s *Service) OverviewFor(ctx context.Context, from, to time.Time) (Overview, error) {
	if to.Before(from) {
		return Overview{}, fmt.Errorf("%w: the period ends before it starts", ErrInvalidInput)
	}
	out := Overview{From: from, To: to}
	err := s.inReadTx(ctx, func(exec database.DBTX) error {
		repo := NewRepository(exec)
		versions, err := repo.ListPolicyVersions(ctx)
		if err != nil {
			return err
		}
		out.Policy = policyFrom(versions, s.today())

		lives, err := repo.ListLiveSuggestions(ctx)
		if err != nil {
			return err
		}
		if err := confirmAll(ctx, exec, lives); err != nil {
			return err
		}
		sc := &syncContext{exec: exec, today: s.today()}
		pending := make(map[string]*CurrencyAmount)
		inPeriod := func(d time.Time) bool { return !d.Before(from) && !d.After(to) }
		for _, live := range lives {
			if !inPeriod(live.IncomeEffectiveDate) {
				continue
			}
			switch live.Status {
			case StatusPendingValuation:
				out.PendingValuationCount++
				p, ok := pending[live.IncomeCurrencyCode]
				if !ok {
					p = &CurrencyAmount{CurrencyCode: live.IncomeCurrencyCode, MinorUnit: live.IncomeMinorUnit}
					pending[live.IncomeCurrencyCode] = p
				}
				p.AmountMinor += live.IncomeAmountMinor
				continue
			case StatusOpen:
				out.OpenCount++
				out.OpenUSDMinor += live.RemainingUSDMinor
				changed, err := sc.rateChanged(ctx, live)
				if err != nil {
					return err
				}
				if changed {
					out.RateChangedCount++
				}
			case StatusDismissed:
				out.DismissedCount++
				out.DismissedUSDMinor += live.RemainingUSDMinor
			}
			out.EligibleIncomeUSDMinor += *live.IncomeUSDMinor
			out.SuggestedUSDMinor += *live.SuggestedUSDMinor
			out.ConfirmedUSDMinor += live.IncomeConfirmedUSDMinor
			out.OverConfirmedUSDMinor += live.OverConfirmedUSDMinor
		}
		bp, defined, err := money.RatioBasisPoints(out.ConfirmedUSDMinor, out.SuggestedUSDMinor)
		if err != nil {
			return fmt.Errorf("computing the confirmation progress: %w", err)
		}
		out.ConfirmationProgress = Ratio{BasisPoints: bp, Defined: defined}

		// The fund's movement in the period, by the operations' own dates:
		// a different question from how much of the period's suggestions was
		// confirmed, and never mixed with it.
		deltas, err := repo.DailyFundDeltas(ctx)
		if err != nil {
			return err
		}
		for _, d := range deltas {
			if !inPeriod(d.Date) {
				continue
			}
			switch d.Kind {
			case KindReserve:
				out.GrossContributionsMinor += d.AmountMinor
			case KindRelease, KindSpend:
				out.WithdrawalsMinor -= d.AmountMinor
			case KindReallocate, KindReback:
				// Net zero by construction: money moved within the fund.
			}
		}
		out.NetChangeMinor = out.GrossContributionsMinor - out.WithdrawalsMinor
		if out.Fund, err = s.fundTx(ctx, exec); err != nil {
			return err
		}
		for _, p := range pending {
			out.PendingByCurrency = append(out.PendingByCurrency, *p)
		}
		sort.Slice(out.PendingByCurrency, func(i, j int) bool {
			return out.PendingByCurrency[i].CurrencyCode < out.PendingByCurrency[j].CurrencyCode
		})

		plan, err := s.planSync(ctx, exec)
		if err != nil {
			return err
		}
		out.UnsyncedCount = len(plan.Items)

		issues, err := s.issuesTx(ctx, exec)
		if err != nil {
			return err
		}
		out.IssueTotal = len(issues)
		for _, i := range issues {
			if i.Acknowledged == nil {
				out.IssueCount++
			}
		}
		return nil
	})
	return out, err
}
