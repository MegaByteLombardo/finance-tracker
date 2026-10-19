package savings_test

import (
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/account"
	"github.com/MegaByteLombardo/finance-tracker/internal/application"
	"github.com/MegaByteLombardo/finance-tracker/internal/category"
	"github.com/MegaByteLombardo/finance-tracker/internal/database"
	"github.com/MegaByteLombardo/finance-tracker/internal/dbtest"
	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
	"github.com/MegaByteLombardo/finance-tracker/internal/ledger"
	"github.com/MegaByteLombardo/finance-tracker/internal/savings"
)

// Every test runs against real SQLite, with "today" fixed at 2026-10-15 in
// Venezuela, and records incomes the way the application does: through
// application.Service, which writes the income and its suggestion in one
// transaction.

const bankTransfer = 1 // payment method seeded by migration 00008

type env struct {
	t      *testing.T
	db     *sql.DB
	svc    *savings.Service
	app    *application.Service
	ledger *ledger.Service
	rates  *fx.Service

	usd, ves, eur      int64 // accounts
	salary, refunds    int64 // income categories
	food               int64 // expense category
	usdSavings, usdTwo int64 // more USD accounts
}

func today() time.Time {
	return time.Date(2026, 10, 15, 12, 0, 0, 0, database.EconomicLocation)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.New(t)
	return newEnvOn(t, db)
}

func newEnvOn(t *testing.T, db *sql.DB) *env {
	t.Helper()
	svc := savings.NewService(db)
	savings.SetClock(svc, today)
	e := &env{
		t:      t,
		db:     db,
		svc:    svc,
		app:    application.NewService(db, svc),
		ledger: ledger.NewService(db),
		rates:  fx.NewService(db, nil, slog.New(slog.DiscardHandler)),
	}
	ctx := t.Context()
	accounts := account.NewService(db)
	open := func(name, code string) int64 {
		a, err := accounts.Create(ctx, account.CreateInput{Name: name, Type: account.Cash, CurrencyCode: code})
		if err != nil {
			t.Fatalf("opening %s: %v", name, err)
		}
		return a.ID
	}
	e.usd, e.ves, e.eur = open("Dólares", "USD"), open("Bolívares", "VES"), open("Euros", "EUR")
	e.usdSavings, e.usdTwo = open("Ahorro USD", "USD"), open("Zelle", "USD")

	categories := category.NewService(db)
	create := func(name string, typ category.Type) int64 {
		c, err := categories.Create(ctx, category.CreateInput{Name: name, Type: typ})
		if err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		return c.ID
	}
	e.salary, e.refunds = create("Salario", category.Income), create("Reembolsos", category.Income)
	e.food = create("Comida", category.Expense)
	return e
}

func (e *env) policy(rate, from string) {
	e.t.Helper()
	if err := e.svc.SetPolicy(e.t.Context(), savings.SetPolicyInput{Rate: rate, EffectiveFrom: from}); err != nil {
		e.t.Fatalf("SetPolicy(%s, %s): %v", rate, from, err)
	}
}

func (e *env) rate(base, quote, value, date string) fx.Rate {
	e.t.Helper()
	r, err := e.rates.CreateManual(e.t.Context(), fx.CreateManualInput{
		BaseCurrencyCode: base, QuoteCurrencyCode: quote, Rate: value, EffectiveDate: date,
	})
	if err != nil {
		e.t.Fatalf("recording %s/%s on %s: %v", base, quote, date, err)
	}
	return r
}

func (e *env) income(accountID int64, amount, date string) int64 {
	e.t.Helper()
	return e.incomeIn(accountID, e.salary, amount, date)
}

func (e *env) incomeIn(accountID, categoryID int64, amount, date string) int64 {
	e.t.Helper()
	view, err := e.app.RecordIncome(e.t.Context(), ledger.RecordIncomeInput{
		AccountID: accountID, CategoryID: categoryID, Amount: amount,
		PaymentMethodID: bankTransfer, EffectiveDate: date,
	})
	if err != nil {
		e.t.Fatalf("recording an income of %s on %s: %v", amount, date, err)
	}
	return view.ID
}

func (e *env) edit(id int64, in ledger.UpdateTransactionInput) {
	e.t.Helper()
	if in.PaymentMethodID == 0 {
		in.PaymentMethodID = bankTransfer
	}
	if _, err := e.app.UpdateTransaction(e.t.Context(), id, in); err != nil {
		e.t.Fatalf("editing transaction %d: %v", id, err)
	}
}

func (e *env) revisions(id int64) []savings.Suggestion {
	e.t.Helper()
	detail, err := e.svc.IncomeDetail(e.t.Context(), id)
	if err != nil {
		e.t.Fatalf("IncomeDetail(%d): %v", id, err)
	}
	return detail.Revisions
}

func (e *env) live(id int64) savings.Suggestion {
	e.t.Helper()
	var found *savings.Suggestion
	for _, r := range e.revisions(id) {
		if r.Status.Live() {
			if found != nil {
				e.t.Fatalf("income %d has two live revisions", id)
			}
			r := r
			found = &r
		}
	}
	if found == nil {
		e.t.Fatalf("income %d has no live suggestion", id)
	}
	return *found
}

func (e *env) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRowContext(e.t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		e.t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

func suggested(t *testing.T, s savings.Suggestion) int64 {
	t.Helper()
	if s.SuggestedUSDMinor == nil {
		t.Fatalf("suggestion %d (%s) has no suggested amount", s.ID, s.Status)
	}
	return *s.SuggestedUSDMinor
}

// --- Income -> Suggestion ----------------------------------------------------

// Income $800, rate 20%: a suggestion of $160, and nothing reserved -- M7.1 has
// no reservation at all, and the ledger holds exactly the income.
func TestAnIncomeProducesASuggestion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")

	id := e.income(e.usd, "800.00", "2026-10-05")
	s := e.live(id)

	if s.Status != savings.StatusOpen || s.Revision != 1 {
		t.Errorf("status %s revision %d, want OPEN 1", s.Status, s.Revision)
	}
	if got := suggested(t, s); got != 16000 {
		t.Errorf("suggested %d cents, want 16000", got)
	}
	if *s.IncomeUSDMinor != 80000 || s.RateBP != 2000 {
		t.Errorf("income %d cents at %d bp, want 80000 at 2000", *s.IncomeUSDMinor, s.RateBP)
	}
	if *s.FXMethod != fx.MethodIdentity || len(s.Legs) != 0 {
		t.Errorf("a USD income was valued by %s with %d legs, want IDENTITY and none", *s.FXMethod, len(s.Legs))
	}
	if e.count("transactions") != 1 || e.count("account_movements") != 1 {
		t.Error("recording an income with a suggestion wrote something else to the ledger")
	}
	balance, err := e.ledger.GetAccountBalance(t.Context(), e.usd)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.BalanceMinor != 80000 {
		t.Errorf("the account holds %d, want 80000: a suggestion must not move money", balance.BalanceMinor)
	}
}

// $500 and $300 at 20%: $100 and $60, $160 suggested in the month.
func TestEachIncomeIsSuggestedFromAsItArrives(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")

	first := e.income(e.usd, "500", "2026-10-02")
	if got := suggested(t, e.live(first)); got != 10000 {
		t.Errorf("first income suggested %d, want 10000 before the second arrives", got)
	}
	second := e.income(e.usd, "300", "2026-10-09")
	if got := suggested(t, e.live(second)); got != 6000 {
		t.Errorf("second income suggested %d, want 6000", got)
	}

	o, err := e.svc.Overview(t.Context())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.SuggestedUSDMinor != 16000 || o.EligibleIncomeUSDMinor != 80000 || o.OpenCount != 2 {
		t.Errorf("overview suggested %d of %d over %d open, want 16000 of 80000 over 2",
			o.SuggestedUSDMinor, o.EligibleIncomeUSDMinor, o.OpenCount)
	}
}

// --- VES and FX --------------------------------------------------------------

// A VES income is suggested from in USD at the rate in force on ITS date: not a
// later one, not a forward-dated one. The observation is copied.
func TestAVESIncomeIsValuedAtTheRateOfItsDate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	applied := e.rate("USD", "VES", "40.00", "2026-10-04")
	e.rate("USD", "VES", "45.00", "2026-10-06") // after the income: never used
	e.rate("USD", "VES", "50.00", "2026-10-20") // forward-dated: never used

	id := e.income(e.ves, "32000.00", "2026-10-05")
	s := e.live(id)

	if *s.IncomeUSDMinor != 80000 || suggested(t, s) != 16000 {
		t.Errorf("valued at %d / suggested %d, want 80000 / 16000 (32,000 VES at 40)", *s.IncomeUSDMinor, suggested(t, s))
	}
	if *s.FXMethod != fx.MethodInverted || len(s.Legs) != 1 {
		t.Fatalf("method %s with %d legs, want INVERTED with one", *s.FXMethod, len(s.Legs))
	}
	leg := s.Legs[0]
	if leg.ExchangeRateID != applied.ID || leg.Rate.String() != "40.00" || !leg.Inverted ||
		database.FormatDate(leg.EffectiveDate) != "2026-10-04" || leg.Source != fx.SourceManual {
		t.Errorf("leg = %+v, want a copy of observation %d (40.00 on 2026-10-04, inverted)", leg, applied.ID)
	}
	if e.count("account_movements") != 1 {
		t.Error("a VES income reserved or moved something")
	}
}

// A cross rate through the BCV's pivot: EUR in USD from EUR/VES and USD/VES.
func TestAEURIncomeIsValuedThroughThePivot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("10", "2026-10-01")
	e.rate("USD", "VES", "40.00", "2026-10-04")
	e.rate("EUR", "VES", "44.00", "2026-10-04")

	s := e.live(e.income(e.eur, "1000.00", "2026-10-05"))
	// 1,000 EUR = 44,000 VES = 1,100 USD; 10% = 110 USD.
	if *s.IncomeUSDMinor != 110000 || suggested(t, s) != 11000 {
		t.Errorf("valued %d / suggested %d, want 110000 / 11000", *s.IncomeUSDMinor, suggested(t, s))
	}
	if *s.FXMethod != fx.MethodCross || len(s.Legs) != 2 {
		t.Errorf("method %s with %d legs, want CROSS with two", *s.FXMethod, len(s.Legs))
	}
}

// No rate: the suggestion is recorded PENDING_VALUATION with no amount -- never
// zero, never a later rate. When a rate for the income's date arrives, valuing
// it turns the SAME row OPEN.
func TestAMissingRateLeavesTheSuggestionPendingAndValuingItCreatesNothingNew(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.rate("USD", "VES", "45.00", "2026-10-08") // after the income: does not apply

	id := e.income(e.ves, "32000.00", "2026-10-05")
	pending := e.live(id)
	if pending.Status != savings.StatusPendingValuation || pending.SuggestedUSDMinor != nil {
		t.Fatalf("status %s, amount %v: want PENDING_VALUATION with no amount", pending.Status, pending.SuggestedUSDMinor)
	}
	list, err := e.svc.ListSuggestions(t.Context())
	if err != nil {
		t.Fatalf("ListSuggestions: %v", err)
	}
	if len(list) != 1 || list[0].Pending != savings.PendingNoRate {
		t.Errorf("the listing does not say the rate is missing: %+v", list)
	}

	err = e.svc.ValueSuggestion(t.Context(), id)
	if !errors.Is(err, savings.ErrRateUnavailable) {
		t.Fatalf("valuing with no applicable rate returned %v, want ErrRateUnavailable", err)
	}

	e.rate("USD", "VES", "40.00", "2026-10-05")
	if err := e.svc.ValueSuggestion(t.Context(), id); err != nil {
		t.Fatalf("ValueSuggestion: %v", err)
	}
	valued := e.live(id)
	if valued.ID != pending.ID || valued.Status != savings.StatusOpen || suggested(t, valued) != 16000 {
		t.Errorf("valued revision %d %s %v, want the same row %d OPEN at 16000",
			valued.ID, valued.Status, valued.SuggestedUSDMinor, pending.ID)
	}
	if e.count("savings_suggestions") != 1 {
		t.Errorf("valuing created a second suggestion")
	}
	if err := e.svc.ValueSuggestion(t.Context(), id); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("valuing twice returned %v, want ErrInvalidState", err)
	}
}

func TestValuePendingValuesWhatItCanAndLeavesTheRest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	first := e.income(e.ves, "4000.00", "2026-10-05")
	second := e.income(e.ves, "4000.00", "2026-10-03")
	e.rate("USD", "VES", "40.00", "2026-10-04")

	n, err := e.svc.ValuePending(t.Context())
	if err != nil {
		t.Fatalf("ValuePending: %v", err)
	}
	if n != 1 {
		t.Errorf("valued %d, want 1", n)
	}
	if e.live(first).Status != savings.StatusOpen || e.live(second).Status != savings.StatusPendingValuation {
		t.Error("the income dated after the rate should be valued and the one before it should not")
	}
}

// A future-dated income in VES cannot know its rate yet (TD-081); one in USD
// needs none.
func TestAFutureDatedIncomeWaitsForItsDateUnlessItIsInUSD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.rate("USD", "VES", "40.00", "2026-10-14")

	ves := e.income(e.ves, "4000.00", "2026-10-20")
	if s := e.live(ves); s.Status != savings.StatusPendingValuation {
		t.Errorf("a future VES income is %s, want PENDING_VALUATION", s.Status)
	}
	if err := e.svc.ValueSuggestion(t.Context(), ves); !errors.Is(err, savings.ErrNotYetEffective) {
		t.Errorf("valuing it returned %v, want ErrNotYetEffective", err)
	}
	list, err := e.svc.ListSuggestions(t.Context())
	if err != nil {
		t.Fatalf("ListSuggestions: %v", err)
	}
	if list[0].Pending != savings.PendingFutureDate {
		t.Errorf("pending reason %q, want FUTURE_DATE", list[0].Pending)
	}

	usd := e.income(e.usd, "100", "2026-10-20")
	if s := e.live(usd); s.Status != savings.StatusOpen || suggested(t, s) != 2000 {
		t.Errorf("a future USD income is %s, want OPEN at 2000", s.Status)
	}
}

// Buying USD is an FX_EXCHANGE and a move between own accounts is a TRANSFER:
// neither is income, so neither is suggested from -- the dollars that arrive
// were already counted when the bolívares were earned.
func TestExchangesAndTransfersAreNeverSuggestedFrom(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.rate("USD", "VES", "40.00", "2026-10-04")
	e.income(e.ves, "32000.00", "2026-10-05")

	ctx := t.Context()
	if _, err := e.ledger.RecordExchange(ctx, ledger.RecordExchangeInput{
		SourceAccountID: e.ves, SourceAmount: "32000.00",
		DestinationAccountID: e.usd, DestinationAmount: "790.00", EffectiveDate: "2026-10-06",
	}); err != nil {
		t.Fatalf("buying USD: %v", err)
	}
	if _, err := e.ledger.RecordTransfer(ctx, ledger.RecordTransferInput{
		SourceAccountID: e.usd, DestinationAccountID: e.usdSavings, Amount: "500.00", EffectiveDate: "2026-10-07",
	}); err != nil {
		t.Fatalf("transferring: %v", err)
	}

	if n := e.count("savings_suggestions"); n != 1 {
		t.Errorf("%d suggestions, want only the income's", n)
	}
	plan, err := e.svc.PlanSync(ctx)
	if err != nil {
		t.Fatalf("PlanSync: %v", err)
	}
	if len(plan.Items) != 0 {
		t.Errorf("the sync would change %d incomes after an exchange and a transfer, want none: %+v", len(plan.Items), plan.Items)
	}
}

// --- rounding ------------------------------------------------------------------

// Both figures are ONE rounding of an exact value, half to even.
func TestSuggestionsRoundOnceHalfToEven(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("50", "2026-10-01")
	e.rate("USD", "VES", "10", "2026-10-01")

	// $1.05 at 50% is 52.5 cents: half to even gives 52.
	if got := suggested(t, e.live(e.income(e.usd, "1.05", "2026-10-02"))); got != 52 {
		t.Errorf("$1.05 at 50%% suggested %d cents, want 52", got)
	}

	// 1.06 VES at 10 is 10.6 cents, rounded to 11 for the income. 50% of the
	// EXACT value is 5.3 cents, which rounds to 5; rounding twice (50% of 11)
	// would have given 5.5 and then 6.
	s := e.live(e.income(e.ves, "1.06", "2026-10-02"))
	if *s.IncomeUSDMinor != 11 || suggested(t, s) != 5 {
		t.Errorf("1.06 VES valued %d and suggested %d cents, want 11 and 5", *s.IncomeUSDMinor, suggested(t, s))
	}
}

// --- policy versions -----------------------------------------------------------

// The version applied is chosen by the income's EFFECTIVE date. Changing the
// rate from 20% to 25% leaves every earlier suggestion as it was -- including
// one for an income entered after the change but dated before it.
func TestChangingTheRateNeverReachesBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	before := e.income(e.usd, "800", "2026-10-05")

	e.policy("25", "2026-10-20")
	after := e.income(e.usd, "800", "2026-10-21")
	lateButOld := e.income(e.usd, "800", "2026-10-10")

	if got := suggested(t, e.live(before)); got != 16000 {
		t.Errorf("the earlier suggestion became %d, want 16000", got)
	}
	if got := suggested(t, e.live(after)); got != 20000 {
		t.Errorf("an income after the change suggested %d, want 20000", got)
	}
	if got := suggested(t, e.live(lateButOld)); got != 16000 {
		t.Errorf("an income dated before the change but entered after it suggested %d, want 16000", got)
	}
	if e.live(before).Revision != 1 {
		t.Error("setting a new rate revised an existing suggestion")
	}

	p, err := e.svc.GetPolicy(t.Context())
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(p.Versions) != 2 || p.Current == nil || p.Current.RateBP != 2000 || p.Scheduled == nil || p.Scheduled.RateBP != 2500 {
		t.Errorf("policy = %+v, want 20%% current and 25%% scheduled", p)
	}
}

func TestOnlyTheFirstVersionMayReachBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()

	e.policy("20", "2026-01-01") // the first: retroactive is allowed
	e.income(e.usd, "100", "2026-03-01")

	for name, in := range map[string]savings.SetPolicyInput{
		"in the past":                {Rate: "25", EffectiveFrom: "2026-10-14"},
		"on the first version's day": {Rate: "25", EffectiveFrom: "2026-01-01"},
	} {
		err := e.svc.SetPolicy(ctx, in)
		if !errors.Is(err, savings.ErrRetroactivePolicy) && !errors.Is(err, savings.ErrPolicyVersionInUse) {
			t.Errorf("%s: SetPolicy returned %v, want a refusal", name, err)
		}
	}

	// Today and a scheduled version are fine; a scheduled one nobody used yet
	// may have its rate corrected.
	e.policy("25", "2026-10-15")
	e.policy("30", "2026-11-01")
	e.policy("35", "2026-11-01")
	p, err := e.svc.GetPolicy(ctx)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(p.Versions) != 3 || p.Versions[0].RateBP != 3500 {
		t.Errorf("versions = %+v, want three with the scheduled one corrected to 35%%", p.Versions)
	}
	for _, bad := range []string{"-1", "100.01", "12.345", "veinte", ""} {
		if err := e.svc.SetPolicy(ctx, savings.SetPolicyInput{Rate: bad, EffectiveFrom: "2026-12-01"}); !errors.Is(err, savings.ErrInvalidInput) {
			t.Errorf("rate %q returned %v, want ErrInvalidInput", bad, err)
		}
	}
}

// --- edits ------------------------------------------------------------------------

// A correction closes the live revision and opens the next, so history is kept
// and there is never more than one live suggestion.
func TestCorrectingAnIncomeRevisesItsSuggestion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.rate("USD", "VES", "40.00", "2026-10-01")
	id := e.income(e.usd, "800", "2026-10-05")

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "1000", EffectiveDate: "2026-10-05"})
	revs := e.revisions(id)
	if len(revs) != 2 || revs[0].Status != savings.StatusSuperseded || revs[1].Status != savings.StatusOpen {
		t.Fatalf("revisions after an amount change: %+v", revs)
	}
	if suggested(t, revs[1]) != 20000 || suggested(t, revs[0]) != 16000 {
		t.Errorf("revision amounts %d then %d, want 16000 kept and 20000 new", suggested(t, revs[0]), suggested(t, revs[1]))
	}
	if revs[0].StatusReason == nil || *revs[0].StatusReason != "El ingreso cambió: importe" {
		t.Errorf("superseded reason %v", revs[0].StatusReason)
	}

	// Moved to a VES account: reinterpreted and re-valued at that date's rate.
	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.ves, Type: ledger.Income, CategoryID: e.salary,
		Amount: "40000.00", EffectiveDate: "2026-10-05", AllowCurrencyChange: true})
	if s := e.live(id); s.Revision != 3 || suggested(t, s) != 20000 || s.IncomeCurrencyCode != "VES" {
		t.Errorf("after moving to VES: revision %d, suggested %v, currency %s", s.Revision, s.SuggestedUSDMinor, s.IncomeCurrencyCode)
	}

	// An edit that changes nothing the suggestion depends on revises nothing.
	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.ves, Type: ledger.Income, CategoryID: e.salary,
		Amount: "40000.00", EffectiveDate: "2026-10-05", Description: ptr("Quincena")})
	if n := len(e.revisions(id)); n != 3 {
		t.Errorf("a description change produced revision %d", n)
	}
}

// Dated across a policy boundary, an income takes the other version's rate.
func TestMovingAnIncomeAcrossVersionsChangesItsRate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.policy("25", "2026-10-20")
	id := e.income(e.usd, "800", "2026-10-21")

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "800", EffectiveDate: "2026-10-05"})
	s := e.live(id)
	if suggested(t, s) != 16000 || s.RateBP != 2000 {
		t.Errorf("after moving before the change: %d at %d bp, want 16000 at 2000", suggested(t, s), s.RateBP)
	}
	if r := e.revisions(id)[0].StatusReason; r == nil || *r != "El ingreso cambió: fecha, versión de la política" {
		t.Errorf("reason %v", r)
	}
}

// An income that stops being an income -- corrected into an expense, there is no
// delete -- or moves to an excluded category is INVALIDATED, and its history
// stays. Corrected back, it is suggested from again, as a new revision.
func TestAnIncomeThatStopsBeingEligibleIsInvalidatedAndKeepsItsHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	if err := e.svc.SetCategoryEligibility(t.Context(), e.refunds, false); err != nil {
		t.Fatalf("excluding refunds: %v", err)
	}
	id := e.income(e.usd, "800", "2026-10-05")

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.refunds,
		Amount: "800", EffectiveDate: "2026-10-05"})
	revs := e.revisions(id)
	if len(revs) != 1 || revs[0].Status != savings.StatusInvalidated {
		t.Fatalf("after moving to an excluded category: %+v", revs)
	}

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "800", EffectiveDate: "2026-10-05"})
	if s := e.live(id); s.Revision != 2 || s.Status != savings.StatusOpen {
		t.Errorf("moved back: revision %d %s, want 2 OPEN", s.Revision, s.Status)
	}

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Expense, CategoryID: e.food,
		Amount: "800", EffectiveDate: "2026-10-05"})
	revs = e.revisions(id)
	last := revs[len(revs)-1]
	if last.Status != savings.StatusInvalidated || last.StatusReason == nil ||
		*last.StatusReason != "La transacción ya no es un ingreso" {
		t.Errorf("after becoming an expense: %+v", last)
	}
	detail, err := e.svc.IncomeDetail(t.Context(), id)
	if err != nil {
		t.Fatalf("an income turned expense has no detail: %v", err)
	}
	if detail.Eligible || len(detail.Revisions) != 2 {
		t.Errorf("detail = %+v", detail)
	}

	// An expense corrected INTO an income becomes eligible.
	expense, err := e.ledger.RecordExpense(t.Context(), ledger.RecordExpenseInput{
		AccountID: e.usd, CategoryID: e.food, Amount: "50", PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-06",
	})
	if err != nil {
		t.Fatalf("expense: %v", err)
	}
	e.edit(expense.ID, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "50", EffectiveDate: "2026-10-06"})
	if s := e.live(expense.ID); suggested(t, s) != 1000 {
		t.Errorf("an expense corrected into income suggested %d, want 1000", suggested(t, s))
	}
}

// A dismissed suggestion stays dismissed until the income changes; a changed
// income is a new question and opens a new revision.
func TestDismissingKeepsTheDecisionUntilTheIncomeChanges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	id := e.income(e.usd, "800", "2026-10-05")

	if err := e.svc.DismissSuggestion(t.Context(), id, "  "); err != nil {
		t.Fatalf("DismissSuggestion: %v", err)
	}
	s := e.live(id)
	if s.Status != savings.StatusDismissed || *s.StatusReason != "Descartada por el usuario" {
		t.Errorf("after dismissing: %s %v", s.Status, s.StatusReason)
	}
	if err := e.svc.DismissSuggestion(t.Context(), id, "otra vez"); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("dismissing twice returned %v, want ErrInvalidState", err)
	}
	if _, err := e.svc.SyncIncomeTx(t.Context(), e.db, id); err != nil {
		t.Fatalf("SyncIncomeTx: %v", err)
	}
	if e.live(id).Status != savings.StatusDismissed {
		t.Error("a sync re-opened a dismissed suggestion whose income had not changed")
	}

	e.edit(id, ledger.UpdateTransactionInput{AccountID: e.usd, Type: ledger.Income, CategoryID: e.salary,
		Amount: "900", EffectiveDate: "2026-10-05"})
	if s := e.live(id); s.Status != savings.StatusOpen || s.Revision != 2 {
		t.Errorf("after the income changed: %s revision %d, want OPEN 2", s.Status, s.Revision)
	}

	pending := e.income(e.ves, "100", "2026-10-05")
	if err := e.svc.DismissSuggestion(t.Context(), pending, ""); !errors.Is(err, savings.ErrInvalidState) {
		t.Errorf("dismissing a suggestion with no amount returned %v, want ErrInvalidState", err)
	}
}

// --- rate corrections -----------------------------------------------------------

// A corrected rate is reported, never applied silently. Revaluing is explicit,
// closes the revision and opens the next at the corrected rate.
func TestACorrectedRateIsReportedAndRevaluedOnlyOnRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.policy("20", "2026-10-01")
	r := e.rate("USD", "VES", "40.00", "2026-10-04")
	id := e.income(e.ves, "32000.00", "2026-10-05")

	if err := e.svc.RevalueSuggestion(ctx, id); !errors.Is(err, savings.ErrRateUnchanged) {
		t.Errorf("revaluing at an unchanged rate returned %v, want ErrRateUnchanged", err)
	}

	if _, err := e.rates.UpdateManual(ctx, r.ID, fx.UpdateManualInput{Rate: "50.00"}); err != nil {
		t.Fatalf("correcting the rate: %v", err)
	}
	if s := e.live(id); suggested(t, s) != 16000 || !s.RateChanged {
		t.Errorf("after the correction: suggested %d, RateChanged %v; want 16000 kept and the change reported",
			suggested(t, s), s.RateChanged)
	}
	o, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if o.RateChangedCount != 1 {
		t.Errorf("overview counts %d changed rates, want 1", o.RateChangedCount)
	}

	if err := e.svc.RevalueSuggestion(ctx, id); err != nil {
		t.Fatalf("RevalueSuggestion: %v", err)
	}
	revs := e.revisions(id)
	if len(revs) != 2 || revs[0].Status != savings.StatusSuperseded || suggested(t, revs[1]) != 12800 {
		t.Fatalf("after revaluing: %+v", revs)
	}
	if revs[0].Legs[0].Rate.String() != "40.00" || revs[1].Legs[0].Rate.String() != "50.00" {
		t.Error("the closed revision no longer records the rate it used")
	}
	if e.live(id).RateChanged {
		t.Error("the new revision still reports a changed rate")
	}
}

// --- backfill and sync ------------------------------------------------------------

// Incomes recorded before any policy are not suggested from until a first,
// retroactive version is set AND its backfill is previewed and confirmed. The
// backfill is idempotent and touches only what was previewed.
func TestARetroactiveStartIsBackfilledOnlyWhenConfirmed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()

	a := e.income(e.usd, "500", "2026-09-10")
	b := e.income(e.usd, "300", "2026-09-20")
	old := e.income(e.usd, "999", "2026-08-31")
	if e.count("savings_suggestions") != 0 {
		t.Fatal("an income was suggested from before any policy existed")
	}

	e.policy("20", "2026-09-01")
	if e.count("savings_suggestions") != 0 {
		t.Fatal("setting a retroactive policy generated suggestions by itself")
	}

	plan, err := e.svc.PlanSync(ctx)
	if err != nil {
		t.Fatalf("PlanSync: %v", err)
	}
	if len(plan.Items) != 2 || plan.Items[0].TransactionID != a || plan.Items[1].TransactionID != b ||
		plan.Items[0].Action != savings.SyncCreate || plan.Items[0].RateBP != 2000 {
		t.Fatalf("preview = %+v, want CREATE for the two incomes since the start, oldest first", plan.Items)
	}

	result, err := e.svc.ApplySync(ctx, []int64{a})
	if err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if result.Created != 1 || e.count("savings_suggestions") != 1 {
		t.Errorf("applying one previewed income: %+v, %d rows", result, e.count("savings_suggestions"))
	}

	result, err = e.svc.ApplySync(ctx, []int64{a, b, b, old})
	if err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if result != (savings.SyncResult{Created: 1}) {
		t.Errorf("second apply = %+v, want exactly one more created", result)
	}
	again, err := e.svc.ApplySync(ctx, []int64{a, b})
	if err != nil || again != (savings.SyncResult{}) {
		t.Errorf("applying again = %+v, %v; want nothing", again, err)
	}
	if plan, err := e.svc.PlanSync(ctx); err != nil || len(plan.Items) != 0 {
		t.Errorf("after the backfill the preview still lists %+v (%v)", plan.Items, err)
	}
}

// Changing a category's eligibility touches nothing until its preview is
// applied; overriding one income applies at once, because it is an act on that
// income.
func TestEligibilityRulesArePreviewedAndOverridesApplyAtOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	e.policy("20", "2026-10-01")
	refund := e.incomeIn(e.usd, e.refunds, "40", "2026-10-03")
	salary := e.income(e.usd, "800", "2026-10-05")

	if err := e.svc.SetCategoryEligibility(ctx, e.refunds, false); err != nil {
		t.Fatalf("SetCategoryEligibility: %v", err)
	}
	if e.live(refund).Status != savings.StatusOpen {
		t.Fatal("excluding a category changed a suggestion before the sync was applied")
	}
	plan, err := e.svc.PlanSync(ctx)
	if err != nil {
		t.Fatalf("PlanSync: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != savings.SyncInvalidate || plan.Items[0].TransactionID != refund {
		t.Fatalf("preview = %+v, want INVALIDATE for the refund", plan.Items)
	}
	if _, err := e.svc.ApplySync(ctx, []int64{refund}); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	if r := e.revisions(refund); r[0].Status != savings.StatusInvalidated {
		t.Errorf("refund is %s, want INVALIDATED", r[0].Status)
	}

	if err := e.svc.SetIncomeOverride(ctx, salary, false, "Es un adelanto, no salario"); err != nil {
		t.Fatalf("SetIncomeOverride: %v", err)
	}
	if r := e.revisions(salary); r[0].Status != savings.StatusInvalidated ||
		*r[0].StatusReason != "Excluido manualmente: Es un adelanto, no salario" {
		t.Errorf("after excluding the salary: %+v", r[0])
	}
	if err := e.svc.SetIncomeOverride(ctx, refund, true, "Este reembolso sí lo ahorro"); err != nil {
		t.Fatalf("including the refund: %v", err)
	}
	if s := e.live(refund); s.Status != savings.StatusOpen || s.Revision != 2 {
		t.Errorf("after including the refund: %s revision %d", s.Status, s.Revision)
	}

	for name, err := range map[string]error{
		"blank reason":     e.svc.SetIncomeOverride(ctx, salary, true, " "),
		"expense category": e.svc.SetCategoryEligibility(ctx, e.food, true),
		"unknown category": e.svc.SetCategoryEligibility(ctx, 9999, true),
		"not an income":    e.svc.SetIncomeOverride(ctx, 9999, true, "x"),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	list, err := e.svc.ListCategoryEligibility(ctx)
	if err != nil {
		t.Fatalf("ListCategoryEligibility: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d income categories, want 2", len(list))
	}
	for _, c := range list {
		if c.CategoryID == e.salary && (!c.Eligible || c.Explicit) {
			t.Errorf("salary = %+v, want eligible by default", c)
		}
		if c.CategoryID == e.refunds && (c.Eligible || !c.Explicit) {
			t.Errorf("refunds = %+v, want explicitly excluded", c)
		}
	}
}

// A sync run twice changes nothing the second time.
func TestSyncIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	id := e.income(e.usd, "800", "2026-10-05")
	for i := 0; i < 3; i++ {
		action, err := e.svc.SyncIncomeTx(t.Context(), e.db, id)
		if err != nil || action != "" {
			t.Fatalf("sync %d: action %q, err %v; want nothing", i, action, err)
		}
	}
	if e.count("savings_suggestions") != 1 {
		t.Error("repeated syncs added suggestions")
	}
}

// --- atomicity -------------------------------------------------------------------

// The income and its suggestion commit together or not at all.
func TestAnIncomeIsNotRecordedIfItsSuggestionCannotBe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-10-01")
	e.rate("USD", "VES", "40.00", "2026-10-01")
	// Break the savings side: valuing a VES income must write a rate leg.
	if _, err := e.db.ExecContext(t.Context(), "DROP TABLE savings_suggestion_rate_legs"); err != nil {
		t.Fatalf("breaking the schema: %v", err)
	}

	_, err := e.app.RecordIncome(t.Context(), ledger.RecordIncomeInput{
		AccountID: e.ves, CategoryID: e.salary, Amount: "4000", PaymentMethodID: bankTransfer, EffectiveDate: "2026-10-05",
	})
	if err == nil {
		t.Fatal("the income was recorded although its suggestion failed")
	}
	if e.count("transactions") != 0 || e.count("account_movements") != 0 || e.count("savings_suggestions") != 0 {
		t.Error("a failed suggestion left part of the income behind")
	}
}

// --- dates -----------------------------------------------------------------------

// A suggestion belongs to the period of its income's EFFECTIVE date. An income
// dated in September and entered in October counts in September, although its
// suggestion was created -- and valued -- in October.
func TestASuggestionBelongsToItsIncomesPeriod(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.policy("20", "2026-09-01")
	id := e.income(e.usd, "800", "2026-09-30")

	s := e.live(id)
	if database.FormatDate(s.IncomeEffectiveDate) != "2026-09-30" || s.ValuedAt == nil || s.CreatedAt.IsZero() {
		t.Fatalf("dates: effective %s, valued %v, created %v", database.FormatDate(s.IncomeEffectiveDate), s.ValuedAt, s.CreatedAt)
	}
	october, err := e.svc.Overview(t.Context())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if october.SuggestedUSDMinor != 0 {
		t.Errorf("October counts %d, want 0", october.SuggestedUSDMinor)
	}
	september, err := e.svc.OverviewFor(t.Context(),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("OverviewFor: %v", err)
	}
	if september.SuggestedUSDMinor != 16000 {
		t.Errorf("September counts %d, want 16000", september.SuggestedUSDMinor)
	}
}

func ptr(s string) *string { return &s }
