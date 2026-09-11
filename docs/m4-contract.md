# M4 contract — backend ↔ presentation

**Status:** frozen · **Milestone:** M4 — Internal Transfers

This document extends [m3-contract.md](m3-contract.md) and
[m2-contract.md](m2-contract.md), which **remain in force except where this
document contradicts them**. Everything they froze — the money rules, the date
rules, the optional-field convention, `errors.Is` for error handling, balances
derived from movements, editing as replacement — still holds.

M4 adds one transaction type and two use cases. It breaks nothing.

## The one rule, unchanged

**The presentation layer never writes SQL, never opens a transaction, never
imports a repository, and never performs financial arithmetic.**

---

## 1. What M4 adds

A **transfer** moves money between two of the user's own accounts. It is the
first transaction with more than one movement, which is what the ledger's two
tables were built for.

```
transactions
  id               = X
  transaction_type = 'TRANSFER'
  category_id      = NULL
  payment_method_id= NULL
  description      = NULL | text
  effective_date   = 'YYYY-MM-DD'
        │
        ├── account_movements   source account        amount_minor = -25000
        └── account_movements   destination account   amount_minor = +25000
```

The two movements are equal in magnitude and opposite in sign, so they sum to
zero. **A transfer therefore never changes the total held in its currency.** That
invariant is the point of the type, and the UI must never present a figure that
contradicts it.

Nothing was added to `transactions`. There is no `source_account_id`, no
`destination_account_id` and no `amount` column, because the two movements
already state all three and a second copy could only ever disagree.

### `Internal Transfer` ≠ the payment method named `Bank transfer`

These are different things and must be presented as different things.

| | What it is | Where it lives |
| --- | --- | --- |
| **`TRANSFER`** | Money moved between **two accounts the user owns**. Not income, not an expense. Net effect on the user's wealth: zero. | `transaction_type` |
| **"Bank transfer"** | The **channel** an operation with the **outside world** used — how a salary arrived, how a bill was paid. | a row in `payment_methods` |

Paying rent by bank transfer is an `EXPENSE` whose payment method is
"Bank transfer". Moving $250 from Mercantil to Efectivo is a `TRANSFER` with no
payment method at all. Never label one with the other's vocabulary.

---

## 2. `TransactionType`

```go
const (
    TransactionTypeIncome         TransactionType = "INCOME"
    TransactionTypeExpense        TransactionType = "EXPENSE"
    TransactionTypeOpeningBalance TransactionType = "OPENING_BALANCE"
    TransactionTypeTransfer       TransactionType = "TRANSFER"   // new in M4
)

func TransactionTypes() []TransactionType                    // all four
func TransactionTypesForIncomeExpenseForm() []TransactionType // INCOME, EXPENSE
```

`TRANSFER` was **appended**, so the order of the existing three is unchanged.

### Knowing the value does not make it offerable

**This is a requirement, not a style note.**

```
Income / Expense form   →  INCOME | EXPENSE only
Transfer                →  RecordTransfer / UpdateTransfer, /transfers/* routes
Opening balance         →  the account-opening workflow
```

- The generic transaction form **must not present `TRANSFER`** as a choice, and
  **must not be able to submit it**. Build its type control from
  `TransactionTypesForIncomeExpenseForm()`, never from `TransactionTypes()`.
- The backend enforces this independently: `POST /transactions` with
  `type=TRANSFER` returns **422** and records nothing, and
  `GET /transactions/new?type=TRANSFER` returns **400**. Before M4 an
  unrecognised type was silently coerced to `EXPENSE`, which would have filed an
  attempted transfer as an expense against one account. It no longer is.
- `UpdateTransaction` refuses a transaction that **is** a transfer, and refuses
  `TRANSFER` as a target type. `UpdateTransfer` refuses everything that is not a
  transfer. Both return `ErrTypeChangeNotAllowed`.

A transfer is never converted to or from anything. `TRANSFER → INCOME`,
`EXPENSE → TRANSFER`, `TRANSFER → OPENING_BALANCE` and the reverse of each are
all impossible: `UpdateTransferInput` has no `Type` field, so there is no
signature through which to attempt one.

---

## 3. Recording a transfer

```go
func (s *Service) RecordTransfer(ctx context.Context, in RecordTransferInput) (TransactionView, error)

type RecordTransferInput struct {
    SourceAccountID      int64
    DestinationAccountID int64
    Amount               string  // positive, canonical format: "250.00"
    Description          *string // nil for none; "" is rejected
    EffectiveDate        string  // "YYYY-MM-DD", required
}
```

There is no `CategoryID` and no `PaymentMethodID` — not fields left empty, but
absent from the struct, so a transfer cannot be given either. There is no `Type`
either: this input records a transfer and nothing else.

**The direction is which account was named as the source, never the sign.**
`Amount` must be positive; `"-250.00"` is `ErrInvalidAmount`, not a way to
reverse the direction. The backend applies the signs, which is what makes a
transfer that takes money out of its destination unrepresentable.

### Rules, in the order they are checked

The order is part of this contract, so the same mistake always produces the same
error:

1. `Description` — trimmed; `nil` is fine, `""` or whitespace is `ErrInvalidInput`, over 255 characters is `ErrInvalidInput`.
2. **Source account** — must exist and be active.
3. **Destination account** — must exist and be active.
4. **The two must differ** — otherwise `ErrSameAccount`.
5. **The two must share a currency** — otherwise `ErrCurrencyMismatch`.
6. **Amount** — parsed at the shared currency's `MinorUnit`, must be `> 0`.
7. **Effective date** — a real calendar date in `YYYY-MM-DD`.

Everything happens in **one database transaction**. Either the transaction and
both movements exist, or none of them do. There is no state in which a transfer
has a debit and no credit.

### Same currency only

```
Mercantil USD  →  Efectivo USD    ✓
Banesco VES    →  Efectivo VES    ✓
Mercantil USD  →  Banesco VES     ✗   ErrCurrencyMismatch
```

A cross-currency transfer needs a source amount, a destination amount and the
rate between them. M4 has none of those and invents none of them; it belongs to
the FX milestone. **Do not build a rate field, a "convert" toggle, or a second
amount input.**

### Any pair of account types

`BANK → CASH`, `BANK → WALLET`, `CASH → BANK`, `WALLET → CASH`, and every other
combination, work identically. `account_type` classifies where money sits and has
no accounting meaning. There are no special rules per type and none will be
added.

### Negative balances are allowed

A transfer may leave an account overdrawn. **This is deliberate and the UI must
not block it.**

The ledger records what happened. Bank statements show overdrafts; a cash account
that goes negative usually means a movement is missing rather than that the user
broke a rule. A "sufficient funds" check would also be a rule that only sometimes
applied, because `EffectiveDate` can be backdated: a transfer entered today can
make last week's balance negative whatever is checked at the moment of writing.

There is no overdraft setting and no warning threshold. The UI may **style** a
negative balance distinctively; it must not refuse one.

---

## 4. Editing a transfer

```go
func (s *Service) UpdateTransfer(ctx context.Context, id int64, in UpdateTransferInput) (TransactionView, error)

type UpdateTransferInput struct {
    SourceAccountID      int64
    DestinationAccountID int64
    Amount               string
    Description          *string
    EffectiveDate        string
    AllowCurrencyChange  bool
}
```

**A replacement, not a patch.** Every editable field is sent on every call, and
the service revalidates **exactly** the seven rules above. An edit that could
produce a state a creation would refuse would be a second door into every
invariant.

Mutable: source, destination, amount, effective date, description.
Immutable: the type, and the transaction's id.

The transaction and **both** movements are rewritten in one database transaction,
so the two balances are never briefly inconsistent with each other.

### The movement ids survive an edit

`Transfer.SourceMovementID` and `Transfer.DestinationMovementID` are the same
after an edit as before it, even when the source and destination are swapped. An
edit corrects an event; it does not replace it with a different one. Presentation
may rely on this.

### Changing the currency of a transfer

Both accounts must always share a currency. Separately, if the **new pair's**
currency differs from the one the transfer currently uses, the amount is read at
a different scale rather than converted — `"250.00"` is a different sum of money
in USD and in VES — so the reinterpretation must be deliberate:

> If the new pair's currency differs and `AllowCurrencyChange` is false, the
> service returns **`ErrCurrencyChangeNotConfirmed` and writes nothing.**

Show a confirmation naming the currency the amount will be read in, then retry
with the flag set. Do not set it unconditionally. This is the same rule
`UpdateTransaction` already applies, for the same reason (TD-040).

`AllowCurrencyChange` does **not** permit a cross-currency transfer.

### Archived accounts

- A transfer that already exists is **always readable**, even if one or both of
  its accounts were archived afterwards. Listings and the dashboard show it
  normally.
- A transfer **cannot be created or edited** with an archived account on either
  end. A financial edit revalidates as a creation, so both resulting accounts
  must be active.

**Consequence to surface in the UI:** if one of a transfer's accounts was
archived later, that transfer can no longer be edited *at all* — not even to fix
its description. The way through is to unarchive the account, edit, and archive
it again. This is not new behaviour: M3's income and expense editing has worked
this way since it shipped.

### Editing is destructive

M4 keeps no previous version of an edited transfer, exactly as M3 keeps none of
an edited transaction. There is no audit log and no history table. The undo is a
backup. Do not build UI implying an edit can be reverted.

### There is no delete

No `DeleteTransfer`, no `CancelTransfer`, no `ReverseTransfer`. Do not offer one.

---

## 5. Reading a transfer

### `TransactionView.Transfer` is the whole story

```go
type TransactionView struct {
    // ... everything M2 and M3 froze, unchanged ...

    // Set if and only if Type == TransactionTypeTransfer.
    Transfer *TransferDetail
}

type TransferDetail struct {
    SourceMovementID  int64
    SourceAccountID   int64
    SourceAccountName string

    DestinationMovementID  int64
    DestinationAccountID   int64
    DestinationAccountName string

    AmountMinor     int64  // the amount that moved, always POSITIVE
    AmountFormatted string // rendered at the currency's scale, unsigned
}
```

> **For a `TRANSFER`, the complete semantic representation lives in
> `TransactionView.Transfer`. The movement-centric fields of `TransactionView`
> must not be used to render the amount or the endpoints of a transfer.**

Concretely, for a transfer:

| Field | What it actually holds | Use it? |
| --- | --- | --- |
| `Transfer.*` | the whole transfer | **yes** |
| `AmountMinor`, `AmountFormatted` | the **source** movement, so **negative** | no |
| `AccountID`, `AccountName`, `AccountType`, `Institution` | the **source** account only | no |
| `MovementID` | the source movement's id | no |
| `CurrencyCode`, `CurrencySymbol`, `MinorUnit` | the shared currency | **yes** |
| `EffectiveDate`, `Description`, `ID`, `CreatedAt` | the event | **yes** |
| `CategoryID/Name/Type`, `PaymentMethodID/Name` | always `nil` | — |

Rendering `AmountFormatted` for a transfer shows `-250.00` attributed to one
account, which is half of what happened. Use `Transfer.AmountFormatted`.

`Transfer` is `nil` for `INCOME`, `EXPENSE` and `OPENING_BALANCE`, and that nil
is how a template knows which branch to take.

### One row per transfer

`ListTransactions` and `GetTransaction` return **exactly one row per
transaction**, transfers included. A transfer is one financial event and appears
once, never once per side.

The listing is still a single query with no N+1, and its order is unchanged:
`effective_date DESC, id DESC`.

**One semantic change:** `ListTransactionsInput.Limit` counts **transactions**.
Before transfers existed that was the same as counting movements; it no longer
is. Asking for 20 gets 20 rows.

### A malformed transfer is an error, not a half-rendering

The read path validates a stored transfer before returning a `TransferDetail`,
and returns `ErrInconsistentTransfer` rather than presenting part of one. It
refuses: a missing source movement, a missing destination movement, a
non-negative source, a non-positive destination, magnitudes that do not cancel
out, both movements in the same account, and a movement count other than two.

This cannot be triggered by any input — every write establishes the shape inside
one database transaction, and nothing deletes a movement. It means the database
was changed by something else. Treat it as **500**, log it, and do not build a
recovery flow for it.

### Balances are unchanged

`GetAccountBalance`, `ListAccountBalances` and `ListCurrencyTotals` have the same
signatures and the same semantics. A balance is still `SUM(amount_minor)`,
computed on read. There is no `balance`, `cached_balance` or `current_balance`
column and there will not be one.

After a transfer the source card goes down, the destination card goes up, and the
**per-currency total does not move**. Do not recompute a total in the browser;
render `TotalFormatted` as given.

---

## 6. Errors

Distinguish with `errors.Is`. Never compare or parse error strings.

| Sentinel | Meaning | Suggested status |
| --- | --- | --- |
| `ErrSameAccount` | source and destination are the same account | 422 |
| `ErrCurrencyMismatch` | the two accounts are in different currencies | 422 |
| `ErrAccountNotFound` | one of the two accounts does not exist | 422 |
| `ErrAccountInactive` | one of the two accounts is archived | 422 |
| `ErrInvalidAmount` | not positive, unparseable, or more decimals than the currency has | 422 |
| `ErrInvalidInput` | bad date, blank description, malformed input | 422 |
| `ErrTypeChangeNotAllowed` | the wrong use case for this transaction's type | 422 |
| `ErrCurrencyChangeNotConfirmed` | the pair's currency changes and the flag was not set | 409 |
| `ErrNotFound` | no transaction with that id | 404 |
| `ErrInconsistentTransfer` | a stored transfer is malformed — an integrity failure, not a user error | **500** |

Everything M2 and M3 declared still applies.

### Which account field to mark

A transfer form has two account fields, and a sentinel alone cannot say which one
is wrong. Account errors are therefore wrapped in a typed error that carries the
side:

```go
type TransferSide string

const (
    TransferSideSource      TransferSide = "source"
    TransferSideDestination TransferSide = "destination"
)

type TransferAccountError struct {
    Side      TransferSide
    AccountID int64
    Err       error // ErrAccountNotFound or ErrAccountInactive
}
```

Both work at once:

```go
_, err := svcs.Ledger.RecordTransfer(ctx, in)

var accountErr *web.TransferAccountError
switch {
case errors.As(err, &accountErr):
    field := "source_account_id"
    if accountErr.Side == web.TransferSideDestination {
        field = "destination_account_id"
    }
    form.Errors[field] = "..."          // errors.Is(err, web.ErrAccountInactive) still matches
case errors.Is(err, web.ErrSameAccount):
    form.Errors["destination_account_id"] = "Choose a different account."
case errors.Is(err, web.ErrCurrencyMismatch):
    form.Errors["destination_account_id"] = "Both accounts must be in the same currency."
}
```

---

## 7. What the presentation layer builds

Routes are presentation's decision. These are the flows the contract must
support, and the recommended spelling follows the existing convention — M3 uses
`POST /transactions/{id}` for updates, not `PATCH`:

```
GET  /transfers/new        the form
POST /transfers            create      → RecordTransfer
GET  /transfers/{id}/edit  the form, populated
POST /transfers/{id}       update      → UpdateTransfer
```

**No REST/JSON API.** Server-rendered HTML and htmx, as everywhere else.

### The form

```
From account      required, active
To account        required, active, same currency as From, not the same account
Amount            required, positive, canonical format
Currency          derived from the accounts — read-only, never an input
Effective date    required
Description       optional
```

### Lookups: nothing new is needed

`AccountService.List(ctx, false)` already returns every active account with its
`ID`, `Name`, `Type`, `Institution` and `CurrencyCode`. Filter the destination
list in the browser to accounts where

```
CurrencyCode == source.CurrencyCode  &&  ID != source.ID
```

The backend revalidates all of it and remains the source of truth. There is no
new endpoint and none is needed.

### Populating the edit form

`GetTransaction(id)` returns everything:

- source and destination from `Transfer.SourceAccountID` / `DestinationAccountID`
- the amount from `Transfer.AmountFormatted` — **positive**, ready to put in the
  input
- date and description from the view itself

### Dashboard

Recent activity should read as one event with two ends:

```
Transfer
Mercantil USD → Efectivo USD
250.00 USD
```

Built from `Transfer.SourceAccountName`, `Transfer.DestinationAccountName`,
`Transfer.AmountFormatted` and `CurrencyCode`. Do not show a category or a
payment method for it — there is none, and inventing a placeholder that looks
like one would be a lie about the data.

### Money in the browser, unchanged

M3's rules stand and are worth restating because a transfer form is a new place
to break them:

- Send the canonical format: digits, an optional single `.`, at most `MinorUnit`
  decimals. **No thousands separators, no symbol.**
- Never `parseFloat`, and never strip characters to make an amount parse.
  `strings.ReplaceAll(amount, ",", "")` turns `1,5` into `15`.
- Derive decimal precision from `MinorUnit`, which every view model carries.
- Render `AmountFormatted` / `BalanceFormatted` / `TotalFormatted` as given.

---

## 8. What the presentation layer must not do

Everything in M2's and M3's lists, plus:

- Offer `TRANSFER` in the income-and-expense form, or submit it there.
- Offer a category or a payment method on a transfer form.
- Offer a currency, an exchange rate, a second amount, or a "convert" control.
- Render `TransactionView.AmountFormatted` or `AccountName` for a transfer.
- Block a transfer that would overdraw an account.
- Offer delete, cancel or reverse for a transfer.
- Set `AllowCurrencyChange` without asking the user.
- Compute a per-currency total in the browser, or sum across currencies.

---

## 9. Out of scope for M4

Cross-currency transfers · FX and exchange rates · BCV or any rate source ·
manual rates · a base or reporting currency · consolidated net worth · transfer
fees · split transactions · scheduled or recurring transfers · **any delete** ·
reversals · audit history of edits.

---

## Changing this contract

Frozen. If either side needs something that is not here: state what and why,
change **this document** first, and call the change out explicitly in the
milestone report.
