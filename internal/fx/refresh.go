package fx

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
)

// DefaultRefreshInterval is how often the refresher WAKES UP, which is not how
// often it fetches.
//
// The fetch happens at most once per Venezuelan calendar day (see NeedsRefresh),
// so on an ordinary day this interval only decides how soon after startup, or
// after midnight, the day's single fetch happens. What it really bounds is the
// bad case: if the BCV is down all day, this is the retry cadence, and four
// hours means six attempts rather than the twenty-four an hourly tick would
// make. Six is enough to catch a site that comes back mid-morning and few enough
// to be a courteous amount of traffic to send a government website that owes
// this application nothing.
//
// No exponential backoff. It would be more machinery than a once-a-day fetch can
// justify, and the failure it protects against -- hammering a struggling server
// -- is already ruled out by the interval.
const DefaultRefreshInterval = 4 * time.Hour

// Refresher keeps the automatic source up to date in the background.
//
// It is deliberately the simplest thing that works: one goroutine, one ticker,
// one question asked on each tick. There is no job queue, no cron expression and
// no scheduler abstraction, because there is one job and it runs once a day.
//
// # It must never be able to take the application down
//
// Every failure here is logged and dropped. The exchange rate provider is an
// EXTERNAL SERVICE, and Finance Tracker's core -- accounts, the ledger, balances,
// manual rates, and reading the net worth from rates already stored -- has no
// dependency on it whatsoever. A government website being unreachable is not a
// reason for a personal finance application to stop opening.
type Refresher struct {
	service  *Service
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
}

// NewRefresher builds the background refresher.
//
// now is injected rather than calling time.Now directly so that the staleness
// rule -- which is about calendar days in another timezone -- can be tested by
// stating what day it is instead of waiting for one to pass.
func NewRefresher(service *Service, interval time.Duration, logger *slog.Logger) *Refresher {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Refresher{service: service, interval: interval, now: time.Now, logger: logger}
}

// Run refreshes when needed until ctx is cancelled.
//
// It attempts once immediately, then on each tick. The immediate attempt is what
// makes a machine that was switched off for a week catch up as soon as it is
// turned on, rather than at some arbitrary point up to an interval later.
//
// The caller starts this in a goroutine AFTER the HTTP server is listening.
// Blocking startup on it would make an unreachable website delay or prevent
// serving, which is exactly the coupling this design refuses.
func (r *Refresher) Run(ctx context.Context) {
	if r.service == nil || !r.service.HasProvider() {
		// Nothing to do, and saying so once is more useful than a goroutine that
		// wakes up forever to discover the same thing.
		r.logger.Info("automatic exchange rate refresh is disabled")
		return
	}

	r.logger.Info("automatic exchange rate refresh started",
		slog.String("provider", string(r.service.ProviderName())),
		slog.Duration("interval", r.interval))

	r.RefreshIfStale(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("automatic exchange rate refresh stopped")
			return
		case <-ticker.C:
			r.RefreshIfStale(ctx)
		}
	}
}

// RefreshIfStale fetches only when the day's rates have not been collected yet.
//
// It returns nothing. Every outcome is a log line, because there is no caller
// who could act on a failure: the interface's own button reports its result to
// the person who pressed it, and this one runs with nobody watching.
func (r *Refresher) RefreshIfStale(ctx context.Context) {
	stale, err := r.service.NeedsRefresh(ctx, r.now())
	if err != nil {
		r.logger.Warn("could not tell whether the exchange rates are stale",
			slog.Any("error", err))
		return
	}
	if !stale {
		r.logger.Debug("exchange rates were already refreshed today",
			slog.String("provider", string(r.service.ProviderName())))
		return
	}

	result, ran, err := r.service.tryRefreshFromProvider(ctx)
	switch {
	case !ran:
		// Somebody pressed the button while this tick was starting. Skipping is
		// the right answer: a fetch is already happening, which is all this tick
		// wanted.
		r.logger.Debug("skipped a refresh because one was already running")
	case err != nil && ctx.Err() != nil:
		// Shutdown, not a failure of the source.
		r.logger.Debug("exchange rate refresh interrupted by shutdown")
	case errors.Is(err, ErrProviderInvalidResponse):
		// The one failure worth shouting about: the site answered, and what it
		// said could not be understood. That usually means the page changed and
		// the parser needs updating, and this log line is the only warning
		// anyone will get.
		r.logger.Error("the exchange rate provider returned something unexpected",
			slog.String("provider", string(r.service.ProviderName())),
			slog.Any("error", err))
	case err != nil:
		r.logger.Warn("the scheduled exchange rate refresh failed",
			slog.String("provider", string(r.service.ProviderName())),
			slog.Any("error", err))
	default:
		r.logger.Info("scheduled exchange rate refresh complete",
			slog.String("provider", string(result.Source)),
			slog.String("effective_date", database.FormatDate(result.EffectiveDate)),
			slog.Int("rates", result.Stored))
	}
}

// NeedsRefresh reports whether the automatic source should be asked again.
//
// The rule: the rates are stale unless an observation from the automatic source
// was FETCHED on today's Venezuelan calendar day.
//
// Two things about that are deliberate.
//
// It is defined on fetched_at, not on effective_date. The BCV holds one Fecha
// Valor across a weekend or a holiday, so Friday's rate is still the correct
// rate on Sunday and an effective_date two days old is not evidence of anything
// being wrong. It also publishes FORWARD: on 2026-09-11 the page carried a Fecha
// Valor of 2026-09-15. An effective_date-based rule would therefore be wrong in
// both directions, and would either re-fetch all weekend or decide it was up to
// date for four days.
//
// The day is Venezuela's, from database.EconomicLocation, not UTC. At nine in
// the evening in Caracas, UTC is already tomorrow: a UTC-based rule would fetch
// twice on some days and never on others, for no reason a person could see.
func (s *Service) NeedsRefresh(ctx context.Context, now time.Time) (bool, error) {
	if s.provider == nil {
		return false, nil
	}

	lastFetch, err := NewRepository(s.db).LastFetchedAt(ctx, s.provider.Name())
	if err != nil {
		return false, err
	}
	if lastFetch.IsZero() {
		// Never fetched. A first run should collect rates immediately rather
		// than wait for a tick.
		return true, nil
	}

	return !database.TodayAt(lastFetch).Equal(database.TodayAt(now)), nil
}
