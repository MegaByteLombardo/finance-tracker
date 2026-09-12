package fx_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
)

// caracas is the offset the business calendar runs on. The tests state instants
// in it so that "which day is it" is visible in the test rather than implied.
var caracas = time.FixedZone("-04:00", -4*60*60)

// TestNeedsRefreshIsOncePerVenezuelanDay pins the staleness rule.
//
// It is defined on fetched_at rather than on effective_date, because the BCV
// holds one Fecha Valor across a weekend AND publishes forward -- on 2026-09-11
// its page carried a Fecha Valor of 2026-09-15. An effective_date rule would be
// wrong in both directions at once: re-fetching all weekend, then believing
// itself up to date for four days.
func TestNeedsRefreshIsOncePerVenezuelanDay(t *testing.T) {
	t.Parallel()

	t.Run("nothing has ever been fetched", func(t *testing.T) {
		t.Parallel()

		service, _ := newService(t, &fakeProvider{})

		stale, err := service.NeedsRefresh(t.Context(), time.Now())
		if err != nil {
			t.Fatalf("NeedsRefresh: %v", err)
		}
		if !stale {
			t.Error("a database with no rates reported itself fresh; the first run must fetch immediately")
		}
	})

	t.Run("with no provider there is nothing to refresh", func(t *testing.T) {
		t.Parallel()

		service, _ := newService(t, nil)

		stale, err := service.NeedsRefresh(t.Context(), time.Now())
		if err != nil {
			t.Fatalf("NeedsRefresh: %v", err)
		}
		if stale {
			t.Error("a service with no provider asked to be refreshed")
		}
	})

	tests := []struct {
		name      string
		fetchedAt time.Time
		now       time.Time
		wantStale bool
	}{
		{
			name:      "fetched earlier the same Caracas day",
			fetchedAt: time.Date(2026, 9, 11, 8, 0, 0, 0, caracas),
			now:       time.Date(2026, 9, 11, 20, 0, 0, 0, caracas),
			wantStale: false,
		},
		{
			name:      "fetched yesterday",
			fetchedAt: time.Date(2026, 9, 10, 20, 0, 0, 0, caracas),
			now:       time.Date(2026, 9, 11, 8, 0, 0, 0, caracas),
			wantStale: true,
		},
		{
			// The case a UTC-based rule gets wrong. Both instants are on
			// 2026-09-11 in Caracas, but 22:00 there is already the 12th in UTC.
			name:      "fetched late in the evening, read even later the same evening",
			fetchedAt: time.Date(2026, 9, 11, 21, 0, 0, 0, caracas),
			now:       time.Date(2026, 9, 11, 23, 30, 0, 0, caracas),
			wantStale: false,
		},
		{
			// And the other half of the same case: crossing Caracas midnight IS
			// a new day, even though both instants share a UTC date.
			name:      "fetched just before midnight, read just after",
			fetchedAt: time.Date(2026, 9, 11, 23, 30, 0, 0, caracas),
			now:       time.Date(2026, 9, 12, 0, 30, 0, 0, caracas),
			wantStale: true,
		},
		{
			name:      "fetched a week ago",
			fetchedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, caracas),
			now:       time.Date(2026, 9, 11, 12, 0, 0, 0, caracas),
			wantStale: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &fakeProvider{publication: publication(t, "2026-09-15",
				map[string]string{"USD": "842.20670000"})}
			provider.publication.FetchedAt = tc.fetchedAt

			service, _ := newService(t, provider)
			if _, err := service.RefreshFromProvider(t.Context()); err != nil {
				t.Fatalf("seeding a fetch: %v", err)
			}

			stale, err := service.NeedsRefresh(t.Context(), tc.now)
			if err != nil {
				t.Fatalf("NeedsRefresh: %v", err)
			}
			if stale != tc.wantStale {
				t.Errorf("NeedsRefresh = %t, want %t (fetched %s, now %s, both in Caracas)",
					stale, tc.wantStale,
					tc.fetchedAt.In(caracas).Format(time.RFC3339),
					tc.now.In(caracas).Format(time.RFC3339))
			}
		})
	}
}

func TestRefresherFetchesWhenStaleAndNotOtherwise(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-15",
		map[string]string{"USD": "842.20670000"})}
	provider.publication.FetchedAt = time.Date(2026, 9, 11, 8, 0, 0, 0, caracas)

	service, _ := newService(t, provider)
	refresher := fx.NewRefresher(service, time.Hour, slog.New(slog.DiscardHandler))
	fx.ExportedSetRefresherClock(refresher, func() time.Time {
		return time.Date(2026, 9, 11, 9, 0, 0, 0, caracas)
	})

	refresher.RefreshIfStale(t.Context())
	if provider.calls.Load() != 1 {
		t.Fatalf("the first pass made %d calls, want 1", provider.calls.Load())
	}

	// Same Caracas day: nothing more to do.
	refresher.RefreshIfStale(t.Context())
	refresher.RefreshIfStale(t.Context())
	if provider.calls.Load() != 1 {
		t.Errorf("%d calls after three passes on one day, want 1", provider.calls.Load())
	}

	// Tomorrow.
	fx.ExportedSetRefresherClock(refresher, func() time.Time {
		return time.Date(2026, 9, 12, 9, 0, 0, 0, caracas)
	})
	refresher.RefreshIfStale(t.Context())
	if provider.calls.Load() != 2 {
		t.Errorf("%d calls after the day rolled over, want 2", provider.calls.Load())
	}
}

// TestRefresherSurvivesAFailingProvider is the resilience rule, stated as a test
// because it is the whole reason the refresher returns nothing.
func TestRefresherSurvivesAFailingProvider(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{err: fx.ErrProviderUnavailable}
	service, db := newService(t, provider)

	refresher := fx.NewRefresher(service, time.Hour, slog.New(slog.DiscardHandler))

	// Several passes, each one failing, and none of them panicking or leaving
	// anything behind.
	for range 3 {
		refresher.RefreshIfStale(t.Context())
	}
	if provider.calls.Load() != 3 {
		t.Errorf("%d calls, want 3: a failure must not stop later attempts", provider.calls.Load())
	}

	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM exchange_rates").Scan(&rows); err != nil {
		t.Fatalf("counting rates: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d rates were stored by a failing provider", rows)
	}

	// Manual entry still works with the source down, which is the point.
	seedManual(t, service, "USD", "VES", "2026-09-11", "500")
	if _, err := service.Resolve(t.Context(), "USD", "VES", date(t, "2026-09-11")); err != nil {
		t.Errorf("valuation is impossible while the provider is down: %v", err)
	}
}

// TestRefresherStopsOnCancellation covers shutdown: the goroutine must return so
// the process can close the database.
func TestRefresherStopsOnCancellation(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{publication: publication(t, "2026-09-15",
		map[string]string{"USD": "842.20670000"})}
	service, _ := newService(t, provider)

	// A short interval so that a Run which ignored cancellation would spin
	// rather than sit still, and the test would notice.
	refresher := fx.NewRefresher(service, 5*time.Millisecond, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		refresher.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled; shutdown would hang")
	}
}

// TestRefresherDoesNotRunWithoutAProvider: with the source switched off, Run
// returns immediately rather than leaving a goroutine ticking forever.
func TestRefresherDoesNotRunWithoutAProvider(t *testing.T) {
	t.Parallel()

	service, _ := newService(t, nil)
	refresher := fx.NewRefresher(service, time.Millisecond, slog.New(slog.DiscardHandler))

	done := make(chan struct{})
	go func() {
		refresher.Run(t.Context())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run is ticking with no provider configured")
	}
}

// TestFetchesDoNotOverlap covers the button and the scheduler arriving at once.
//
// Two concurrent fetches would write the same publication twice and hold the
// single SQLite connection for no reason. The scheduler SKIPS rather than
// queueing, because a fetch already running is exactly what its tick wanted.
func TestFetchesDoNotOverlap(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{
		publication: publication(t, "2026-09-15", map[string]string{"USD": "842.20670000"}),
		block:       make(chan struct{}),
	}
	service, _ := newService(t, provider)

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		_, err := service.RefreshFromProvider(t.Context())
		finished <- err
	}()

	<-started
	// Give the goroutine a moment to take the lock. If it has not yet, the
	// assertion below simply does not exercise the case, so this is a
	// best-effort window rather than a timing-dependent assertion.
	waitUntil(t, func() bool { return provider.calls.Load() > 0 })

	_, ran, err := fx.ExportedTryRefresh(t.Context(), service)
	if err != nil {
		t.Fatalf("the skipped attempt reported an error: %v", err)
	}
	if ran {
		t.Error("a second fetch started while one was already running")
	}

	close(provider.block)
	if err := <-finished; err != nil {
		t.Fatalf("the first fetch failed: %v", err)
	}

	if provider.calls.Load() != 1 {
		t.Errorf("the provider was called %d times, want 1", provider.calls.Load())
	}
}

// waitUntil polls a condition rather than sleeping a guessed interval, which is
// what makes the test above condition-based instead of timing-based.
func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition was never met")
}

// Sanity: the sentinels a caller is expected to branch on are distinct, so that
// errors.Is can actually tell them apart.
func TestProviderErrorsAreDistinct(t *testing.T) {
	t.Parallel()

	if errors.Is(fx.ErrProviderUnavailable, fx.ErrProviderInvalidResponse) ||
		errors.Is(fx.ErrProviderInvalidResponse, fx.ErrProviderUnavailable) {
		t.Error("the two provider errors are not distinguishable; " +
			"unreachable is weather, unparseable means the parser needs updating")
	}
}
