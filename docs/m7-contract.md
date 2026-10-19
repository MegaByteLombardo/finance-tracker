# M7 contract — Savings Fund & Goals

M7 turns the ledger into a savings planner: a policy says what share of each
eligible income to save, the money is set aside against real USD accounts, and
it is assigned to goals. It is delivered in four submilestones, each approved
separately:

| Submilestone | Scope | Status |
| --- | --- | --- |
| **M7.1** | Savings policy & suggestions | Delivered (this document, §1–§9) |
| M7.2 | Savings fund: reservations, partial confirmation, availability | Delivered (§11) |
| M7.3 | Goals: permanent and one-time, distribution, withdrawals | Delivered (§12) |
| M7.4 | Ledger integration (expenses, transfers), reconciliation, dashboard | Delivered (§13) |

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

## 11. M7.2 — The savings fund (TD-082, TD-083)

**The fund is a sum over entries, never an account.** An operation (`RESERVE`,
`RELEASE`; `REALLOCATE`, `REBACK`, `SPEND` arrive in M7.3–M7.4) has one or more
signed entries on USD accounts. Nothing is edited or deleted.

| Figure, per USD account | Definition |
| --- | --- |
| Balance | the ledger's balance today (TD-063) |
| Reserved | Σ entries on the account |
| Free | balance − reserved (negative when overallocated) |
| Overallocated | max(0, reserved − max(0, balance)) |

The fund totals sum those, with **free** counting only positive free balances,
and **unallocated** = reserved − assigned to goals (all of it until M7.3).

**`Reserve({RequestKey, IncomeTransactionID?, Lines, Exception, Reason})`**

- Each line's account must exist, be in USD and be active; the amounts per
  account must fit its free balance, read inside the transaction.
- With an income: its live revision must be `OPEN`; what is confirmed for the
  income across all its revisions plus this amount may not exceed the live
  suggested amount, unless `Exception` with a `Reason` (stored as
  `is_exception`). `ErrExceedsSuggestion` otherwise.
- Without an income: a manual contribution (savings that predate M7).
- The request key is generated when the form renders; the same key replays the
  operation it produced and writes nothing.
- The operation is dated today (Venezuela): **the confirmation date**, distinct
  from the income's effective date and from `created_at`.

**`Release({RequestKey, AccountID, Amount, Reason})`** — reason required; never
more than reserved there. An archived account may release, never back new money.

**Derived on every suggestion:** `ConfirmedUSDMinor` (this revision),
`IncomeConfirmedUSDMinor` (the income, all revisions), `RemainingUSDMinor`,
`OverConfirmedUSDMinor`, `Confirmation` = `PENDING_CONFIRMATION` /
`PARTIALLY_CONFIRMED` / `CONFIRMED`. Dismissing applies to the remainder and is
refused once fully confirmed.

**Overview, six figures that are never mixed:**

| Figure | Dated by |
| --- | --- |
| Suggested; confirmed; remaining; declined; over-confirmed | the income's effective date |
| Confirmation progress = confirmed ÷ suggested (bp) | the income's effective date |
| Gross contributions (all `RESERVE`), withdrawals (`RELEASE` + `SPEND`), net change | the operation's effective date |
| Reserved, free, overallocated, unallocated | today |

**Routes:** `GET /savings/fund`, `GET /mobile/ahorro/fondo`;
`POST /savings/reserve` (`request_key`, `account_id`, `amount`, optional
`income_id`, `exception=1`, `reason`); `POST /savings/release` (`request_key`,
`account_id`, `amount`, `reason`). The income page carries the confirmation form,
prefilled with what remains.

## 12. M7.3 — Goals (TD-084, TD-085)

A goal's money is Σ of the entries that name it; an entry naming no goal is the
fund's unallocated part. **Status is the lifecycle only:**

```
ACTIVE ⇄ PAUSED
ACTIVE | PAUSED → COMPLETED   (ONE_TIME only)
ACTIVE | PAUSED → CANCELLED
```

Closing moves what the goal holds, per account, to unallocated with one
`REALLOCATE`, in the same transaction, idempotent on the request key. A paused or
closed goal accepts no money; a closed one keeps its history.

**Derived per goal:** reserved, remaining = max(0, target − reserved), funding
(`UNFUNDED` / `PARTIAL` / `FUNDED` / `OVERFUNDED`), progress in basis points,
and — for a one-time goal with a date — the suggested monthly contribution:
remaining ÷ calendar months from this month to the target's (at least 1),
rounded half to even. FUNDED is never COMPLETED.

**Targets:** editing appends to `savings_goal_target_revisions` when the target
changes; `OriginalTargetUSDMinor` is the first revision.

**Money in and out of goals:** `Reserve` lines and `Release` take an optional
`GoalID`; `Reallocate({AccountID, FromGoalID?, ToGoalID?, Amount})` moves
between two goals of one account or between a goal and unallocated, never more
than is there, net zero.

**Distribution:** `SuggestDistribution(amount)` → one line per active goal with
a share, plus the unallocated line. Exact share, capped by need, rest
unallocated, `money.Allocate` for the cents. Σ shares of open goals ≤ 100%
(`ErrDistributionExceeded`). The confirmation form prefills the lines; posted as
`line_goal` / `line_amount` pairs, empty or zero lines dropped, and what is
confirmed is their sum.

**Routes:** `GET|POST /savings/goals`; `GET|POST /savings/goals/{id}`;
`POST /savings/goals/{id}/{pause,resume,complete,cancel}` (`request_key`,
`note`); `POST /savings/reallocate`; mobile `/mobile/ahorro/metas[/{id}]`.

## 13. M7.4 — The ledger meets the fund (TD-086, TD-087)

**Never refused.** An expense or a transfer is recorded whatever it does to
savings. `GET /savings/impact` (the form's own fields, by htmx) shows the
account's free USD and the shortfall — `min(max(outflow − max(free, 0), 0),
reserved)` — and the reservation slices it could use; empty for any account
outside the fund or any other operation.

| Plan | Operation | Rules |
| --- | --- | --- |
| Expense: the money was for this (`savings_purpose=goal`) | `SPEND`, linked to the expense | from slices of the expense's USD account; no reason needed |
| Expense: cover an extraordinary expense (`cover`) | `RELEASE`, linked to the expense | reason mandatory, checked first |
| Transfer: move the backing | `REBACK`, linked to the transfer | from the source's slices to the destination, net zero per goal; destination active |

Σ uses ≤ the movement. Atomic with the movement (`application.RecordExpense`,
`RecordTransfer`); idempotent on `savings_key` (suffixed per operation). Form
fields: `use_goal` / `use_amount` (expense), `move_goal` / `move_amount`
(transfer), `savings_purpose`, `savings_reason`, `savings_key`.

**Goal cost.** `LinkedExpenses` = expenses with a `SPEND` for the goal;
`SpentMinor` what the goal paid, `ActualCostMinor` their whole amount,
`DeviationMinor = actual − original target`. The target is never changed.

**Issues** (derived; key = kind + subject + amount):

| Kind | When |
| --- | --- |
| `OVERALLOCATED` | an account's reserved money exceeds max(0, its balance today) |
| `OVER_CONFIRMED` | confirmed for an income > its live suggestion, or confirmed with no live revision |
| `SPEND_MISMATCH` | a `SPEND`'s expense is no longer an expense, moved account, or is smaller than what it spent |
| `ARCHIVED_BACKING` | an archived account backs reserved money |

`AcknowledgeIssue(key, note)` stores a note; nothing else changes. The fund and
the dashboard keep the overallocated figure until it is resolved by an ordinary
operation.

**History.** `FundHistory()`: per month, contributions (`RESERVE`), withdrawals
(`RELEASE` + `SPEND`), net, closing reserved balance — from one read of the daily
deltas. Per goal: in, out, closing per month.

**Routes:** `GET /savings/impact`; `GET /savings/issues`,
`/mobile/ahorro/incidencias`; `POST /savings/issues/acknowledge`. The dashboard
and mobile Inicio show the savings block once a policy or a reservation exists.

## 14. Edge cases

| # | Case | Behaviour |
| --- | --- | --- |
| 1 | VES income with no rate for its date | `PENDING_VALUATION`, no amount, never zero; valued in place when a rate ≤ its date exists |
| 2 | VES income whose rate is corrected later | frozen valuation; `RateChanged` reported; `RevalueSuggestion` opens a new revision |
| 3 | Income edited after its suggestion | new revision from the current facts; the old one `SUPERSEDED` |
| 4 | Income "deleted" (corrected away) after a confirmation | revision `INVALIDATED`; reservations untouched; `OVER_CONFIRMED` issue |
| 5 | Purchase of USD | an `FX_EXCHANGE`, never income; no suggestion |
| 6 | Partial confirmation | the rest stays pending on the same suggestion |
| 7 | Double click on a confirmation | the request key replays the operation |
| 8 | Two reservations on one account at once | `BEGIN IMMEDIATE` + free balance read in the transaction: at most what is free |
| 9 | USD account with too little free | reservation refused (`ErrInsufficientFree`) |
| 10 | Expense causing overallocation | recorded; shortfall shown; release with reason, or an `OVERALLOCATED` issue |
| 11 | USD transfer from an account with reserves | backing movable with `REBACK`; otherwise an issue on the source |
| 12 | Goal already funded | gets nothing from a suggested distribution; the excess stays unallocated |
| 13 | Goal cancelled with money | its money moves to unallocated in the same action |
| 14 | Completed goal costing more than its target | real cost and deviation shown; target untouched |
| 15 | Goal without a date | valid; no suggested monthly contribution |
| 16 | Permanent goal used and refilled | FUNDED → PARTIAL after a withdrawal → FUNDED again; never COMPLETED |
| 17 | Reallocation between goals | one `REALLOCATE`, net zero, history kept |
| 18 | Rounding cents in a distribution | exact parts, largest remainder, capped parts never exceeded; Σ = amount |
| 19 | Retroactive change of the savings rate | only the first version may start in the past |
| 20 | Recomputing history without touching confirmations | revisions and the previewed sync never move a reservation |

## Not in M7

Investments, bank synchronisation, automatic transfers or exchanges, category
budgets, notifications, optimisers and predictions. Savings in any currency but
USD. No background job, no cache, no scheduled sync, no stored issue. No savings
figure on `transactions`, and no rate on any transaction.
