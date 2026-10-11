# M6 contract — backend ↔ presentation

**Status:** frozen · **Milestone:** M6 — Historical Net Worth & Analytics Dashboard

This document extends [m2-contract.md](m2-contract.md) through
[m5.5-contract.md](m5.5-contract.md), which **remain in force except where this
document contradicts them**. Two M5 rules are superseded and say so below:
which accounts are valued (§3) and what a "current" balance is (§2).

M6 adds no table, no column, no index and no migration. Schema version stays 13.

## The one rule, unchanged

**The presentation layer never writes SQL, never opens a transaction, never
imports a repository, and never performs financial arithmetic.** M6 makes that
rule carry more weight than any milestone before it, because a dashboard is
nothing but figures. Every figure on it is computed by `internal/valuation` and
`internal/analytics` and arrives formatted. Handlers parse a period and render.
Templates format nothing that is not already a string. JavaScript computes
nothing.

The one computation in `internal/web` is **geometry**: where a point sits on the
chart, computed from integer minor units with integer arithmetic (§8).

---

## 1. The question

> What was everything I hold worth at the close of a given day, in my base
> currency, using only the information and the rates available for that day?

`NetWorth(date)` answers it for one day and `NetWorthSeries(from, to)` for every
day of a range. They are **the same computation**: the headline figure is the
series asked for today.

## 2. Time

| Concept | Where it lives | What it means |
| --- | --- | --- |
| Financial date | `transactions.effective_date`, `exchange_rates.effective_date` | The calendar day an event or a rate applies to. The only dates any figure is computed from. |
| Technical timestamp | `created_at`, `updated_at`, `fetched_at` | When this application wrote or saw something. Never used as a financial date. |
| Today | `database.TodayAt(now)` | Today on the Venezuelan calendar (TD-051). Read from an injected clock in `ledger`, `valuation` and `analytics`. |

**A movement affects every date on or after its `effective_date`, and no date
before it.** That holds for every type — the opening balance, income, expenses,
both halves of a transfer, both halves of an exchange — because all of them are
dated by their transaction. `accounts.created_at` plays no part.

**Current means as of today** (TD-063, supersedes the M2 balance definition):

```
current balance = SUM(movements WHERE effective_date <= today)
```

This is the ledger's own rule, applied by `GetAccountBalance`,
`ListAccountBalances` and `ListCurrencyTotals`, so the account screens, the
dashboard and the valuation cannot disagree. A future-dated movement still
exists, is listed and can be edited; it is counted in
`AccountBalance.FutureMovementCount` and `NetWorth.FutureMovementCount`, and the
interface says "N movimientos futuros". There is no projected balance.

## 3. Which accounts are valued

**All of them, active or archived** (TD-064, supersedes M5's "active accounts
only"). Archiving is visibility, not a financial event: it hides an account and
stops new movements, and it changes no figure, past or present. An archived
account whose balance is not zero is reported (`NetWorthAccount.ArchivedWithBalance()`,
`NetWorth.ArchivedWithBalance`) and the interface warns about it. Archiving is
never refused.

The breakdown lists an account when it is active or holds a non-zero balance on
the valuation date.

## 4. Rates

Unchanged from M5 in every rule, now implemented once (TD-066):

- the latest observation with `effective_date <= D` applies — **never** `>`,
  **never** `=` only, and with **no maximum age**;
- `MANUAL > BCV_OFFICIAL` **within one effective date**, never across dates;
- identity, direct, inverted, or a cross through one pivot; inverses and crosses
  are computed exactly and never stored.

`fx.Book` is the implementation; `fx.Service.Resolve` and the historical series
both use it. Every resolution carries the effective dates of the observations it
used (`Resolution.EffectiveDate()`, `NetWorth.RatesAsOf`), so a screen can show
how old a rate is.

**Missing rates** (TD-067):

| Balance on D | Rate on D | Result |
| --- | --- | --- |
| zero | any, or none | Valued at exactly zero. Complete. |
| non-zero | found | Converted, rounded once, half to even. |
| non-zero | none | **Incomplete.** `Complete = false`, `Missing` names the currency and the amount. Never zero, never a later rate, never a guess. |

On the chart an incomplete day is a **gap**. A change whose endpoints include an
incomplete day is **unavailable**, with its reason. No historical rates exist
before the first BCV fetch; past rates are entered manually through the rates
screen, and the gaps close as they are.

## 5. Valuation (`internal/valuation`)

```go
func (s *Service) GetCurrentNetWorth(ctx) (NetWorth, error)
func (s *Service) GetNetWorthAt(ctx, on time.Time) (NetWorth, error)
func (s *Service) GetNetWorthSeries(ctx, from, to time.Time) (Series, error)
func (s *Service) Load(ctx, through time.Time) (*Snapshot, error)

func (s *Snapshot) NetWorthAt(on time.Time) (NetWorth, error)
func (s *Snapshot) Series(from, to time.Time) (Series, error)
func (s *Snapshot) ConvertToBaseExact(amountMinor int64, currency string, on time.Time) (*big.Rat, bool, error)
```

A `Snapshot` is loaded with five queries (settings, currencies, accounts, daily
deltas, rates) and answers everything else in memory. It cannot answer for a date
after `through` (`ErrInvalidRange`), because the rates that take effect later
were never read.

`NetWorth` gains `Accounts []AccountComponent`, `ArchivedWithBalance` and
`FutureMovementCount`; each `Component` and `AccountComponent` gains a `Share` in
basis points. `Series` is one `Point{Date, TotalMinor, TotalFormatted, Complete,
Missing}` per day, plus `IncompleteDays`. `Series.Gaps()` groups the days'
`Missing` into runs of consecutive days, one `Gap{FromCurrencyCode,
ToCurrencyCode, From, To, Days}` per currency and run, so a screen can say WHICH
rates are missing and not only how many days. Under the lookup rule, an
observation dated a gap's `From` covers the whole run; nothing is filled in on
the user's behalf.

**Rounding** (TD-047, TD-070): each currency's balance is converted exactly and
rounded once; the total is the integer sum of the currency lines. Within a
currency, the account lines are allocated from the rounded currency figure by
largest remainder, so accounts add up to their currency to the minor unit. Shares
are allocated to exactly 10,000 basis points, and are defined only for a complete,
positive total.

## 6. Analytics (`internal/analytics`)

```go
func NewService(valuation *valuation.Service, ledger *ledger.Service, now func() time.Time) *Service
func (s *Service) Dashboard(ctx, preset Preset) (Dashboard, error)
func (s *Service) MonthToDateCashFlow(ctx) (CashFlow, error)
func (s *Service) CashFlow(ctx, period Period) (CashFlow, error)
func MonthToDate(today time.Time) Period
```

### Periods (TD-069)

| Preset | Current | Previous |
| --- | --- | --- |
| `7D`, `30D`, `90D`, `1Y` | 7, 30, 90, 365 days ending today, inclusive | Same length, ending the day before the current starts |
| `ALL` | From the first movement (or today) to today | None |

On 2026-10-05, `30D` is 2026-09-06 → 2026-10-05 and its previous period is
2026-08-07 → 2026-09-05.

### Figures

| Figure | Definition |
| --- | --- |
| Net worth | `NetWorthAt(today)`. Equal to the last point of the series. |
| Change | `NW(to) − NW(from − 1 day)`; percentage over `|NW(from − 1)|`. Unavailable if either endpoint is incomplete; percentage undefined when the base is zero. |
| Income, expenses | `INCOME` and `EXPENSE` transactions only. Never `TRANSFER`, `FX_EXCHANGE` or `OPENING_BALANCE` (TD-068). |
| Conversion of a flow | At the reference rate of **its own** `effective_date`; summed exactly per category, rounded once per category; totals are the sums of the lines. |
| Net cash flow | `income − expenses`. |
| Savings rate | `(income − expenses) / income`, basis points. **Undefined** when income is zero or the cash flow is incomplete — never NaN, never infinity. |
| Spending | Total expenses; daily average = total ÷ days, rounded once; top 5 categories plus "Otras categorías", each with its share and its change against the previous period. The change is unavailable only when an **expense** of either period has no rate on its day; an unvalued income leaves the cash flow incomplete but not the spending comparison. Each `MissingFlow` carries `ExpenseFrom`/`ExpenseTo`, so the screen lists the unvalued expenses with the rate to load. |

Every ratio is `Ratio{BasisPoints int64, Defined bool}`. Every change is
`Change{Defined, Reason, AmountMinor, AmountFormatted, Percent}` with `Reason`
one of `INCOMPLETE` or `NO_PREVIOUS_PERIOD` when it is not defined.

### Cost

One `Dashboard` call issues **six queries whatever the period**: the snapshot's
five and one for the flows of the current and previous period together. Building
a series is O(days × currencies) in memory; five years of daily points is about
1,800 iterations.

## 7. The presentation surface (`internal/web`)

```go
type AnalyticsService interface {
	Dashboard(ctx context.Context, preset DashboardPreset) (DashboardAnalytics, error)
	MonthToDateCashFlow(ctx context.Context) (CashFlow, error)
}
```

`Services.Analytics` is it. `ParseDashboardPreset` turns `?period=` into a preset,
and anything unrecognised is the default, `30D`. An unknown preset reaching the
adapter is `ErrInvalidInput`.

`DashboardAnalytics` carries the period, `NetWorth` (with `Accounts`,
`ArchivedWithBalance`, `FutureMovementCount`), `Points`, `IncompleteDays`,
`Gaps` (`RateGap`: the run, computed over `Opening` AND the period, because a rate
missing on the baseline day is what makes `Change` unavailable even when the chart
has no hole; `PairLabel` in the `USD`-first orientation, and a
`FormURL` to the prefilled manual-rate form),
`Opening`, `Change`, `CashFlow`, `PreviousCashFlow` and `Spending`, every amount
both as minor units and formatted. `Ratio.Formatted` is the percentage with two
decimals ("7.24").

`AccountBalance` gains `FutureMovementCount`.

## 8. The interface

### Routes

| Route | Change |
| --- | --- |
| `GET /?period=` | The dashboard. One `Analytics.Dashboard` call. |
| `GET /mobile?period=` | The mobile Inicio. The same call. |
| `GET /mobile/movimientos` | The month's summary is `Analytics.MonthToDateCashFlow`: first of the month to today, the dashboard's rules, in the base currency. |
| `GET /settings/exchange-rates?base=&quote=&date=` | Prefills the manual-rate form. Unknown currencies and malformed dates are ignored; the POST validates as before. |
| `POST /accounts/{id}/archive` | Sets the account inactive. Never refused for a non-zero balance. Honours a local `return_to`. |
| `POST /accounts/{id}/restore` | Sets it active again. Same redirect rule. |

The period selector is plain links that htmx enhances with `hx-select`, swapping
only the analytics block of the same full page. A deep link and a click render the
same server output; the page works without JavaScript. There is no JSON endpoint.

### The chart

An inline `<svg>` with a 1000 × 240 viewBox, laid out by `BuildNetWorthChart`
from integer minor units with integer arithmetic (`big.Int` for the scaling). An
incomplete day breaks the line; a single-day run is a dot. Labels are the
backend's formatted totals. No charting library (TD-070).

### Desktop

Period selector; KPIs (net worth with change and percentage, income, expenses,
net cash flow and savings rate); the history chart with its gap note, which lists
each missing rate's pair and date range with a link to load it; warnings for
an incomplete total, an incomplete cash flow, future movements and archived
accounts with a balance; the breakdown by currency (with how each was valued) and
by account; the cash flow by category; spending with its comparison; recent
transactions.

### Mobile

The same figures: net worth with its change, the history chart with period pills,
and the cash flow and spending card. **The weekly-budget placeholder is gone** —
budgets are not a feature of this application, and the card suggested they were.

### Archiving

The archive and restore routes name the state they set; posting either twice
is harmless. The desktop accounts list and edit page, and every mobile account
card, offer the action. The edit page shows the current balance and says it will
keep counting in the net worth; the mobile accounts screen lists archived accounts
in their own section rather than hiding them, because an archived account can
still hold money (§3).

### Templates may only

Show strings, choose a CSS class from a boolean, and prefix a sign or a `%` with
`signedChange`, `signedRatio` and `ratio`. They never add, subtract, divide or
convert.

## 9. Not in M6

Budgets and spending limits, alerts of any kind, anomaly detection,
notifications, credit and debt, forecasting, recommendations, a projected
balance, an automatic importer of historical BCV rates, a maximum age for rates,
snapshot tables, caches, background jobs, a JSON API, a charting library,
production deployment, **any delete**, and audit history of edits.
