# M8 contract — Credit & Debt Management

**Status:** M8.1 delivered, awaiting functional validation. M8.2–M8.5 are
planned in [m8-plan.md](m8-plan.md) (Spanish, rev. 2), which holds the full
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
   An automatic `SPEND` is postponed (D-12).
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
