package savings_test

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/migrations"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// Many incomes recorded at once, each with its suggestion: one live revision
// per income, none lost, none doubled.
func TestConcurrentIncomesEachGetExactlyOneSuggestion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")

	const n = 20
	ids := make([]int64, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			view, err := e.app.RecordIncome(t.Context(), incomeInput(e, "100"))
			if err != nil {
				errs <- err
				return
			}
			ids[i] = view.ID
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent income failed: %v", err)
	}
	assertOneLivePerIncome(t, e.db, n)
}

// The same backfill applied by several requests at once -- a double click, two
// tabs -- creates each suggestion once.
func TestConcurrentSyncsCreateEachSuggestionOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		ids = append(ids, e.income(e.usd, "100", fmt.Sprintf("2026-09-%02d", i+10)))
	}
	e.policy("20", "2026-09-01")

	runConcurrently(t, 8, func() error {
		_, err := e.svc.ApplySync(t.Context(), ids)
		return err
	})
	assertOneLivePerIncome(t, e.db, len(ids))
}

// Two pools over one file stand in for two processes. BEGIN IMMEDIATE (TD-075)
// makes the second writer wait at BEGIN, before it reads anything, so both
// running the same backfill still create each suggestion exactly once.
//
// A writer that waits longer than busy_timeout -- likely under the race
// detector, where every transaction is many times slower -- is refused AT
// BEGIN, having read and written nothing. That refusal is the safe outcome and
// is accepted; a failure anywhere after BEGIN is not, and neither is a
// duplicate. Under DEFERRED the same test fails at the first write, after the
// decision was taken.
func TestTwoProcessesSyncingAtOnceCreateEachSuggestionOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "finance.db")
	first := openPool(t, path)
	if err := migrations.Up(t.Context(), first, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	second := openPool(t, path)

	e := newEnvOn(t, first)
	ids := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		ids = append(ids, e.income(e.usd, "100", fmt.Sprintf("2026-09-%02d", i+10)))
	}
	e.policy("20", "2026-09-01")

	other := savings.NewService(second)
	savings.SetClock(other, today)

	refusedAtBegin := func(err error) bool { return strings.HasPrefix(err.Error(), "beginning transaction: ") }
	succeeded := runConcurrentlyAllowing(t, 6, refusedAtBegin, func() error {
		_, err := e.svc.ApplySync(t.Context(), ids)
		return err
	}, func() error {
		_, err := other.ApplySync(t.Context(), ids)
		return err
	})
	if succeeded == 0 {
		t.Fatal("every concurrent sync was refused; nothing was exercised")
	}
	assertOneLivePerIncome(t, first, len(ids))
}

func openPool(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	})
	return db
}

// runConcurrently runs each fn rounds times, all at once, and fails on any error.
func runConcurrently(t *testing.T, rounds int, fns ...func() error) {
	t.Helper()
	runConcurrentlyAllowing(t, rounds, func(error) bool { return false }, fns...)
}

// runConcurrentlyAllowing is runConcurrently that tolerates the errors allowed
// reports as safe, and returns how many calls succeeded.
func runConcurrentlyAllowing(t *testing.T, rounds int, allowed func(error) bool, fns ...func() error) int {
	t.Helper()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	errs := make(chan error, rounds*len(fns))
	start := make(chan struct{})
	for i := 0; i < rounds; i++ {
		for _, fn := range fns {
			wg.Add(1)
			go func(fn func() error) {
				defer wg.Done()
				<-start
				err := fn()
				switch {
				case err == nil:
					mu.Lock()
					succeeded++
					mu.Unlock()
				case !allowed(err):
					errs <- err
				}
			}(fn)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent call failed: %v", err)
	}
	return succeeded
}

func assertOneLivePerIncome(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var incomes, live, rows int
	if err := db.QueryRowContext(t.Context(), `
		SELECT (SELECT count(*) FROM transactions WHERE transaction_type = 'INCOME'),
		       (SELECT count(DISTINCT income_transaction_id) FROM savings_suggestions
		         WHERE status IN ('PENDING_VALUATION', 'OPEN', 'DISMISSED')),
		       (SELECT count(*) FROM savings_suggestions)`).Scan(&incomes, &live, &rows); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if incomes != want || live != want || rows != want {
		t.Errorf("%d incomes, %d with a live suggestion, %d suggestion rows; want %d of each",
			incomes, live, rows, want)
	}
}

func incomeInput(e *env, amount string) ledger.RecordIncomeInput {
	return ledger.RecordIncomeInput{
		AccountID: e.usd, CategoryID: e.salary, Amount: amount,
		PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-05",
	}
}
