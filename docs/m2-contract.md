# M2 contract — backend ↔ presentation

**Status:** frozen · **Milestone:** M2 — Core Ledger MVP

This document is the agreement between the M2 **backend** (domain, persistence,
migrations, services) and the M2 **presentation layer** (HTTP handlers,
templates, htmx). It is written before the backend is finished so that both can
proceed in parallel: **the signatures here are a commitment, not a sketch.**

It describes *what the presentation layer may call and what it gets back*. It
deliberately contains no SQL and no schema. The schema is documented in
[data-model.md](data-model.md); the reasoning behind each choice is in
[technical-decisions.md](technical-decisions.md).

## The one rule

**The presentation layer never writes SQL, never opens a transaction, never
imports a repository, and never performs financial arithmetic.** Everything it
needs is a method on a service in this document. If something it needs is
missing, that is a gap in this contract — see
[Changing this contract](#changing-this-contract).

## Ownership

| Owner | Paths |
| --- | --- |
| Backend | `internal/account`, `internal/currency`, `internal/category`, `internal/ledger`, `internal/money`, `internal/application`, `internal/database`, `internal/migrations`, `internal/dbtest`, `docs/` |
| Presentation | `internal/web`, `web/`, `cmd/web/main.go` (wiring only) |

`internal/money` and `internal/application` are new packages introduced by M2;
their justification is in their sections below.

---

## 1. Wiring

Every service is constructed from the `*sql.DB` that `cmd/web` already opens.
Construction is cheap and stateless; build them once at startup and share them.

```go
currencies := currency.NewService(db)
accounts   := account.NewService(db)
categories := category.NewService(db)
ledgerSvc  := ledger.NewService(db)
app        := application.NewService(db) // cross-domain workflows

// hand these to the handler layer
```

Migrations already run at startup, before anything listens. Nothing else is
required to make the M2 schema available.

Every method takes a `context.Context` as its first argument. Pass the request
context, so that a cancelled request cancels its queries.

---

## 2. Enumerations

All of these are Go `string` types. Their values are exactly the strings below —
they are what is stored, and they are safe to use as HTML form values.

Each type has a `Valid() bool` method, and each package exposes a slice
constructor listing every value **in the order a UI should present them**.

### Account type — `account.Type`

| Value | Constant | Meaning |
| --- | --- | --- |
| `BANK` | `account.Bank` | Held at an institution |
| `CASH` | `account.Cash` | Physical money |
| `WALLET` | `account.Wallet` | Digital balance that is not a bank account |
| `OTHER` | `account.Other` | Anything else |

`account.Types() []Type`

### Account subtype — `account.Subtype` *(new in M2)*

| Value | Constant |
| --- | --- |
| `CHECKING` | `account.Checking` |
| `SAVINGS` | `account.Savings` |

`account.Subtypes() []Subtype` — every value.

`account.SubtypesFor(t Type) []Subtype` — the values valid for `t`. Returns the
two above for `BANK` and an **empty slice** for every other type. Use this to
drive the form: if it returns nothing, the subtype control must not be rendered
and must not be submitted.

**The rule, enforced in Go and in SQLite:**

```text
account_type == BANK   ->  subtype is REQUIRED and must be CHECKING or SAVINGS
account_type != BANK   ->  subtype MUST be absent (nil)
```

Sending a subtype with a `CASH` account, or omitting it for a `BANK` account, is
`account.ErrInvalidInput`. The subtype is editable metadata: it can be changed
later through `UpdateMetadata`, subject to the same rule.

### Category type — `category.Type`

| Value | Constant |
| --- | --- |
| `INCOME` | `category.Income` |
| `EXPENSE` | `category.Expense` |

`category.Types() []Type`

### Transaction type — `ledger.TransactionType`

| Value | Constant | Category | Payment method | Sign of the amount |
| --- | --- | --- | --- | --- |
| `INCOME` | `ledger.Income` | required, must be an `INCOME` category | required | positive |
| `EXPENSE` | `ledger.Expense` | required, must be an `EXPENSE` category | required | negative |
| `OPENING_BALANCE` | `ledger.OpeningBalance` | must be absent | must be absent | signed (may be negative) |

`ledger.TransactionTypes() []TransactionType`

There is no `TRANSFER` in M2. A transfer between two of the user's own accounts
is a later milestone.

### Payment method — `ledger.PaymentMethod`

| Value | Constant |
| --- | --- |
| `BANK_TRANSFER` | `ledger.BankTransfer` |
| `POS` | `ledger.POS` |
| `CASH` | `ledger.CashPayment` |
| `OTHER` | `ledger.OtherMethod` |

`ledger.PaymentMethods() []PaymentMethod`

A payment method is the **channel** an operation with the outside world used. It
applies to income and to expenses alike — money can arrive by transfer or in
cash just as it can leave that way — so the same four values are offered for
both, and no combination of transaction type and payment method is rejected.

`BANK_TRANSFER` is not a transfer between the user's own accounts. It means "this
external operation moved through a bank transfer".

An opening balance has no channel: it is a statement about where the account
started, not an operation. Its payment method is always absent.

### Display labels

These constants are storage values, not user-facing text. Human labels,
capitalisation, translation and iconography are the presentation layer's
business; the backend provides no `Label()` method and never will.

---

## 3. Money

### The rule

`float32`, `float64` and SQLite `REAL` are forbidden anywhere near a monetary
amount. This is not a style preference — see TD-013 in
[technical-decisions.md](technical-decisions.md).

**The presentation layer must never do arithmetic on an amount.** It must not
add balances, must not negate an expense, and must not convert between a decimal
string and a number. Everything it needs is already computed.

### The two representations

| Representation | Type | Where it appears |
| --- | --- | --- |
| Decimal string | `string`, e.g. `"1000.50"` | **Input** — what a form posts |
| Minor units | `int64`, e.g. `100050` | **Output** — exact, for sorting and comparison |
| Formatted string | `string`, e.g. `"1000.50"` | **Output** — exact, for display |

Every read model carries **both** `…Minor int64` and `…Formatted string`. Render
the formatted one; the minor one is there for comparisons — `< 0` to colour a
negative balance red, for example.

### Input format — what the backend accepts

`internal/money` parses a decimal string into minor units, exactly, using the
account's currency `minor_unit` as the scale.

Accepted:

```text
"1000"        minor_unit 2  ->  100000
"1000.50"     minor_unit 2  ->  100050
"25.99"       minor_unit 2  ->    2599
"0.05"        minor_unit 2  ->       5
"-40"         minor_unit 2  ->   -4000   (opening balance only)
"  12.34  "   minor_unit 2  ->    1234   (surrounding whitespace is trimmed)
"007.50"      minor_unit 2  ->     750
"1234"        minor_unit 0  ->    1234
```

Rejected, each as an invalid amount:

```text
""  "   "     empty or blank
"12.345"      more decimals than the currency has — never rounded silently
"1,000.50"    thousands separators
"$100"        currency symbols
"1e3"         exponent notation
"12."  ".5"   a decimal point must have digits on both sides
"abc"  "1-2"  anything else
"92233720368547758080"   beyond int64 — reported, never wrapped
```

The rule to give the user: **digits, an optional single decimal point, and at
most as many decimals as the currency has.** For USD, EUR and VES that is two.

Sign is **not** part of the input for income or expenses. `RecordExpense`
receives `"25.99"`, not `"-25.99"`; a leading `-` there is rejected. The sign is
determined by the operation, which is what makes an expense that increases a
balance unrepresentable. Only an opening balance accepts a negative amount.

### Output format

`money.Format` renders minor units back to a plain decimal string with exactly
`minor_unit` decimals, a leading `-` when negative, no thousands separators and
no currency symbol:

```text
minor_unit 2:   100050 -> "1000.50"     5 -> "0.05"     -5 -> "-0.05"     0 -> "0.00"
minor_unit 0:     1234 -> "1234"
```

`money.Parse(money.Format(v, u), u) == v` for every representable `v`. That
round-trip is a tested property.

Grouping separators and symbol placement are **presentation decisions** and are
deliberately not provided. The currency's symbol travels next to every amount as
`CurrencySymbol`, which may be `nil` — fall back to the code.

### Where this lives

```go
package money // internal/money

var ErrInvalidAmount = errors.New("invalid amount")

// Parse converts a decimal string to minor units at the given scale.
func Parse(s string, minorUnit int) (int64, error)

// Format renders minor units back to a decimal string at the given scale.
func Format(minor int64, minorUnit int) string
```

It is its own package rather than part of `currency` because it is numeric
representation, not reference data: `currency` is a catalogue of rows, while this
is the arithmetic that exchange rates will later join. It is two functions and a
sentinel — there is no `Money` struct, no constructor and no method set, because
nothing yet needs one.

The presentation layer should not normally call it: amounts arrive pre-formatted.

---

## 4. Dates

A transaction has two different times and they must not be confused.

| Field | Type | Meaning |
| --- | --- | --- |
| `EffectiveDate` | `time.Time` (UTC midnight) | **When it happened financially.** A calendar date. |
| `CreatedAt` | `time.Time` | When the row was written. A timestamp. |

A purchase made on 2026-09-03 and entered on 2026-09-05 has an effective date of
2026-09-03 and a `created_at` of 2026-09-05. Listings order by the effective
date; `created_at` is for provenance.

**Input:** the effective date is submitted as a `string` in `YYYY-MM-DD` —
exactly what `<input type="date">` posts. It is required; there is no implicit
"today", so default the form control to today rather than sending an empty
value. Invalid shapes and impossible dates such as `2026-02-31` are rejected in
Go and by the database. Future dates are allowed.

**Output:** a `time.Time` at UTC midnight. To render it:

```gotemplate
{{ .EffectiveDate.Format "2006-01-02" }}
```

or `database.FormatDate(t)`, which is the same layout as one function. Do not
apply a timezone to it — it is a calendar date, not an instant.

**Defaulting a form to today:** use `database.Today()`, which returns the current
date in UTC as a `time.Time` at midnight. It exists so that "today" has one
definition in the codebase rather than one per handler.

---

## 5. Optional fields

Optional strings are `*string`. **`nil` means absent; an empty string is a
caller mistake and is rejected**, so absence has exactly one representation all
the way down to SQL `NULL`. This applies to `Institution`, `Symbol` and
`Description`.

An HTML form always posts a string, so convert once at the edge:

```go
// optional returns nil for a field the user left empty.
func optional(s string) *string {
    s = strings.TrimSpace(s)
    if s == "" {
        return nil
    }
    return &s
}
```

Values are trimmed by the domain as well, so leading and trailing whitespace
never reaches the database.

Length limits, enforced in Go and by the database:

| Field | Max |
| --- | --- |
| `account.Name` | 120 |
| `account.Institution` | 120 |
| `category.Name` | 80 |
| `ledger` description | 255 |
| `currency.Name` | 60 |
| `currency.Symbol` | 8 |

---

## 6. `internal/currency` — unchanged in M2

M1's API stands as it is. The presentation layer needs it to populate the
currency selector on the account form.

```go
func NewService(db *sql.DB) *Service

func (s *Service) List(ctx context.Context, includeInactive bool) ([]Currency, error)
func (s *Service) Get(ctx context.Context, code string) (Currency, error)
func (s *Service) Create(ctx context.Context, in CreateInput) (Currency, error)
func (s *Service) UpdateMetadata(ctx context.Context, code string, in UpdateMetadataInput) (Currency, error)
func (s *Service) SetActive(ctx context.Context, code string, isActive bool) (Currency, error)
```

```go
type Currency struct {
    Code      string  // ISO 4217, the primary key, immutable
    Name      string
    Symbol    *string // may be absent — fall back to Code
    MinorUnit int     // decimals: 2 for USD, EUR and VES
    IsActive  bool
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

Use `List(ctx, false)` for a "new account" form: an archived currency must not be
offered for new accounts.

Seeded: `USD`, `EUR`, `VES`.

---

## 7. `internal/account` — subtype added

### Model

```go
type Account struct {
    ID           int64
    Name         string
    Type         Type
    Subtype      *Subtype // NEW in M2: set if and only if Type == Bank
    CurrencyCode string   // immutable
    Institution  *string  // may be absent
    IsActive     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

### Inputs

```go
type CreateInput struct {
    Name         string
    Type         Type
    Subtype      *Subtype // required if Type == Bank, otherwise must be nil
    CurrencyCode string
    Institution  *string
}

type UpdateMetadataInput struct {
    Name        string
    Type        Type
    Subtype     *Subtype
    Institution *string
}
```

`CurrencyCode` is absent from `UpdateMetadataInput` on purpose: **an account's
currency can never change.** There is no signature through which to attempt it.
To "change" a currency, archive the account and open a new one.

### Service

```go
func NewService(db *sql.DB) *Service

func (s *Service) List(ctx context.Context, includeInactive bool) ([]Account, error)
func (s *Service) Get(ctx context.Context, id int64) (Account, error)
func (s *Service) Create(ctx context.Context, in CreateInput) (Account, error)
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (Account, error)
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (Account, error)
```

`List` orders by name, then id. There is no delete: accounts are archived with
`SetActive(ctx, id, false)` and can be brought back.

**To create an account together with its opening balance, do not call `Create` —
use `application.OpenAccount` (section 10).**

### Institution

`institution` is the free-text name of the bank or provider — "Mercantil",
"Banesco", "Bank of America". There is no `banks` table and there will not be one
in M2.

---

## 8. `internal/category`

Categories group income and expenses. They are created by the user; **nothing is
seeded**, so the first screen a new user needs is "create a category".

An opening balance has no category, so no special category exists for it.

### Model

```go
type Category struct {
    ID        int64
    Name      string
    Type      Type // INCOME or EXPENSE — immutable
    IsActive  bool
    CreatedAt time.Time
    UpdatedAt time.Time
}

type CreateInput struct {
    Name string
    Type Type
}

type UpdateMetadataInput struct {
    Name string // only the name is editable
}
```

**`Type` is immutable.** Changing a category from `INCOME` to `EXPENSE` would
reinterpret every transaction already filed under it, so `UpdateMetadataInput`
does not carry it and no update statement names it. This mirrors how
`currency.MinorUnit` and `account.CurrencyCode` are handled.

### Service

```go
func NewService(db *sql.DB) *Service

func (s *Service) List(ctx context.Context, in ListInput) ([]Category, error)
func (s *Service) Get(ctx context.Context, id int64) (Category, error)
func (s *Service) Create(ctx context.Context, in CreateInput) (Category, error)
func (s *Service) UpdateMetadata(ctx context.Context, id int64, in UpdateMetadataInput) (Category, error)
func (s *Service) SetActive(ctx context.Context, id int64, isActive bool) (Category, error)

type ListInput struct {
    Type            *Type // nil = both types
    IncludeInactive bool
}
```

`List` orders by type, then name, then id — so a grouped list needs one pass and
no sorting in the template.

To fill the category selector on an expense form:

```go
expenseType := category.Expense
categories.List(ctx, category.ListInput{Type: &expenseType})
```

### Rules

- The name must not be blank, and is trimmed. Maximum 80 characters.
- **Names are unique per type, case-insensitively.** Creating a second `EXPENSE`
  category called "Comida" — or "comida" — is `category.ErrConflict`. The same
  name may exist once as `INCOME` and once as `EXPENSE`, which is legitimate:
  "Intereses" can be both. Case folding is ASCII-only (SQLite's `NOCASE`), so
  "Café" and "CAFÉ" are treated as different. This is a known limitation, not an
  oversight.
- There is no delete. `SetActive(ctx, id, false)` archives; it is reversible.
- An archived category **cannot be used for new transactions**, but transactions
  already filed under it keep it and keep displaying it. Use `List` without
  `IncludeInactive` to populate a form, and with `IncludeInactive: true` for a
  management screen.
- Hierarchy is out of scope: there is no `parent_id` in M2.

---

## 9. `internal/ledger`

The ledger is the only place amounts exist. It owns two concepts:

- a **transaction** — the financial event: its type, category, channel,
  description and effective date;
- an **account movement** — the impact of that event on one account, a signed
  `amount_minor`.

They are separate because a transfer between the user's own accounts is one
event with two movements. That is a later milestone, but the shape is built for
it now. **In M2 every transaction has exactly one movement**, so the read model
below is flat.

The presentation layer does not create movements. It records income, an expense
or an opening balance, and the movement follows.

### Read model

One row per movement, joined to its transaction, its account and its currency —
everything a table row needs, with no second query and no N+1.

```go
type TransactionView struct {
    // The event
    ID            int64
    Type          TransactionType
    EffectiveDate time.Time      // calendar date, UTC midnight
    Description   *string        // may be absent
    PaymentMethod *PaymentMethod // absent for OPENING_BALANCE

    // The category, absent for OPENING_BALANCE
    CategoryID   *int64
    CategoryName *string
    CategoryType *category.Type

    // The account the money moved in
    MovementID     int64
    AccountID      int64
    AccountName    string
    AccountType    account.Type
    AccountSubtype *account.Subtype
    Institution    *string

    // The currency, which is the account's currency, always
    CurrencyCode   string
    CurrencySymbol *string // may be absent — fall back to CurrencyCode
    MinorUnit      int

    // The amount, signed: income positive, expenses negative
    AmountMinor     int64
    AmountFormatted string // exact, e.g. "-25.99"

    CreatedAt time.Time // when it was recorded, not when it happened
}
```

`AmountFormatted` carries the sign. To display an expense as `25.99` in a "debit"
column, branch on `AmountMinor < 0` and render the sign yourself from the
formatted string — do not compute the absolute value numerically.

### Balances

```go
type AccountBalance struct {
    AccountID      int64
    AccountName    string
    AccountType    account.Type
    AccountSubtype *account.Subtype
    Institution    *string
    IsActive       bool

    CurrencyCode   string
    CurrencySymbol *string
    MinorUnit      int

    BalanceMinor     int64
    BalanceFormatted string
}

type CurrencyTotal struct {
    CurrencyCode   string
    CurrencyName   string
    CurrencySymbol *string
    MinorUnit      int

    AccountCount   int
    TotalMinor     int64
    TotalFormatted string
}
```

### Service

```go
func NewService(db *sql.DB) *Service

func (s *Service) RecordIncome(ctx context.Context, in RecordIncomeInput) (TransactionView, error)
func (s *Service) RecordExpense(ctx context.Context, in RecordExpenseInput) (TransactionView, error)
func (s *Service) RecordOpeningBalance(ctx context.Context, in RecordOpeningBalanceInput) (*TransactionView, error)

func (s *Service) ListTransactions(ctx context.Context, in ListTransactionsInput) ([]TransactionView, error)
func (s *Service) GetTransaction(ctx context.Context, id int64) (TransactionView, error)

func (s *Service) GetAccountBalance(ctx context.Context, accountID int64) (AccountBalance, error)
func (s *Service) ListAccountBalances(ctx context.Context, includeInactive bool) ([]AccountBalance, error)
func (s *Service) ListCurrencyTotals(ctx context.Context, includeInactive bool) ([]CurrencyTotal, error)
```

There is deliberately **no** `CreateTransaction(type, …)`. One method per use
case is what makes an expense with an income category, or an opening balance with
a payment method, impossible to express rather than merely invalid.

M2 has no operation that edits or deletes a transaction.

### Inputs

```go
type RecordIncomeInput struct {
    AccountID     int64
    CategoryID    int64         // must be an INCOME category, and active
    Amount        string        // decimal, positive, e.g. "1500.00"
    PaymentMethod PaymentMethod // required
    Description   *string       // optional
    EffectiveDate string        // "YYYY-MM-DD", required
}

type RecordExpenseInput struct {
    AccountID     int64
    CategoryID    int64         // must be an EXPENSE category, and active
    Amount        string        // decimal, POSITIVE — the sign is applied by the backend
    PaymentMethod PaymentMethod // required
    Description   *string
    EffectiveDate string
}

type RecordOpeningBalanceInput struct {
    AccountID     int64
    Amount        string // decimal, may be negative, may be zero
    EffectiveDate string
}

type ListTransactionsInput struct {
    Limit int // <= 0 uses the default of 50; capped at 500
}
```

`RecordOpeningBalanceInput` has no category and no payment method — not as
optional fields left empty, but absent from the struct, so an opening balance
cannot be given either.

The **currency is never an input**. It is the account's currency, derived from
`AccountID`. A request cannot claim that an operation on a USD account is in VES,
because there is nowhere to say so. Cross-currency belongs to the FX milestone.

### An opening balance of zero records nothing

`RecordOpeningBalance` with an amount that parses to zero — `"0"`, `"0.00"` — is
**accepted and creates nothing**: no transaction, no movement, and it returns
`(nil, nil)`. The balance of an account with no movements is already zero, so a
zero-amount row would assert nothing while adding a row to every listing.

**Check the pointer before using it.** This is the only method in the contract
that can return a `nil` value with a `nil` error.

Because nothing is recorded, **a zero amount does not require an effective
date** either: there is no transaction for the date to belong to, so
`EffectiveDate` may be empty when the amount is zero. The account is still
checked — a zero amount is not a way to skip validation of everything else.

Every other amount, including a negative one, records a transaction and a
movement. A negative opening balance is meaningful — an account that starts
overdrawn — and is not rejected.

### Ordering and limits

`ListTransactions` returns **`effective_date DESC, id DESC`**. The second key is
what makes the order total and stable: several transactions commonly share one
date, and without it their relative order would be undefined between calls.

The default limit is 50 and the maximum is 500. This is a "recent activity"
listing, not a paginated archive; real pagination arrives when there is a screen
that needs it.

`ListAccountBalances` returns **currency code, then account name, then id**, so
the dashboard's per-currency sections are one pass over the slice with a break
when `CurrencyCode` changes — no sorting and no grouping logic in the template.

`ListCurrencyTotals` returns one row per currency **that has at least one
account**, ordered by currency code, with `TotalMinor` being the exact sum of
that currency's account balances.

An account with no movements has a balance of `0`, formatted at its currency's
scale — `"0.00"` for USD. It is included in both listings.

**No currency is ever converted into another.** The dashboard shows USD, VES and
EUR side by side; a single consolidated net worth requires exchange rates and is
the FX milestone.

### What the backend guarantees

The presentation layer may rely on all of this and must not re-check it:

1. A transaction and its movement are written in **one** `sql.Tx`. A financial
   event with no impact on an account cannot exist, and there is no compensating
   delete anywhere in the code.
2. The movement's currency is the account's currency, by construction — no column
   can disagree with it, because none exists.
3. Income is stored positive, an expense negative. The sign is derived from the
   use case, never from input.
4. An `INCOME` transaction has an `INCOME` category; an `EXPENSE` transaction has
   an `EXPENSE` category. Verified against the category row inside the same
   transaction as the insert.
5. An `OPENING_BALANCE` transaction has no category and no payment method.
6. A balance is always `SUM(movements)` computed at read time. **No balance is
   stored anywhere.** There is no `accounts.balance`, no `current_balance` and no
   cached total; a test asserts the exact column set so one cannot appear by
   accident.
7. Amounts are exact `int64` minor units end to end. No float is involved in
   parsing, storage, summation or formatting.

---

## 10. `internal/application` — opening an account with a balance

Creating an account and recording its opening balance is one user action that
touches two domains. It cannot live in `account` — which would have to import
`ledger` — or in `ledger`, which already imports `account`, without a cycle, so
it lives in a third package.

**This package exists for exactly one reason: this workflow is real today and it
must be atomic.** It is not a general application layer, and it holds nothing
else. If a second cross-domain workflow appears, it joins it; if not, it stays
one function.

```go
func NewService(db *sql.DB) *Service

func (s *Service) OpenAccount(ctx context.Context, in OpenAccountInput) (OpenAccountResult, error)

type OpenAccountInput struct {
    Account account.CreateInput

    // OpeningBalance is the account's starting amount as a decimal string.
    // nil, blank, or an amount of zero means no opening balance is recorded.
    OpeningBalance *string

    // OpeningBalanceDate is "YYYY-MM-DD". Required when OpeningBalance names a
    // non-zero amount, ignored otherwise.
    OpeningBalanceDate string
}

type OpenAccountResult struct {
    Account account.Account
    // OpeningBalance is nil when none was recorded.
    OpeningBalance *ledger.TransactionView
}
```

**Use this for the "new account" form, always** — even when the balance field is
left empty. It behaves identically to `account.Service.Create` in that case, and
using one path means the form has one code path rather than two.

The account insert and the opening-balance transaction share **one** `sql.Tx`.
Either both exist or neither does. The handler must never do:

```go
// WRONG — not atomic. A failure here leaves an account that the user
// believes has a balance.
acct, err := accounts.Create(ctx, in)
if err == nil {
    ledgerSvc.RecordOpeningBalance(ctx, …)
}
```

### Errors from `OpenAccount`

This is the one place the contract lets errors from two packages through:
`OpenAccount` returns errors that satisfy `errors.Is` against `account.*` **and**
`ledger.*` sentinels, and declares none of its own. A caller of this method has
already named both domains in its input and receives values from both in its
result, so this is the composition it asked for rather than a leaked internal.

---

## 11. Errors

Distinguish errors with `errors.Is`. **Never compare or parse error strings**,
and never inspect a SQLite error code — see TD-023 in
[technical-decisions.md](technical-decisions.md).

Every error carries a message that names what went wrong and is safe to show a
user once the sentinel has been matched.

| Sentinel | Meaning | Suggested status |
| --- | --- | --- |
| `account.ErrNotFound` | no such account | 404 |
| `account.ErrInvalidInput` | validation failed, including the subtype rule | 422 |
| `account.ErrCurrencyNotFound` | the chosen currency does not exist | 422 |
| `account.ErrCurrencyInactive` | the chosen currency is archived | 422 |
| `account.ErrConflict` | reserved; not returned in M2 | 409 |
| `category.ErrNotFound` | no such category | 404 |
| `category.ErrInvalidInput` | blank or too-long name, unknown type | 422 |
| `category.ErrConflict` | that name already exists for that type | 409 |
| `ledger.ErrNotFound` | no such transaction | 404 |
| `ledger.ErrInvalidInput` | bad date, unknown payment method, bad description | 422 |
| `ledger.ErrInvalidAmount` | unparseable, over-precise, negative where forbidden, overflowing | 422 |
| `ledger.ErrAccountNotFound` | the account does not exist | 422 |
| `ledger.ErrAccountInactive` | the account is archived; no new movements | 422 |
| `ledger.ErrCategoryNotFound` | the category does not exist | 422 |
| `ledger.ErrCategoryInactive` | the category is archived | 422 |
| `ledger.ErrCategoryTypeMismatch` | an `EXPENSE` category on income, or the reverse | 422 |
| `currency.ErrNotFound` / `ErrInvalidInput` / `ErrConflict` | as M1 | 404 / 422 / 409 |

Anything that matches none of these is an internal failure: log it and return
500. Do not show its text to the user.

`ledger` declares its own account and category errors rather than passing
`account.ErrNotFound` or `category.ErrNotFound` through, so a transaction handler
never has to import a neighbouring domain to interpret what the ledger returned.
`ErrInvalidAmount` is separate from `ErrInvalidInput` so that the amount field can
be highlighted specifically; a generic validation handler should check both.

---

## 12. What the presentation layer must not do

- Write SQL, open a transaction, or import any `Repository` type.
- Parse or format a monetary amount, or add balances together.
- Represent an amount as `float64` — including in an intermediate variable, a
  JSON payload or a template computation.
- Negate an expense before sending it, or send a signed amount for income or
  expenses at all.
- Send a currency for a transaction. It comes from the account.
- Assume a transaction can be edited or deleted. Neither exists in M2.
- Assume an account's currency can change. It cannot.
- Offer a subtype control for a non-`BANK` account, or omit it for a `BANK` one.
- Create an account and its opening balance in two calls.
- Convert between currencies, or display a single consolidated net worth.

---

## 13. Out of scope for M2

Transfers between the user's own accounts · FX and exchange rates · BCV · a base
or reporting currency · consolidated net worth · historical net worth · credit
cards · loans · instalments · recurring transactions · budgets · tags ·
hierarchical categories · authentication · multiple users · a public REST API ·
transaction editing and deletion · pagination beyond a limit.

Do not build a screen that implies any of these exists.

---

## Changing this contract

Once frozen, this document does not change silently.

If the backend needs to change a signature, or the presentation layer needs
something that is not here:

1. state what is needed and why;
2. change **this document** first;
3. call the change out explicitly in the milestone report, so the other side sees
   it rather than discovering it at merge time.

The most likely gaps, listed here so they are cheap to ask for rather than
invented locally: a filter on `ListTransactions` — by account, by date range, by
category — and per-account transaction listings. Both are small additions to
`ListTransactionsInput`. Ask; do not query the database from a handler.
