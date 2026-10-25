# M8 contract — Credit & Debt Management

**Status:** M8.1 delivered and validated. M8.2 delivered, awaiting functional
validation (with synthetic data until Banco Plaza's first real statement
arrives). M8.3–M8.5 are planned in [m8-plan.md](m8-plan.md) (Spanish, rev. 2), which holds the full
audit, the obligation and component models, the FX formulas and every worked
example. This document is the contract for what is **built**; each later
submilestone adds its section here when it is implemented.

## The rule the milestone hangs on

**A card is an account, and a debt is a negative balance.** Nothing about the
ledger's arithmetic changes: a purchase is one negative movement on the card, a
payment one positive movement on it (paired with the money leaving a bank
account), and the net worth falls by exactly what was bought on the day it was
bought. What M8 adds is what an account cannot say on its own — the issuer's
terms, which debits are financial charges, which refund belongs to which
purchase — as **facts and assertions**. Nothing calculated is stored: no debt,
no available credit, no cycle, no allocation of a payment (TD-092, M8.2).

## M8.1 — Credit accounts & liability ledger

### Schema (migrations 00019–00021)

| Change | Where | Decision |
| --- | --- | --- |
| `account_type` admits `CREDIT_CARD` | `accounts` (rebuilt) | TD-089 |
| `transaction_type` admits `REFUND`, shaped like `EXPENSE` | `transactions` (rebuilt) | TD-090 |
| The issuer's terms | `credit_cards` | TD-089 |
| Interest, fee, penalty | `credit_charges` | TD-091 |
| Which expense a refund gives back | `transaction_refunds` | TD-090 |
| Idempotent form submissions | `operation_requests` | TD-095 |

00019 rebuilds two **parent** tables with SQLite's twelve-step procedure and
in-SQL guards (TD-088). It is forward-only; the way back is the automatic
pre-migration snapshot. 00020 and 00021 are additive and reversible. No column
was added to `transactions` or `account_movements`.

### Nature: asset or liability

- `account.Type.Nature()` is `ASSET` for `BANK`, `CASH`, `WALLET`, `OTHER` and
  `LIABILITY` for `CREDIT_CARD`. It is derived, never stored, and never inferred
  from a balance's sign, a category or a name.
- An account never changes nature (`account.ErrNatureChange`, 422 on the form).
  A card's name, institution and archived status are edited as any account's;
  its terms on `/cards/{id}/edit`.
- The generic account form and `account.Service.Create` open assets only. A card
  is opened through `application.OpenCreditCard`: the account, its terms and an
  optional previous debt (entered positive, recorded as a negative
  `OPENING_BALANCE`), in one transaction.

### Terms (`internal/creditcard`)

| Term | Unknown is | Notes |
| --- | --- | --- |
| `statement_day` 1–31 | — (required) | A month without that day closes on its last day (M8.2). |
| `credit_limit_minor` | NULL | Read at the card currency's scale. Never zero, never "unlimited". |
| `due_days_after_statement` 1–60 | NULL | An unknown due date never makes anything overdue (M8.2). |
| `payment_processing_days` 0–10 | 0 | Informational until M8.5. |
| `denomination` `NOMINAL`/`INDEXED` + `denomination_confirmed` | `NOMINAL`, unconfirmed | Shown as an assumption until verified. M8.3 refuses `INDEXED`. |
| `default_payment_method_id` | NULL | Preselected on the purchase form. |

The **position** (`creditcard.Position`) is derived on every read from the
ledger as of today (TD-063) and the terms: `DebtMinor = max(0, −balance)`,
`CreditBalanceMinor = max(0, balance)`, and with a known limit
`AvailableMinor = limit + balance`, which may be negative (over the limit is
reported, never clamped). Future-dated movements are counted, not summed.

### What happens on a card

| Event | Transaction | Movements | Use case |
| --- | --- | --- | --- |
| Purchase | `EXPENSE` | card −x | `application.RecordCardPurchase` (or the generic expense form) |
| Interest / fee / penalty | `EXPENSE` + `credit_charges.kind` | card −x | `RecordCardCharge` / `UpdateCardCharge` |
| Payment, same currency | `TRANSFER` into the card | bank −x / card +x | the transfer form (`?to_account_id=`) |
| Payment, other currency | `FX_EXCHANGE` into the card | USD −a / card +b | the exchange form |
| Refund | `REFUND` (+ optional link) | card +x | `RecordRefund` / `UpdateRefund` |
| Cash advance | `TRANSFER` out of the card | card −x / bank +x | the transfer form |
| Previous debt | negative `OPENING_BALANCE` | card −x | `OpenCreditCard` |

"Payment", "purchase", "charge" are labels inferred from the type, the
direction and the `credit_charges` row (TD-091). Guards name what they accept
(TD-045): `UpdateTransaction` refuses a `REFUND` and the application refuses a
charge (`creditcard.ErrChargeEditedAsCharge`); the web layer redirects both to
their own forms.

### Refunds

- One **positive** movement under an **EXPENSE** category, on any account: on a
  card it lowers the debt, on a bank account it raises the balance.
- Never income: never in "what did I earn", never eligible for savings.
- In the cash flow it is reported as `EXPENSE` and nets against its category's
  spending on its own day (TD-068). M6 and M6.5 figures therefore change when a
  refund is recorded; this is intended. It is never among the largest expenses.
- The link to the original is optional. When present the original is an
  `EXPENSE` of the same account and the refunds of one expense never exceed it
  (`ledger.ErrRefundOriginal`, `ledger.ErrRefundExceedsOriginal`), checked after
  every write, edits of the original included. Unlinking is a correction of the
  assertion; no amount changes.

### Idempotency

Every M8 form carries a server-rendered request key. A replayed key returns
what the first submission produced and writes nothing (`operation_requests`,
TD-095). A 422 re-renders with the same key.

### Valuation

`NetWorth.AssetsMinor` and `NetWorth.LiabilitiesMinor` (owed, positive) are sums
of the same allocated account lines as the total, so
`AssetsMinor − LiabilitiesMinor == TotalMinor` exactly (I2). An account's
`Share` is its part of the **assets** and is undefined for a liability. A
currency component's share is still of the total. The dashboard lists accounts
in two groups, Activos and Pasivos.

### M7 under liabilities (TD-094)

1. An income is eligible only if credited to an **ASSET**, checked before the
   policy and before any override: cashback on a card is never saved from, and
   an inclusion override cannot change that.
2. Only USD **assets** back the fund. A USD card is out of the balance, the free
   figure, the table and `AccountImpact`, even holding a credit; reserving on it
   is `savings.ErrLiabilityAccount`; a plan with a card at either end is a 422 of
   the plan, and the same movement without the plan records (TD-086).
3. Paying a card from a USD account is an ordinary outflow: if it leaves the
   account below what is reserved, M7 reports `OVERALLOCATED` as it always has.
   From M8.2 the payment form offers to consume those reservations in the same
   operation (D-12, approved 2026-10-25; see M8.2 below).
4. An income moved onto a card has its live suggestion `INVALIDATED`;
   reservations confirmed against it are untouched and reported
   `OVER_CONFIRMED`.

Before M8 no liability existed, so none of this changes a suggestion,
reservation or issue M7 already holds.

### Routes

| Route | Use |
| --- | --- |
| `GET /cards`, `GET /cards/new`, `POST /cards` | List, open |
| `GET /cards/{id}` | Position, terms summary, movements with their labels |
| `GET/POST /cards/{id}/edit` | Terms |
| `GET /cards/{id}/purchases/new`, `POST /cards/{id}/purchases` | Purchase |
| `GET /cards/{id}/charges/new`, `POST /cards/{id}/charges` | Charge |
| `GET /cards/{id}/charges/{tid}/edit`, `POST /cards/{id}/charges/{tid}` | Correct a charge |
| `GET /refunds/new?account_id=&original=`, `POST /refunds` | Refund, any account |
| `GET /refunds/{id}/edit`, `POST /refunds/{id}` | Correct a refund |

PRG 303 on success, 422 on a refused form, 409 for a currency change that needs
confirmation, 404 for an id that is not a card, a charge or a refund.

A card's page opens the ledger's own forms with `return_to=/cards/{id}`: in
M8.1 a payment (`/transfers/new?to_account_id=`) and a payment from another
currency (`/exchanges/new?to_account_id=`, which preselects the card as
destination) — from M8.2 the page opens its own payment form instead, and
those query parameters still work — and the correction of a purchase or a payment (`/transactions/{id}/edit`, which
keeps the query when it hands over to the type's own screen). Those forms carry
it in a hidden field; Cancel, the back button and the redirect after a save all
lead there, and without it they lead to `/transactions` as before. The savings
pages that open a correction pass their own path the same way (an income's
page, a goal's linked expenses, the issues list), and so does the exchange
form's link to a transfer. `return_to`
is honoured only as a local path — `//host`, `/\host` and absolute URLs fall
back (`safeReturn`).

### Invariants M8.1 guarantees

| # | Invariant | Where |
| --- | --- | --- |
| I2 | Net worth = assets − liabilities, exactly | `valuation` + test |
| I3 | A card purchase touches no asset account | one movement, on the card |
| I4 | A payment is a `TRANSFER` or an `FX_EXCHANGE`, never income or expense | types, TD-068 |
| I8 | Nothing calculated is persisted | schema review: terms, facts, assertions |
| I10 | No income on a liability is eligible; no reservation is backed by one | TD-094 + tests |
| I11 | A double submission records once | `operation_requests` + tests |

### Not in M8.1

Statements, cycles, minimum and due amounts, payment allocation and
reconciliation (M8.2); FX analytics of the debt (M8.3); committed liquidity
(M8.4); the card dashboard, debt history, simulation and calendar facts (M8.5);
the mobile card screens. No `ADJUSTMENT` type (D-6), no network field (D-9), no
loans or instalments (M9).

## M8.2 — Statements & payments

### Schema (migration 00022, reversible)

| Change | Where | Decision |
| --- | --- | --- |
| What the issuer printed | `credit_statements` | TD-092 |
| "The bank billed this movement in that statement" | `credit_statement_assignments` | TD-092, plan R7 |
| What a refund after the closing does to the billed balance | `credit_cards.refund_policy` (`NEXT_STATEMENT` default, `REDUCES_BILLED`) | plan R2.5 |

A statement's balances follow the ledger's sign (negative = owed); its minimum,
payments, debits and past-due figures are magnitudes; anything the statement
does not print is NULL. `UNIQUE(card, closing_date)`; Go refuses an overlapping
period, a closing after today and a due date not after the closing. An
assertion's statement must close on or after the movement's date, and the
movement must move that card. No column was added to `transactions` or
`account_movements`, and no allocation, status or pending figure is stored.

### The calendar (`creditcard.ClosingDate`, `CalendarClosingFor`)

- `closing(m) = min(statement_day, last day of m)`: day 31 closes February on the
  28th, or the 29th in a leap year.
- A movement belongs to the first cycle closing **on or after** its date: the
  closing day is inside the cycle, the next day is in the next one, and late
  December belongs to January.
- A registered statement replaces the calendar closing nearest to it (and any
  calendar closing inside its period): a bank that closed on the 23rd opens no
  one-day cycle on the 25th.
- An assertion replaces the rule for one movement. If the movement's date is
  later corrected past the statement's closing, the assertion is ignored and
  reported (`ASSERTION_BEFORE_MOVEMENT`); one whose transaction no longer moves
  the card is reported too (`ASSERTION_ORPHAN`).
- Membership is derived on every read and never stored. `effective_date` stays
  the date of the operation: it alone decides the rate and the history.

### Layer O: obligations (`creditcard.Derive`, TD-092)

A pure, deterministic function of the card's terms, statements, movements and
assertions as of a date. Movements dated after it take part in nothing
(TD-063). Roles are structural (TD-044): `PAYMENT` is a `TRANSFER` or
`FX_EXCHANGE` into the card, `REFUND` a refund, `DEBIT` any other negative
movement, `CREDIT` any other positive one.

| Figure | Rule |
| --- | --- |
| Statement in force `k` | The last cycle closing **before** the date. On the closing day the cycle is still open. |
| Owed at a closing | The bank's `B_j` when registered; else the ledger's `L_j` (labelled computed). `O_j = max(0, owed)`. |
| Billed pending | `max(0, O_k − P_k − R_k·[REDUCES_BILLED])`; also shown net of refunds when there are any. |
| Minimum pending | `max(0, M_k − P_k)`; unknown when the statement has none. Refunds never count. |
| Overdue `V` | The **maximum** of `M_j − P_j` over cycles whose due date has passed. Only a known due date and a known minimum make a cycle overdue. |
| Due date | The statement's; else `closing + due_days_after_statement` (estimated); else unknown. |
| Missed minimum, billed unpaid at the due date | Facts of the cycle, with payments made on or before the due date. |
| Prepaid | `max(0, P_k − billed)`. |
| Status | `OPEN`, `PAID`, `OVERDUE`, `MINIMUM_MET`, `DUE_UNKNOWN`, `PARTIALLY_PAID`, `BILLED`, in that order of precedence. |

**What a payment settled.** Each payment is placed on the line of the statement
in force on its date, after the earlier payments `p` on that line:
`[0, V)` overdue (naming the overdue cycles), `[V, M)` minimum, `[M, O)` rest of
the billed balance, `[O, O+U)` prepaid (U: what the open cycle owes so far),
beyond that a credit balance. The lines are the intersection with `[p, p+x)`:
they always add up to the payment exactly (I5, a property test over generated
facts). When the bank's figures are out of order — a minimum below what is
overdue or above the billed balance — the thresholds are bounded and the payment
is flagged `Clamped` with a warning. With no statement in force a payment is
prepaid or a credit balance. Nothing of this is stored; a back-dated purchase or
a corrected statement re-derives every allocation (E6, E7).

The scenarios of plan R2.4 are tests with their numbers: E1 (700 overdue + 300
minimum from one payment, then V = 0, minimum 1,500, billed 17,500), E2
(prepaid), E3 (refund under both policies, minimum untouched), E4 (charges are
debits of their cycle), E5 (credit balance), E6 (retroactive purchase: B
unchanged, Δ from 3,000 to 0), E7 (corrected minimum).

### Reconciliation (`Standing.Reconcile`)

Per registered statement: `Δ = B − L`. When the bank reported them, the
difference is broken down into previous balance, payments (compared with the
ledger's payments, refunds and other credits of the cycle) and debits, with
whatever none of them explains; the bank's past-due amount is compared with
the one derived at the closing from the earlier statements, which tests whether
the minimum nests what is overdue (plan R2.3, RR1). The page lists the
statement's movements and, as candidates, the card's movements within five days
of the closing that belong to a neighbour; one that happened on or before the
closing can be asserted into it, and an assertion can be withdrawn. It informs
and never corrects by itself.

### Use cases

| Use case | Where | Notes |
| --- | --- | --- |
| `RecordStatement`, `UpdateStatement` | `creditcard.Service` | Idempotent on the natural key (card, closing): the same statement twice returns the first; a different one is `ErrStatementExists` (409). |
| `AssertMembership` | `creditcard.Service` | One domain only, so not `internal/application` (a deviation from plan §5.2). A nil statement withdraws. |
| `Standing`, `Reconcile`, `NextStatementDates` | `creditcard.Service` | Three queries per card, whatever its history. |
| `RecordCardPayment` | `internal/application` | `TRANSFER` (same currency) or `FX_EXCHANGE` (both amounts) into the card, from an **asset**; request key (TD-095); savings plan (D-12). |

### Paying from USD: the savings plan (D-12)

The payment form loads the savings hint (`/savings/impact?type=CARD_PAYMENT`)
for its source, with the amount that LEFT it — the USD of an exchange. It
offers to consume reservations of that account in the same operation:
`SPEND` (the money was saved to pay the card) or `RELEASE` with a reason, linked
to the payment, never more than left the account nor more than is reserved
(`savings.ApplyCardPaymentPlanTx`). A plan on a payment that is not from a USD
asset into a liability is `ErrNotLinkable`, a 422 of the plan; the payment
without the plan records (TD-086). Without a plan, M7 reports `OVERALLOCATED`
as before. M7's reconciliation follows a `SPEND` to its payment: corrected to
leave another account, to stop going to a card, or to be smaller than what it
spent, it is a `SPEND_MISMATCH`. M7's own suite is unchanged.

### Routes

| Route | Use |
| --- | --- |
| `GET /cards/{id}/payments/new`, `POST /cards/{id}/payments` | Pay: suggested amounts (overdue, minimum, billed, total debt) that only fill the field; the second amount appears when the source is in another currency |
| `GET /cards/{id}/statements` | Obligations, cycles (registered and computed) with their status, and what each payment settled with its trace |
| `GET /cards/{id}/statements/new[?period_start=&closing_date=]`, `POST /cards/{id}/statements` | Record a statement; a computed cycle can be registered from the list |
| `GET /cards/{id}/statements/{sid}/edit`, `POST /cards/{id}/statements/{sid}` | Correct one |
| `GET /cards/{id}/statements/{sid}/reconcile` | Reconciliation |
| `POST /cards/{id}/statements/{sid}/assertions` | `action=assign` / `clear` |

The card's page shows the obligations panel and opens the payment form; the
ledger's transfer and exchange forms still accept `?to_account_id=`. The card's
terms gain the refund policy.

### Invariants M8.2 guarantees

| # | Invariant | Where |
| --- | --- | --- |
| I5 | A payment's allocation is an exact partition of its amount | `Derive` + property test |
| I8 | Nothing calculated is persisted: no allocation, status or pending figure | schema: statements and assertions only |
| I11 | A double submission records once | payment: `operation_requests`; statement: its closing |

### Not in M8.2

The components layer and the FX effect of a debt (M8.3); committed liquidity
(M8.4); the card dashboard, debt history, simulation, calendar facts and the
mobile card screens (M8.5). Interest is never computed: the billed balance
unpaid at the due date is shown as the base the bank would charge it on.
