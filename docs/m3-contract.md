# M3 contract — backend ↔ presentation

**Status:** frozen · **Milestone:** M3 — Data Safety, Editing & Payment Methods

This document extends [m2-contract.md](m2-contract.md), which **remains in force
except where this document contradicts it**. Everything M2 froze — the money
rules, the date rules, the optional-field convention, `errors.Is` for error
handling, balances derived from movements — still holds.

Read the [breaking changes](#1-breaking-changes-from-m2) first. M3 changes the
shape of payment methods, and that touches the transaction views and forms the
presentation layer already renders.

## The one rule, unchanged

**The presentation layer never writes SQL, never opens a transaction, never
imports a repository, and never performs financial arithmetic.**

---

## 1. Breaking changes from M2

### Payment method stops being an enum

In M2 a payment method was one of four fixed strings. In M3 it is a **row in a
table the user administers**, so it is referenced by id and displayed by name.

| M2 | M3 |
| --- | --- |
| `ledger.PaymentMethod` (string type) | **deleted** |
| `ledger.PaymentMethods() []PaymentMethod` | **deleted** |
| `RecordIncomeInput.PaymentMethod PaymentMethod` | `PaymentMethodID int64` |
| `RecordExpenseInput.PaymentMethod PaymentMethod` | `PaymentMethodID int64` |
| `TransactionView.PaymentMethod *PaymentMethod` | `PaymentMethodID *int64` **and** `PaymentMethodName *string` |

On the presentation side this retires `web.PaymentMethod`, its four constants,
`web.PaymentMethods()` and `web.PaymentMethodLabel()`. The label now comes from
the database, because the user chose it.

**The four M2 methods survive the migration** with the names the UI already
displayed, so nothing visible changes on upgrade:

```
BANK_TRANSFER → "Bank transfer"      CASH  → "Cash"
POS           → "Point of sale"      OTHER → "Other"
```

A form that used to render a fixed `<select>` now renders one built from
`paymentmethod.Service.List`.

### The rest of `TransactionView` is unchanged

Same fields, same order, same semantics. `AmountFormatted`, `MinorUnit`,
`EffectiveDate`, the category triple and the account block are exactly as M2
froze them.

---

## 2. `internal/paymentmethod`

A payment method is the **channel** an operation with the outside world used. It
is not a transfer between the user's own accounts — internal transfers remain out
of scope.

### Model

```go
type PaymentMethod struct {
    ID        int64
    Name      string
    IsActive  bool
    CreatedAt time.Time
    UpdatedAt time.Time
}

type CreateInput struct {
    Name string
}

type UpdateMetadataInput struct {
    Name string
}

type ListInput struct {
    IncludeInactive bool
}
```

### Service

```go
func NewService(db *sql.DB) *Service

func (s *Service) List(ctx context.Context, in ListInput) ([]PaymentMethod, error)
func (s *Service) Get(ctx context.Context, id int64) (PaymentMethod, error)
func (s *Service) Create(ctx context.Context, in CreateInput) (PaymentMethod, error)
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (PaymentMethod, error)
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (PaymentMethod, error)
```

`List` orders by name, then id. Use it without `IncludeInactive` to populate a
transaction form; with it for a management screen.

### Rules

- Name required, trimmed, 1–60 characters.
- **Names are unique exactly**, on the trimmed value. Unlike `category`, this is
  *not* case-insensitive: SQLite's `NOCASE` folds ASCII only, and pretending it
  implements Unicode case folding is worse than not claiming it. "Zelle" and
  "zelle" are therefore two different methods. The divergence from `category` is
  deliberate and recorded in [data-model.md](data-model.md).
- There is no delete. `SetActive(ctx, id, false)` archives; it is reversible.
- An archived method **cannot be used for new or edited transactions**, but
  transactions already filed under it keep it and keep displaying it.

### Renaming changes history — by design

Renaming a payment method changes how **every** past transaction that used it is
displayed. That is intended: a payment method is a label for a channel, not a
fact captured at the moment of the transaction, so correcting "Point of sale" to
"Punto de venta" should follow the history. It is the same choice `category.Name`
already makes.

**What must not be done** is reusing a row for a different meaning — renaming
"Zelle" to "PayPal" instead of creating a second method — because that silently
rewrites what past transactions claim. The schema cannot prevent it; this
document forbids it.

M3 keeps no version history of edited records. See [section 8](#8-editing-is-destructive).

---

## 3. Editing

### Accounts — unchanged since M2

`account.Service.UpdateMetadata` already exists and already enforces everything
below. No signature changes.

| Mutable | Immutable |
| --- | --- |
| `Name`, `Type`, `Subtype`, `Institution` | `ID`, `CurrencyCode` |

`CurrencyCode` is absent from `UpdateMetadataInput`, so there is no signature
through which to attempt it. A USD account never becomes a EUR account; that
would reinterpret every movement ever recorded against it.

The subtype rule applies to edits exactly as it does to creation:
`BANK` requires `CHECKING`/`SAVINGS`, everything else requires `nil`.
Reclassifying `BANK → CASH` means sending the new type **and** a nil subtype in
the same call; sending one without the other is `account.ErrInvalidInput`.

**Editing metadata never changes a balance.**

### Categories — unchanged since M2

`category.Service.UpdateMetadata` already exists.

| Mutable | Immutable |
| --- | --- |
| `Name`, and `IsActive` via `SetActive` | `Type` |

A category created with the wrong type is archived and replaced, never flipped:
flipping it would reinterpret every transaction already filed under it.

### Transactions — new in M3

```go
func (s *Service) UpdateTransaction(ctx context.Context, id int64, in UpdateTransactionInput) (TransactionView, error)
func (s *Service) UpdateOpeningBalance(ctx context.Context, id int64, in UpdateOpeningBalanceInput) (TransactionView, error)
```

Two use cases, not one generic update — the same reasoning that gave M2 three
separate `Record*` methods.

```go
type UpdateTransactionInput struct {
    AccountID       int64
    Type            TransactionType // Income or Expense only
    CategoryID      int64
    PaymentMethodID int64
    Amount          string          // decimal, POSITIVE — the sign is applied by the backend
    Description     *string
    EffectiveDate   string          // "YYYY-MM-DD"

    // AllowCurrencyChange must be true to move a transaction to an account
    // denominated in a different currency. See below.
    AllowCurrencyChange bool
}

type UpdateOpeningBalanceInput struct {
    Amount        string // may be negative; may NOT be zero
    EffectiveDate string
}
```

**This is a replacement, not a patch.** Every editable field is sent on every
call, and the service revalidates **exactly the same invariants as a creation**:
account exists and is active, category exists, is active and matches the type,
payment method exists and is active, amount is positive and within the currency's
precision, date is a real calendar date.

`UpdateTransaction` accepts only `Income` and `Expense`. Converting to or from
`OpeningBalance` is `ErrTypeChangeNotAllowed` — an opening balance is a statement
about where an account started, not an operation, and turning one into income
would put it in every "what did I earn" figure.

**`INCOME ↔ EXPENSE` is supported** because it is a plausible data-entry mistake.
The movement's sign is recomputed from the new type, never carried over: an
expense corrected to income becomes positive, and the balance follows
immediately.

### Opening balances — separate flow

`UpdateOpeningBalanceInput` carries only the amount and the date. Category and
payment method are not fields of it, so they cannot be given. The account cannot
change and the type cannot change.

**An amount of zero is rejected** with `ErrInvalidAmount`. In M2 a zero opening
balance means "record nothing"; M3 has no delete, so setting an existing one to
zero would leave a transaction with no financial effect.

### Moving a transaction to another currency

The amount is a decimal string, parsed at the **destination** account's
`minor_unit`. Moving a transaction from a USD account to a VES account therefore
reinterprets what the number means — "100.00" is a different amount of money in
each — and M3 has no exchange rates and performs no conversion.

**The backend requires explicit confirmation.** If the destination account's
currency differs from the current one and `AllowCurrencyChange` is false, the
service returns `ErrCurrencyChangeNotConfirmed` and **writes nothing**.

The UI must show a confirmation step naming the currency the amount will be read
in, then retry with the flag set. Do not set it unconditionally: it exists so the
reinterpretation is never silent.

The currency is still derived from the account. There is no `currency_code` on
`transactions` or `account_movements` and there will not be one.

---

## 4. Money — the canonical wire format

M2's rules stand: exact `int64` minor units, no floats anywhere, amounts in and
out as decimal strings.

M3 makes the input format explicit, because it was ambiguous:

**The canonical format the presentation layer sends is `1234.56`** — digits, an
optional single `.`, at most `MinorUnit` decimals, an optional leading `-` only
where the use case allows it. **No thousands separators. No currency symbol.**

The UI may format values however it likes for *display*. What it must not do is
**guess financial meaning by stripping separators**: `strings.ReplaceAll(amount, ",", "")`
turns `1,5` into `15`, silently multiplying by ten a user who typed a decimal
comma. Normalisation must be a deliberate conversion from a known input
convention to the canonical format, not a blind deletion.

Amounts come back pre-formatted (`AmountFormatted`, `BalanceFormatted`) and must
be rendered as given.

---

## 5. Backups

```go
type Reason string

const (
    Manual       Reason = "manual"
    Shutdown     Reason = "shutdown"
    PreMigration Reason = "premigration"
    PreRestore   Reason = "prerestore"
)

type Backup struct {
    Name      string    // finance-20260905T183045Z-manual.db
    Reason    Reason
    CreatedAt time.Time
    SizeBytes int64
}

func (s *Service) Create(ctx context.Context, reason Reason) (Backup, error)
func (s *Service) List(ctx context.Context) ([]Backup, error)
```

`List` returns newest first. It is what a *Settings → Data* screen renders:
filename, timestamp, size.

The UI calls `Create(ctx, backup.Manual)` for a **Create backup now** button. The
snapshot is taken with `VACUUM INTO` against the live database — no downtime, and
the result is validated before it is reported as a success.

The other three reasons are produced by the backend on its own (before applying
migrations, on graceful shutdown, and before a restore) and are never triggered
from the web.

### Restore is not a web operation

Restore replaces the database file. Doing that while the server is serving
requests would pull the file out from under open connections, so it is an
**offline CLI operation** on the same binary:

```
docker compose stop
docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
docker compose up -d
```

Do not build a restore button. Do not expose the backup directory path in a form.

---

## 6. Errors

Distinguish with `errors.Is`. Never compare or parse error strings.

| Sentinel | Meaning | Suggested status |
| --- | --- | --- |
| `paymentmethod.ErrNotFound` | no such payment method | 404 |
| `paymentmethod.ErrInvalidInput` | blank or too-long name | 422 |
| `paymentmethod.ErrConflict` | that name already exists | 409 |
| `ledger.ErrPaymentMethodNotFound` | the method does not exist | 422 |
| `ledger.ErrPaymentMethodInactive` | the method is archived | 422 |
| `ledger.ErrTypeChangeNotAllowed` | tried to convert to/from `OPENING_BALANCE` | 422 |
| `ledger.ErrCurrencyChangeNotConfirmed` | destination account has another currency and the flag was not set | 409 |
| `backup.ErrNotFound` | no such backup file | 404 |
| `backup.ErrInvalidName` | the name is not a plain backup filename | 422 |
| `backup.ErrInvalidBackup` | the file is not a usable SQLite backup | 422 |

Everything M2 declared still applies: `account.*`, `category.*`, `currency.*`,
and the rest of `ledger.*`. `ledger.ErrAccountInactive`,
`ErrCategoryInactive`, `ErrCategoryTypeMismatch`, `ErrInvalidAmount` and
`ErrInvalidInput` are returned by the edit methods too, for the same reasons they
are returned by the record methods.

`ErrCurrencyChangeNotConfirmed` is the one error that means "ask the user and try
again", not "the input was wrong".

---

## 7. Presentation requirements

These are requirements of M3, not suggestions, and each has an acceptance
criterion.

1. **Send the canonical money format.** Remove the blind
   `strings.ReplaceAll(amount, ",", "")` in `internal/web/transactions.go` and
   `internal/web/accounts.go`. If the UI accepts grouped input, convert it
   deliberately; do not delete characters and hope.
2. **Derive decimal precision from `MinorUnit`.** `web/static/js/app.js`
   hard-codes two decimals (`slice(0, 2)`). Correct for USD, EUR and VES; wrong
   for any currency with a different scale. Every view model already carries
   `MinorUnit`.
3. **Handle every parse error.** No `value, _ := strconv.ParseInt(...)`. A
   malformed id is a validation failure with a status code, not a silent zero.

---

## 8. Editing is destructive

**M3 supports editing current records. M3 does not preserve previous versions of
edited records.**

There is no audit log, no history table and no event sourcing. Editing a
transaction overwrites it and its movement. This is a deliberate scope decision
for a single-user personal application, revisitable in a later milestone.

Do not build UI that implies an edit can be undone. What *can* be undone is the
whole database, from a backup.

---

## 9. What the presentation layer must not do

Everything in M2's list, plus:

- Build a restore button, or accept a backup path from a form.
- Set `AllowCurrencyChange` without asking the user.
- Offer conversion to or from `OPENING_BALANCE`.
- Offer a delete for transactions, accounts, categories or payment methods —
  none exists.
- Assume a payment method's name is stable, or cache it as an identifier. The id
  is the identifier.
- Strip characters from a monetary input to make it parse.

---

## 10. Out of scope for M3

Internal transfers · FX and exchange rates · a base or reporting currency ·
consolidated net worth · credit · loans · instalments · recurring transactions ·
budgets · tags · hierarchical categories · authentication · a public REST API ·
**any delete** · audit history of edits.

---

## Changing this contract

Frozen. If either side needs something that is not here: state what and why,
change **this document** first, and call the change out explicitly in the
milestone report.
