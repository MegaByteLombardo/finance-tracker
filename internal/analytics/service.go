package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/valuation"
)

// Service computes the dashboard.
type Service struct {
	valuation *valuation.Service
	ledger    *ledger.Service

	// now is the clock "today" is read from. It is injected by the composition
	// root, and by tests that need a fixed date, so that no financial date in
	// this package comes from a scattered call to time.Now.
	now func() time.Time
}

// NewService wires the dashboard to the two services it reads from.
func NewService(valuationService *valuation.Service, ledgerService *ledger.Service, now func() time.Time) *Service {
	return &Service{valuation: valuationService, ledger: ledgerService, now: now}
}

// Dashboard computes everything the dashboard shows for a preset, as of today in
// Venezuela.
//
// It issues a constant number of queries whatever the preset: the valuation
// snapshot (settings, currencies, accounts, the ledger's daily deltas, the rates
// in effect through today) and one query for the flows of the current and the
// previous period together. Everything else is arithmetic in memory.
func (s *Service) Dashboard(ctx context.Context, preset Preset) (Dashboard, error) {
	if _, ok := ParsePreset(string(preset)); !ok {
		return Dashboard{}, fmt.Errorf("unknown dashboard period %q", preset)
	}

	today := database.TodayAt(s.now())

	snapshot, err := s.valuation.Load(ctx, today)
	if err != nil {
		return Dashboard{}, err
	}
	base := snapshot.Base()

	earliest, hasEarliest := snapshot.EarliestActivity()
	period, hasPrevious := PeriodFor(preset, today, earliest, hasEarliest)

	result := Dashboard{
		Preset:      preset,
		Today:       today,
		Period:      period,
		HasPrevious: hasPrevious,
	}
	if hasPrevious {
		result.PreviousPeriod = period.Previous()
	}

	if result.NetWorth, err = snapshot.NetWorthAt(today); err != nil {
		return Dashboard{}, err
	}

	// One series from the day before the period to its end: the first point is
	// the baseline the change is measured from, the rest is the chart.
	full, err := snapshot.Series(period.From.AddDate(0, 0, -1), period.To)
	if err != nil {
		return Dashboard{}, err
	}
	result.Opening = full.Points[0]
	result.Series = full
	result.Series.From = period.From
	result.Series.Points = full.Points[1:]
	if !result.Opening.Complete {
		result.Series.IncompleteDays--
	}

	closing := full.Points[len(full.Points)-1]
	if result.Change, err = compare(result.Opening.TotalMinor, closing.TotalMinor,
		result.Opening.Complete && closing.Complete, true, base.MinorUnit); err != nil {
		return Dashboard{}, err
	}

	flowsFrom := period.From
	if hasPrevious {
		flowsFrom = result.PreviousPeriod.From
	}
	flows, err := s.ledger.ListFlows(ctx, flowsFrom, period.To)
	if err != nil {
		return Dashboard{}, fmt.Errorf("reading the period's flows: %w", err)
	}

	convert := converter(snapshot.ConvertToBaseExact)
	if result.CashFlow, err = buildCashFlow(period, flows, convert, base.MinorUnit); err != nil {
		return Dashboard{}, err
	}
	if hasPrevious {
		if result.PreviousCashFlow, err = buildCashFlow(result.PreviousPeriod, flows, convert, base.MinorUnit); err != nil {
			return Dashboard{}, err
		}
	}

	if result.Spending, err = buildSpending(result.CashFlow, result.PreviousCashFlow, hasPrevious, base.MinorUnit); err != nil {
		return Dashboard{}, err
	}
	return result, nil
}
