# M7 contract — Savings Fund & Goals

M7 turns the ledger into a savings planner: a policy says what share of each
eligible income to save, the money is set aside against real USD accounts, and
it is assigned to goals. It is delivered in four submilestones, each approved
separately:

| Submilestone | Scope | Status |
| --- | --- | --- |
| **M7.1** | Savings policy & suggestions | Delivered (this document, §1–§9) |
| M7.2 | Savings fund: reservations, partial confirmation, availability | Planned |
| M7.3 | Goals: permanent and one-time, distribution, withdrawals | Planned |
| M7.4 | Ledger integration (expenses, transfers), reconciliation, dashboard | Planned |

This document is the agreement between the backend and the presentation layer
for what is built. It grows as each submilestone is approved.

## The rules the milestone hangs on

**Four things are never confused:**

| Concept | Where it lives | Moves money? |
| --- | --- | --- |
| Income recorded | `transactions` (`internal/ledger`) | Yes — it is the ledger |
| Saving suggested | `savings_suggestions` (M7.1) | **No** — a target |
| Money reserved | M7.2 | No — it marks money that is already in an account |
| Money assigned to a goal | M7.3 | No |

**The savings fund is a layer over the ledger, never a second ledger.** Nothing
in M7 adds a row to `transactions` or `account_movements`, adds a column to
them, or is read by `internal/valuation`. Reserving money cannot change the net
worth, by construction.

**Every M7 figure is in USD**, which is `savings.FundCurrency` and **not** the
valuation base of `app_settings`.

---

## 1. Eligibility (TD-076)

An income is saved from when, in this order:

1. its `transaction_type` is `INCOME` — the only accepted type, so a `TRANSFER`,
   an `FX_EXCHANGE` (a purchase of USD) and an `OPENING_BALANCE` are excluded by
   construction;
2. a policy version governs its effective date (it is on or after the policy's
   start);
3. an explicit **override** for that income says so — it wins in either
   direction, and always carries a reason;
4. otherwise its **category** is not excluded — every INCOME category is
   eligible by default;
5. and the rate in force is not 0%.

The description is never read. `IncomeDetail` states the decision and its
reason in words.

## 2. The policy (TD-077)

`SetPolicy({Rate: "20", EffectiveFrom: "2026-10-01"})`. The rate is a
percentage with at most two decimals, read as basis points without a float.

- The version applied to an income is the latest `effective_from` **on or before
  the income's effective date** — never by when anything was recorded.
- The **first** version may start in the past. Saving it creates nothing; the
  incomes it reaches appear in the sync preview (§6).
- Every later version starts **today or later**, never before the version in
  force. `ErrRetroactivePolicy` otherwise.
- A version no suggestion uses may have its rate corrected (same start date). A
  version that governs suggestions refuses with `ErrPolicyVersionInUse`. A policy
  that never produced a suggestion may move its start earlier.

## 3. The suggestion

Computed when an eligible income is recorded or corrected, **in the same
transaction** (TD-080): `application.RecordIncome`, `application.UpdateTransaction`.
The web adapter routes every income write through them.

```
income_usd_exact = ConvertMinorExact(amount, currency → USD, rate on income date)
income_usd_minor = RoundHalfEven(income_usd_exact)
suggested_minor  = RoundHalfEven(income_usd_exact × rate_bp / 10000)
```

Each figure is **one** rounding of an exact value; the suggestion is not derived
from the rounded income (1.06 VES at 10 VES/USD and 50% suggests 5 cents, not 6).

### FX (TD-078, TD-081)

- The rate is the **reference** rate in force on the income's effective date,
  resolved by `fx.Book` with M5's rules: latest `effective_date <=` the date,
  `MANUAL > BCV_OFFICIAL` within one date, identity / direct / inverted / one
  pivot. Never a later rate, never a forward-dated one, never today's rate for a
  past income.
- The valuation is **frozen**: the USD figures, the method and a copy of each
  observation used (`savings_suggestion_rate_legs`). A later correction of the
  rate is reported (`RateChanged`) and applied only by `RevalueSuggestion`.
- No rate → `PENDING_VALUATION` (`Pending = NO_RATE`), no amount. Never zero.
  `ValueSuggestion` values **the same row** once a rate dated on or before the
  income exists; `ValuePending` does it for all that can be.
- A future-dated income in another currency waits for its date
  (`Pending = FUTURE_DATE`); a USD one is valued at once.
- Buying USD is an `FX_EXCHANGE` and is never suggested from: the dollars it
  brings were counted when the bolívares were earned.

## 4. Revisions and statuses (TD-079)

One income has many revisions and **at most one live** (a partial unique index).
A correction never edits a suggestion's figures.

| Stored status | Live | Meaning |
| --- | --- | --- |
| `PENDING_VALUATION` | yes | waiting for a rate or for its date |
| `OPEN` | yes | valued; shown as "Pendiente de confirmar" |
| `DISMISSED` | yes | declined by the user, with a reason |
| `SUPERSEDED` | no | replaced by the next revision (the income or its rate changed) |
| `INVALIDATED` | no | the income stopped being eligible |

`PENDING_CONFIRMATION`, `PARTIALLY_CONFIRMED` and `CONFIRMED` are **derived** from
the reservations of M7.2, never stored.

What a correction of the income does:

| Correction | Effect |
| --- | --- |
| Amount, date, account (even to another currency), category, or crossing a policy version | live revision → `SUPERSEDED`; revision n+1 computed from the current facts |
| Description only | nothing |
| Moved to an excluded category, overridden out, corrected into an `EXPENSE` | live revision → `INVALIDATED` |
| An excluded income becomes eligible again, an expense corrected into an income | a new revision |
| A dismissed income changes | the new revision is `OPEN`: it is a new question |

There is no delete in this application; "deleting" an income means correcting it
into something else, and the table above covers it. History is never lost.

## 5. Dates

| Date | Meaning | Used for |
| --- | --- | --- |
| `income_effective_date` | when the income happened financially | the policy version, the rate, the period the suggestion counts in |
| `created_at` | when the revision was written | traceability |
| `valued_at` | when its rate was found | traceability |
| confirmation date | when money was reserved against it | M7.2 |

An income dated 30 September and entered on 15 October counts in **September**.

## 6. Sync and backfill (TD-080)

`PlanSync()` previews every income since the policy's start whose suggestion is
missing (`CREATE`), out of date (`REVISE`) or no longer warranted
(`INVALIDATE`), oldest first, with the reason and the rate that would apply. It
writes nothing.

`ApplySync(ids)` applies exactly the incomes named — those the user left ticked
— each re-planned inside the transaction. Idempotent: applying twice does
nothing the second time; an income not previewed is never touched.

It is the backfill after a retroactive first version, the way a category rule
reaches existing incomes, and the safety net for an income changed outside the
application. `SetIncomeOverride` applies to its income at once.

## 7. Concurrency

Writes open with `BEGIN IMMEDIATE` (TD-075). Within the process the single
connection serialises everything; against another process the second writer
waits at `BEGIN`, before it reads. The partial unique index is the database's
half of "never suggested twice"; Go checks for the live revision first and never
classifies a constraint error. Every read inside an income transaction goes
through that transaction's executor, never the pool.

## 8. Backend surface

`internal/savings.Service`:

| Method | Notes |
| --- | --- |
| `GetPolicy`, `SetPolicy` | §2 |
| `ListCategoryEligibility`, `SetCategoryEligibility` | INCOME categories only; changes reach existing incomes through the sync |
| `SetIncomeOverride(id, eligible, reason)` | reason required; syncs that income |
| `SyncIncomeTx(ctx, exec, id)` | for `internal/application`; runs in the caller's transaction |
| `ListSuggestions` | live revisions, newest income first, with `Pending` and `RateChanged` |
| `IncomeDetail(id)` | eligibility, reason, override, every revision with its legs |
| `ValueSuggestion(id)`, `ValuePending`, `RevalueSuggestion(id)`, `DismissSuggestion(id, reason)` | act on the income's live revision |
| `PlanSync`, `ApplySync(ids)` | §6 |
| `Overview`, `OverviewFor(from, to)` | the month by default |

Errors: `ErrInvalidInput`, `ErrNotFound`, `ErrIncomeNotFound`,
`ErrCategoryNotFound`, `ErrCategoryNotIncome`, `ErrRetroactivePolicy`,
`ErrPolicyVersionInUse`, `ErrInvalidState`, `ErrRateUnavailable`,
`ErrNotYetEffective`, `ErrRateUnchanged`. The adapter maps each to a web
sentinel.

`internal/ledger` gained, for M7 only: `RecordIncomeTx`, `UpdateTransactionTx`
(the same rules, run through a caller's transaction), `IncomeItem`,
`Repository.GetIncome`, `Repository.ListIncomesSince`. No behaviour changed.

### Overview figures (USD, month of the income's effective date)

| Figure | Definition |
| --- | --- |
| Tasa objetivo | the version in force today |
| Ingresos elegibles | Σ `income_usd_minor` of live valued revisions |
| Ahorro sugerido | Σ `suggested_usd_minor` of `OPEN` + `DISMISSED` |
| Pendiente de confirmar | Σ of `OPEN` (M7.2 subtracts what is confirmed) |
| Descartado | Σ of `DISMISSED` |
| Sin valorar | count, and the amounts in their own currencies — never estimated |
| Tasa corregida, por sincronizar | counts |

The policy's "tasa de ahorro objetivo" is **not** the recap's "tasa de ahorro"
(net cash flow over income), and the two are never labelled the same.

## 9. Presentation

| Route | Purpose |
| --- | --- |
| `GET /savings`, `GET /mobile/ahorro` | overview and live suggestions |
| `GET /savings/policy`, `GET /mobile/ahorro/politica` | policy, history, category eligibility |
| `POST /savings/policy` | new version; a refusal re-renders with the reason (422) |
| `POST /savings/categories/{id}/eligibility` | `eligible=0|1` |
| `GET /savings/sync`, `GET /mobile/ahorro/sincronizar` | preview |
| `POST /savings/sync` | `transaction_id` repeated, exactly the ticked incomes |
| `POST /savings/value-pending` | value every pending suggestion that now has a rate |
| `GET /savings/incomes/{id}`, `GET /mobile/ahorro/ingresos/{id}` | one income's eligibility and history |
| `POST /savings/incomes/{id}/override` | `eligible`, `reason` |
| `POST /savings/incomes/{id}/{value,revalue,dismiss}` | act on the live revision |

POSTs accept a `return` path limited to `/savings…` and `/mobile/ahorro…`, and
come back with a `msg` or `err` key whose text the server chooses from a fixed
list. Every amount arrives formatted and every allowed action (`CanValue`,
`CanRevalue`, `CanDismiss`) decided; a template compares no amount. "Ahorro" is
in the desktop navigation; mobile links it from Inicio; an income's edit page
links to its savings history.

## 10. What M7.2 inherits

- A confirmed reservation is **never** duplicated, deleted or reconciled
  automatically when its suggestion is superseded or invalidated. Reservations
  reference a revision; what is confirmed for an income is their sum across all
  of its revisions.
- Confirming more than the live revision's suggested amount, cumulatively, is
  refused unless the user records an explicit exception.
- A confirmation is idempotent through a form token and its own date column,
  distinct from the income's effective date.

## Not in M7.1

Reservations, confirmation, the fund, goals, distribution, expense and transfer
integration, reconciliation of reserves and the dashboard block — M7.2 to M7.4.
No automatic importer of historical rates, no background job, no cache, no
scheduled sync. No savings figure on `transactions`, and no rate on any
transaction.
