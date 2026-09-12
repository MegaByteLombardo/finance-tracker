# M5 contract — backend ↔ presentation

**Status:** frozen · **Milestone:** M5 — Multi-Currency, Exchange Rates & Current Net Worth

This document extends [m2-contract.md](m2-contract.md), [m3-contract.md](m3-contract.md)
and [m4-contract.md](m4-contract.md), which **remain in force except where this
document contradicts them**. Everything they froze — money as decimal strings,
dates as `YYYY-MM-DD`, the optional-field convention, `errors.Is` for error
handling, balances derived from movements, editing as replacement — still holds.

M5 adds exchange rates, a base currency of valuation, and one derived figure. It
changes nothing about the ledger.

## The one rule, unchanged

**The presentation layer never writes SQL, never opens a transaction, never
imports a repository, and never performs financial arithmetic.**

M5 sharpens the last clause. Every figure below arrives already computed in
integer minor units and already formatted. A template must never add two amounts
together, never apply a rate, and never divide anything.

---

## 1. The rule that governs this whole milestone

```
Reference / Valuation FX        ≠        Transaction FX
```

| | What it is | Does it exist? |
| --- | --- | --- |
| **Reference FX** | What a source said one currency was worth in another, on a given day. Used to express balances the user already holds in one currency, for presentation. | **Yes — this is all of M5.** |
| **Transaction FX** | The rate a particular operation actually happened at: a card's rate on a purchase, a merchant's rate, the rate a cross-currency transfer settled at. | **No.** Not in M5, not implied by anything in M5. |

`transactions` gained **no** `exchange_rate_id`, and no transaction carries a
rate. Nothing in this contract may be attached to one. Conflating the two would
make it impossible to say later which one a stored number meant.

---

## 2. Orientation, fixed for the whole project

```
1 BaseCurrencyCode  =  Rate  QuoteCurrencyCode
```

```
base = USD, quote = VES, rate = "842.20670000"   →   1 USD = 842.2067 VES
```

The fields are `BaseCurrencyCode` / `QuoteCurrencyCode` / `Rate`. They are **not**
`from` / `to` / `value`: from/to invites a reader to assume a direction of
conversion that the row does not state. Direction is the caller's question, and
the backend answers it.

**A template must never reverse a rate by relabelling it.** To show the other
direction, ask the backend; the inverse is computed exactly and is not the same
string with the codes swapped.

### Two different things called "base"

| Term | Meaning | Where |
| --- | --- | --- |
| the **base of a pair** | one half of an observation: the `1` side of `1 USD = 842.2067 VES` | `ExchangeRate.BaseCurrencyCode` |
| the **valuation base** | the currency the consolidated net worth is expressed in | `AppSettings.BaseCurrencyCode`, `NetWorth.BaseCurrencyCode` |

They are never called the same thing in prose. In the interface, the second is
always "moneda base".

---

## 3. Numbers

Everything in M5 is exact. There is no `float32`, no `float64` and no SQLite
`REAL` anywhere in the path, and a test parses the source of every package on
that path to keep it that way.

| Value | Representation crossing the boundary |
| --- | --- |
| a **rate** | a canonical decimal **string**, exactly as stored: `"842.20670000"` |
| an **amount** | integer minor units, plus a pre-formatted string, as in M2 |

**Rates keep their scale.** `"842.20670000"` has eight decimal places because
that is the precision the Banco Central published at, and the trailing zeros are
part of the statement. Display the string; never reformat it for storage, never
round it, and never parse it into a JavaScript number.

**Canonical form for input:** digits, optionally one `.`, digits. No sign, no
thousands separators, **no comma**. A comma is the provider's problem to
normalise out of somebody else's document, not the user's to type — the same
refusal M3 established for amounts, where `"1,5"` is rejected rather than
silently becoming `15`.

### Rounding

Conversion is exact rational arithmetic, rounded **once**, **half to even**, into
the destination currency's minor units.

It is rounded **per currency, not per account**. The consequence is visible and
is the reason for the choice: the components a screen shows **add up exactly to
the total that screen shows**. Converting account by account would accumulate up
to half a unit of error per account and produce a page whose lines do not sum to
its own total.

Half to even rather than half away from zero because this is a *valuation*, not a
payment: nothing changes hands, the same balances are revalued many times a day,
and half-up would bias every one of those revaluations upward. It is also
symmetric about zero, so a negative balance — which transfers may legitimately
produce (TD-043) — rounds by the same rule as a positive one.

---

## 4. `RateSource`

```go
type RateSource string

const (
    RateSourceManual      RateSource = "MANUAL"
    RateSourceBCVOfficial RateSource = "BCV_OFFICIAL"
)
```

`Label()` gives the display name (`"Manual"`, `"BCV"`). Use it rather than
spelling the names in a template, so the two interfaces cannot disagree.

**There is no `BCV_MIRROR`.** A third-party mirror was evaluated for M5 and left
out; see §11.

### The presentation layer never chooses a source

`CreateManualRateInput` **has no `Source` field**, and neither does the domain
input behind it. The backend sets `MANUAL`. A form submission cannot claim to be
the Banco Central, and a handler must not add a field for it. This is tested.

---

## 5. Precedence

**`MANUAL` outranks `BCV_OFFICIAL` — within one `effective_date`, and never
across dates.**

```
1. find the latest effective_date <= the valuation date, from any source
2. among the observations OF THAT DATE, take the highest-precedence source
```

The restriction in the second half is the whole decision. Applied across dates, a
manual rate typed once in January would shadow every publication for the rest of
the year, and the user's net worth would quietly stop moving with no symptom at
all. Scoped to its own day, a manual rate wins exactly where it claims to apply.

**Precedence must be visible.** When a valuation used a manual rate over a
published one, `RateResolutionView.UsesManualOverride` is true and the interface
says so. A total that silently preferred a typed rate would be impossible to debug
from the screen.

---

## 6. Types

### `ExchangeRate` — one stored observation

```go
type ExchangeRate struct {
    ID                int64
    BaseCurrencyCode  string
    QuoteCurrencyCode string
    Pair              string      // "USD/VES", precomputed
    EffectiveDate     time.Time   // the ECONOMIC date, YYYY-MM-DD at UTC midnight
    Rate              string      // canonical decimal, at the published precision
    Source            RateSource
    SourceLabel       string
    IsEditable        bool        // true only for MANUAL
    IsActive          bool        // false when archived
    FetchedAt         *time.Time  // nil for MANUAL
    IsUpcoming        bool        // EffectiveDate is still in the future
}
```

**`EffectiveDate` is not `FetchedAt`.** They are different facts and they
disagree in both directions:

- the BCV holds one Fecha Valor across a weekend, so Friday's rate is still
  correct on Sunday;
- the BCV publishes **forward** — a page read on Friday 2026-09-11 carried a
  Fecha Valor of Tuesday 2026-09-15.

**`IsUpcoming`** exists because of that second case. Such an observation is real,
is stored, and **may be shown, labelled as upcoming** — but it takes **no part in
any valuation before its date**. See §8.

### `AppSettings`

```go
type AppSettings struct {
    BaseCurrencyCode string
    UpdatedAt        time.Time
}
```

### `NetWorth` — the consolidated figure

```go
type NetWorth struct {
    BaseCurrencyCode string
    BaseCurrencyName string
    BaseSymbol       *string
    BaseMinorUnit    int

    ValuationDate    time.Time   // today's business date in Venezuela

    TotalMinor       int64
    TotalFormatted   string

    Complete         bool
    Components       []NetWorthComponent
    Missing          []MissingRateView
    RatesAsOf        time.Time   // the OLDEST observation date behind any component
}

type NetWorthComponent struct {
    CurrencyCode, CurrencyName string
    CurrencySymbol             *string
    MinorUnit, AccountCount    int

    BalanceMinor, ConvertedMinor         int64
    BalanceFormatted, ConvertedFormatted string

    Valued     bool                 // false when no rate could be found
    Resolution *RateResolutionView  // nil exactly when Valued is false
}

type MissingRateView struct {
    FromCurrencyCode, ToCurrencyCode string
    BalanceFormatted                 string
}
```

**It is derived on every read and never stored.** There is no
`current_net_worth`, no `converted_balance`, no `cached_net_worth`, no table and
no in-memory cache. A handler must not cache it either.

**`RatesAsOf` is the oldest observation behind any component**, not the freshest,
so "Tasas al …" is true of the whole figure rather than of its most recent part.

### `RateResolutionView` — where the number came from

```go
type RateResolutionView struct {
    Method             RateMethod   // IDENTITY | DIRECT | INVERTED | CROSS
    MethodLabel        string
    Legs               []RateLegView
    EffectiveDate      time.Time
    UsesManualOverride bool
}

type RateLegView struct {
    BaseCurrencyCode, QuoteCurrencyCode string
    Pair, Rate                          string
    EffectiveDate                       time.Time
    Source                              RateSource
    SourceLabel                         string
    Inverted                            bool
}
```

`Legs` is empty for `IDENTITY`, one long for `DIRECT` and `INVERTED`, two long
for `CROSS`.

This is not decoration. **Nobody can verify a net worth by hand**, so every
component has to be able to say which source said what, effective on which day,
and whether anything was inverted or divided.

---

## 7. How a conversion is resolved

In this order, stopping at the first that works:

| Method | What it uses |
| --- | --- |
| `IDENTITY` | `from == to`. Exactly 1, no lookup, **no stored row**. `USD/USD` does not exist in the database and must not. |
| `DIRECT` | one observation of the pair, as published |
| `INVERTED` | one observation of the reverse pair, inverted exactly |
| `CROSS` | two observations sharing **one** pivot currency, divided |

### Inversion

The application stores **what a source published** and never its mirror.
`1/842.2067` does not terminate as a decimal, so a stored inverse would be a
rounded second copy of one fact that could then disagree with the original. The
inverse is computed as an exact rational at the moment of use.

### Cross rates

```
from → to  =  (from → pivot) ÷ (to → pivot)

EUR → USD  =  (EUR/VES) ÷ (USD/VES)  =  977.87777730 ÷ 842.20670000
```

This is what makes a valuation base other than VES work at all: the BCV quotes
everything against the bolívar, so with `USD/VES` and `EUR/VES` published, no
direct `EUR/USD` observation is needed.

**One pivot, never two.** A figure on a dashboard arrived at through four hops is
one nobody can check, and a chain nobody can check is worse than a gap that says
so. When more than one pivot is available, the one whose **stalest leg is the
freshest** wins, ties broken by currency code — a chain is only as current as its
oldest link.

**Nothing derived is persisted.** A cross rate is a function of two rows; a stored
copy could end up contradicting both.

---

## 8. Rate lookup, and the two rules that are not negotiable

```
the latest observation whose effective_date <= the valuation date
```

**`<=`, never `=`.** A publication holds until the next one, so a Sunday is
valued with Friday's rate and no row is invented for the weekend. Filling gaps
physically would also confuse a *published* rate with a *carried-forward* one.

**`<=`, never `>`.** An observation whose Fecha Valor is still in the future has
not taken effect. Using it would value today with tomorrow's number — look-ahead
bias — and would make every figure this application ever produces about a past
date depend on information that did not exist then. There is **no exception and
no fallback**: if the only observation for a pair is dated later than the
valuation date, that currency is **missing**, not approximated.

Archived observations are excluded, which is how archiving a manual override
restores the published rate underneath it.

---

## 9. A missing rate is reported, never guessed

When a currency cannot be converted to the valuation base:

- `Complete` is `false`;
- the currency appears in `Missing`, with how much is sitting unvalued;
- the component is **still listed**, with its own balance intact — the user holds
  that money whether or not this application can price it;
- `Valued` is `false` and `Resolution` is `nil`;
- **nothing is treated as zero.**

`TotalMinor` is then the sum of what *could* be valued. **It is a partial total
and the interface must say so.** Presenting it as the user's net worth would be a
lie, and a zero would silently shrink the figure — the one outcome worse than an
incomplete one.

This is **not an error**. An error would deny the user the part of their net
worth that is known. The only errors from a valuation are genuine failures: the
database, or a valuation base that no longer exists.

```
Falta una tasa JPY → USD para valorar 50000 JPY.   [Agregar tasa]
```

---

## 10. Services

```go
type ExchangeRateService interface {
    List(ctx, in ListExchangeRatesInput) ([]ExchangeRate, error)
    Get(ctx, id int64) (ExchangeRate, error)
    CreateManual(ctx, in CreateManualRateInput) (ExchangeRate, error)
    UpdateManual(ctx, id int64, in UpdateManualRateInput) (ExchangeRate, error)
    SetManualActive(ctx, id int64, isActive bool) (ExchangeRate, error)
    RefreshNow(ctx) (RefreshRatesResult, error)
    ProviderEnabled() bool
}

type SettingsService interface {
    Get(ctx) (AppSettings, error)
    SetBaseCurrency(ctx, code string) (AppSettings, error)
}

type ValuationService interface {
    GetCurrentNetWorth(ctx) (NetWorth, error)
}
```

### Manual rates

| Operation | Semantics |
| --- | --- |
| **create** | `ErrRateConflict` if one already exists for that pair, date and source. A person typing a value that is already there has not been told it is there, so they are told rather than silently obeyed. |
| **update** | **the value only.** The pair and the effective date are the *identity* of an observation: changing them names a different observation rather than correcting this one. There is no field for them. |
| **archive / restore** | `SetManualActive`. Archiving is how an override is withdrawn: the published rate underneath applies again, and the record that somebody disagreed survives. |
| **delete** | **does not exist**, here or anywhere else in this application. |

Both currencies must **exist**. Neither has to be **active** — an active account
denominated in an archived currency is a reachable state, and refusing to record
the rate that values it would leave that money permanently unvaluable.

### A published rate is not editable

`IsEditable` is false for `BCV_OFFICIAL`, and `UpdateManual` / `SetManualActive`
return `ErrRateNotEditable` for one. That row records what was published; editing
it would make this application claim the source said something it did not. The
way to disagree with a publication is a **manual rate for the same day**, which
wins by precedence and can itself be corrected or withdrawn.

### Changing the base currency rewrites nothing

`SetBaseCurrency` changes **no transaction, no movement and no balance**. Each
account keeps its own currency and its own amounts; only the presentation of the
consolidated figure changes. The interface says this next to the control, and a
test compares every ledger row before and after.

Only an **active** currency may be chosen. A base currency archived *after* it was
chosen keeps working — the rule applies at the moment of the choice, exactly as it
does for accounts.

---

## 11. The provider

`RefreshNow` fetches the Banco Central de Venezuela's published page over plain
HTTP and parses the DOM. There is **no headless browser, no Chromium, no Python
and no sidecar**: the page is server-rendered and the values are in the HTML.

**All or nothing.** A publication is one document with one Fecha Valor. Either all
of its usable observations are stored or none are, in a single transaction.
Storing half would let a valuation combine one currency's rate from this
publication with another's from a previous one and produce a total that looks
entirely ordinary and is a blend of two days.

**Required pairs: `USD/VES` and `EUR/VES`.** If either is missing or malformed,
the whole fetch fails. Other currencies on the same page (CNY, TRY, RUB) are
read opportunistically; a malformed one is dropped with a warning and cannot fail
the fetch, because a broken yuan block says nothing about the dollar rate.

**A currency this application does not have is skipped, never created.** It is
counted in `RefreshRatesResult.Skipped`. Which currencies exist is the user's
decision, not a scraped document's.

**No mirror.** A third-party mirror was evaluated for M5 and rejected on evidence:
the historical one no longer resolves at all, and the reachable one publishes four
decimal places where the BCV publishes eight, under a date field that is not the
Fecha Valor. Historical backfill is manual until a source worth trusting exists.

### A provider failure is not a server error

| Situation | Sentinel | What the interface does |
| --- | --- | --- |
| unreachable — DNS, TLS, timeout, non-200 | `ErrProviderUnavailable` | re-render the page with a message |
| reached, but the answer could not be trusted | `ErrProviderInvalidResponse` | re-render with a different message; this usually means the page changed shape |

**Neither produces a 500.** Both re-render with HTTP 200. Every stored rate stays
visible and manual entry stays available — the external source is a convenience,
never a dependency.

### Automatic refresh

At most **one successful fetch per Venezuelan calendar day**, attempted first
shortly after the server starts listening and then on a four-hourly tick. The
refresher starts **after** the HTTP server is listening and never blocks startup.

**Finance Tracker starts and works with no network at all.** Accounts, the ledger,
balances, manual rates and the net worth computed from already-stored rates are
all available the moment the listener opens.

---

## 12. Dates

`effective_date` is a **calendar date**, `YYYY-MM-DD`, never a timestamp — the
same convention `transactions.effective_date` has used since M2 (TD-028).

**The valuation date, and every date a form defaults to, is today in Venezuela
(UTC−04:00)**, not UTC. Between 20:00 and midnight in Caracas, UTC is already
tomorrow: a UTC "today" would ask for rates effective on a day the user has not
reached, and pre-fill forms with tomorrow's date.

---

## 13. Errors

M5 adds seven sentinels. The M2, M3 and M4 ones remain in force.

| Sentinel | When | HTTP |
| --- | --- | --- |
| `ErrInvalidRate` | not a canonical decimal, or not greater than zero | 422 |
| `ErrRateNotFound` | no observation with that id | 404 |
| `ErrRateConflict` | one already exists for that pair, date and source | 409 |
| `ErrSameCurrencyPair` | base and quote are the same currency | 422 |
| `ErrRateNotEditable` | edit or archive attempted on a published rate | 409 |
| `ErrProviderUnavailable` | the source could not be reached | **200**, with a message |
| `ErrProviderInvalidResponse` | the source answered with something untrustworthy | **200**, with a message |
| `ErrBaseCurrencyUnavailable` | the valuation base or the settings row is gone | 500 |

Reused: `ErrCurrencyNotFound`, `ErrCurrencyInactive`, `ErrInvalidInput`,
`ErrNotFound`.

Matched with `errors.Is`. `ErrBaseCurrencyUnavailable` is an integrity failure —
migration `00012` creates the settings row and a `CHECK` makes a second one
impossible — so it is a 500, never a message on a form.

---

## 14. Routes

```
GET  /settings                              base currency, and links onward
POST /settings/base-currency

GET  /settings/exchange-rates               list + manual form + refresh
GET  /settings/exchange-rates?archived=1    including withdrawn overrides
POST /settings/exchange-rates               create a manual rate
POST /settings/exchange-rates/{id}          correct a manual rate's value
POST /settings/exchange-rates/{id}/toggle   archive / restore
POST /settings/exchange-rates/refresh       fetch the BCV now

GET  /settings/data                         (unchanged, M3)

GET  /mobile/configuracion
GET  /mobile/tasas
```

---

## 15. What the presentation layer builds

### The net worth, on the dashboard and on mobile

```
Patrimonio neto
1,420.00 USD                          Base: USD · Tasas al 2026-09-11

USD   1,000.00      →  1,000.00       misma moneda
VES   50,000.00     →    100.00       tasa invertida · USD/VES = 500 · BCV · 2026-09-11
EUR   200.00        →    220.00       tasa cruzada · EUR/VES = 550 · USD/VES = 500
```

It must show the valuation base, "Tasas al …", and, per component, how it was
valued — including a visible mark when a **manual override** was used.

When `Complete` is false it must show the warning and a link to
`/settings/exchange-rates`.

### What it must NOT do

- Add, scale, convert or round any amount. Every figure arrives computed.
- Sum balances across currencies. The per-currency balances are the ledger's own
  and are never added together; the consolidated figure is a separate,
  clearly-labelled *valuation*.
- Reverse a rate by swapping the labels around it.
- Invent a trend. **M4's mobile header carried a hard-coded "+2.4%" with nothing
  behind it. It is gone, and nothing replaces it in M5**: a real change needs the
  net worth at a past date, which belongs to the historical milestone.
- Send a `source` field. There is nowhere for it to go.
- Treat a provider failure as a broken page.

---

## 16. Out of scope for M5

Cross-currency transfers · FX attached to a transaction · merchant or card FX ·
realized and unrealized FX gains · the historical net-worth chart · a third-party
mirror · Playwright, Chromium or any browser automation · an external scraper
service · a public REST API · budgets · forecasting · credit and debt · recurring
transactions · **any delete** · audit history of edits.

---

## Changing this contract

The same rule as M2, M3 and M4: this document is changed **deliberately and
visibly**, in the same commit as the code that changes with it. If the two sides
disagree, this is what they are both wrong or right against.
