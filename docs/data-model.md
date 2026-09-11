# Data model

Current schema version: **10** (M4). Migrations live in
[`internal/migrations/`](../internal/migrations) and are applied automatically at startup.

```
currencies                     categories        payment_methods
  code (PK)                      id (PK)           id (PK)
     │ 1                            │ 1               │ 1
     │                              │                 │
     │ N                            │ 0..N            │ 0..N
  currency_code (FK, NOT NULL)   category_id       payment_method_id
accounts                       └──── transactions ────┘
     │ 1                            │ 1
     │                              │
     │ 0..N                         │ 1..N
  account_id (FK, NOT NULL)      transaction_id (FK, NOT NULL)
        └──────── account_movements ────────┘
                    amount_minor
```

Every foreign key is `ON DELETE RESTRICT ON UPDATE RESTRICT`. Nothing in this
schema is ever deleted in normal operation.

**`account_movements` is the only place an amount exists.** Every balance in the
application is `SUM(amount_minor)` over it, computed at read time. There is no
balance column anywhere, and a test asserts the exact column set of every table
so one cannot appear by accident.

---

## `currencies`

Reference data. A currency is created once and then referred to; it is never
deleted, because accounts and, later, movements depend on it permanently.

`CREATE TABLE currencies (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `code` | `TEXT` | NOT NULL | — | **PRIMARY KEY**. `CHECK (code GLOB '[A-Z][A-Z][A-Z]')` |
| `name` | `TEXT` | NOT NULL | — | `CHECK (length(trim(name)) BETWEEN 1 AND 60)` |
| `symbol` | `TEXT` | **NULL** | — | `CHECK (symbol IS NULL OR length(trim(symbol)) BETWEEN 1 AND 8)` |
| `minor_unit` | `INTEGER` | NOT NULL | — | `CHECK (minor_unit BETWEEN 0 AND 4)` |
| `is_active` | `INTEGER` | NOT NULL | `1` | `CHECK (is_active IN (0,1))` |
| `created_at` | `TEXT` | NOT NULL | `strftime('%Y-%m-%dT%H:%M:%fZ','now')` | timestamp `GLOB` (below) |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` (below) |

**`code`** — The ISO 4217 code, used directly as the primary key. It is stable,
globally unique, and already the value every other table references; a surrogate
integer would add a join to read something the foreign key column already holds.
The `GLOB` pattern has no wildcards, so it requires exactly three characters, and
`GLOB` being case-sensitive means it also enforces canonical uppercase.
**Immutable.**

**`symbol`** — Presentation metadata only, never an identifier: `$` is shared by
USD, CAD, AUD and others. Nullable, because not every currency has a distinctive
symbol in common use; the UI falls back to the code. The `CHECK` keeps `''` out,
so "no symbol" has exactly one representation. Modelled in Go as `*string`.

**`minor_unit`** — Decimal places in the currency's minor unit. The range is the
real span of ISO 4217 exponents: 0 (JPY, CLP), 2 (most), 3 (KWD, BHD), 4 (CLF,
UYW). Nothing in the standard exceeds 4. **Immutable** — changing it would
silently reinterpret every amount ever stored in that currency, turning 1.00 into
0.100.

Crypto-style precision is excluded deliberately. Eighteen decimals would overflow
`int64` for trivial amounts, so supporting it would be a numeric representation
decision, not a wider `CHECK`.

**`is_active`** — SQLite has no boolean type; `INTEGER` constrained to 0/1 is the
correct spelling under STRICT. Retires a currency from new use without destroying
history.

### Seed

Inserted by migration `00002_seed_currencies.sql`. Only the currencies actually
in use — a full ISO 4217 catalogue would be rows nobody reads.

| code | name | symbol | minor_unit |
| --- | --- | --- | --- |
| `USD` | US Dollar | `$` | 2 |
| `EUR` | Euro | `€` | 2 |
| `VES` | Bolívar Soberano | `Bs.` | 2 |

### Lifecycle

Create → update metadata (`name`, `symbol`) → archive / restore.

There is no delete. `code` and `minor_unit` never change: no use case accepts
them and no `UPDATE` statement names them.

---

## `accounts`

`CREATE TABLE accounts (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `name` | `TEXT` | NOT NULL | — | `CHECK (length(trim(name)) BETWEEN 1 AND 120)` |
| `account_type` | `TEXT` | NOT NULL | — | `CHECK (account_type IN ('BANK','CASH','WALLET','OTHER'))` |
| `account_subtype` | `TEXT` | **NULL** | — | `CHECK (… IN ('CHECKING','SAVINGS'))`, plus the table `CHECK` below |
| `currency_code` | `TEXT` | **NOT NULL** | — | **FK** → `currencies(code)`, `ON DELETE RESTRICT ON UPDATE RESTRICT` |
| `institution` | `TEXT` | **NULL** | — | `CHECK (institution IS NULL OR length(trim(institution)) BETWEEN 1 AND 120)` |
| `is_active` | `INTEGER` | NOT NULL | `1` | `CHECK (is_active IN (0,1))` |
| `created_at` | `TEXT` | NOT NULL | `strftime('%Y-%m-%dT%H:%M:%fZ','now')` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

```sql
CREATE INDEX idx_accounts_currency_code ON accounts(currency_code);
```

**`id`** — `INTEGER PRIMARY KEY`, a rowid alias. No UUID: this is one local
SQLite file, so there is no distributed generation and no database merging to
accommodate. No `AUTOINCREMENT` either — it only prevents reuse of rowids from
deleted rows, and rows here are never deleted.

**`name`** — Not `UNIQUE` on purpose. Two accounts legitimately share a name
across different institutions, and forcing uniqueness would force artificial
names.

**`account_type`** — Classifies **the logical container the money sits in**, and
nothing else. Purpose — "savings", "emergency fund", "day-to-day" — is a separate
axis: one can save in a bank account, in cash or in a wallet, so folding it in
here would force a false choice between two simultaneously true facts. `BANK`
covers what elsewhere is split into current and savings accounts; the institution
is recorded separately. If a purpose dimension is ever needed it arrives as its
own decision, not as more values here.

**`account_subtype`** *(M2)* — Refines `BANK`, and only `BANK`. The dependency is
a table constraint:

```sql
CHECK (
    (account_type =  'BANK' AND account_subtype IS NOT NULL
                            AND account_subtype IN ('CHECKING', 'SAVINGS'))
    OR
    (account_type <> 'BANK' AND account_subtype IS NULL)
)
```

A bank account must have one; nothing else may. **The `IS NOT NULL` clause is
load-bearing** — a `CHECK` fails only when it is *false*, and `NULL IN (…)` is
`NULL`, which passes. Without it, a `BANK` row with no subtype is accepted by a
constraint that reads as though it forbids exactly that. See
[TD-025](technical-decisions.md).

This is editable metadata: an account reclassified from current to savings is
corrected, not re-created. Reclassifying a bank account as cash requires changing
both columns in one statement, which is what the constraint demands.

Why not two more `account_type` values: [TD-024](technical-decisions.md).

**`currency_code`** — `NOT NULL` matters independently of the foreign key: a NULL
child column always satisfies a foreign key, so without it an account could exist
with no currency at all. **Immutable** — see the invariant below.

**`institution`** — Nullable for real: cash held outside any institution has
none, which differs from an empty string. The `CHECK` keeps `''` out. Not
normalised into a separate table; there is no query that would benefit yet.

**`is_active`** — Accounts are archived, not deleted. Once movements exist,
deleting an account would orphan history or cascade destructively over it. There
is no `deleted_at` or `archived_at`: nothing needs to know *when* an account was
archived, only *whether* it is, and `updated_at` already records the last change.

### Lifecycle

Create → update metadata (`name`, `account_type`, `account_subtype`,
`institution`) → archive / restore.

There is no delete, and no operation that changes `currency_code`.

---

## `categories` *(M2)*

What income and expenses are grouped under. Created by the user; **nothing is
seeded**.

`CREATE TABLE categories (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `name` | `TEXT` | NOT NULL | — | `CHECK (length(trim(name)) BETWEEN 1 AND 80)` |
| `category_type` | `TEXT` | NOT NULL | — | `CHECK (category_type IN ('INCOME','EXPENSE'))` |
| `is_active` | `INTEGER` | NOT NULL | `1` | `CHECK (is_active IN (0,1))` |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

```sql
CREATE UNIQUE INDEX idx_categories_type_name
    ON categories(category_type, name COLLATE NOCASE);
```

**`category_type`** — **Immutable.** Flipping a category from `INCOME` to
`EXPENSE` would reinterpret every transaction already filed under it, so no use
case accepts it and no `UPDATE` names it. The same treatment as
`currencies.minor_unit` and `accounts.currency_code`, and protected the same way:
by the absence of a code path, not by a trigger.

**Uniqueness** — Per type, case-insensitively. Two `EXPENSE` categories called
"Comida" are a data-entry mistake; the same name under both types is not, because
"Intereses" is both earned and paid. `NOCASE` folds ASCII only, so "Café" and
"CAFÉ" are treated as different — a known limitation of the collation, recorded
in [TD-031](technical-decisions.md) rather than papered over.

The index exists for **correctness, not speed**, and it is the only one on this
table. An index on `(category_type, is_active)` was considered for the form
dropdowns and rejected: a personal finance database holds tens of categories,
where a scan is cheaper than an index lookup.

**No seed, and no category for opening balances.** An opening balance is
uncategorised by construction — `transactions` refuses a category on one.

**No hierarchy.** There is no `parent_id` in M2.

### Lifecycle

Create → rename → archive / restore. There is no delete: `transactions`
references categories with `ON DELETE RESTRICT`.

An archived category **still holds its name**, so it cannot be taken by a new
one while it exists. Transactions already filed under it keep displaying it; it
simply cannot classify anything new.

---

## `payment_methods` *(M3)*

The channels money moves through: a bank transfer, a card at a point of sale,
cash, "Pago móvil", "Zelle". Created by the user; **nothing beyond the four
migrated from M2 is seeded**.

`CREATE TABLE payment_methods (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `name` | `TEXT` | NOT NULL | — | `CHECK (length(trim(name)) BETWEEN 1 AND 60)` |
| `is_active` | `INTEGER` | NOT NULL | `1` | `CHECK (is_active IN (0,1))` |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

```sql
CREATE UNIQUE INDEX idx_payment_methods_name ON payment_methods(name);
```

### Migrated from M2

Migration `00008` seeds exactly the four values M2 held in a `CHECK` constraint,
under the names the interface already displayed for them, so upgrading changes
nothing anyone can see. `00009` then rewrites every existing transaction to point
at the matching row.

| M2 value | M3 row |
| --- | --- |
| `BANK_TRANSFER` | Bank transfer |
| `POS` | Point of sale |
| `CASH` | Cash |
| `OTHER` | Other |

### Uniqueness is exact — and diverges from `categories`

`payment_methods` uses a plain `UNIQUE(name)`. `categories` uses
`UNIQUE(category_type, name COLLATE NOCASE)`. **This inconsistency is deliberate
and worth stating rather than leaving to be discovered.**

`NOCASE` folds ASCII only. It treats "Zelle" and "zelle" as the same name while
treating "Café" and "CAFÉ" as different — case-insensitive uniqueness for half the
alphabet, which is worse than not claiming it at all. Importing ICU to do it
properly is a dependency this application will not carry for one index.

M2 made the other choice for categories, and changing it now would mean a further
destructive migration for no functional gain. So the two differ: categories fold
ASCII case, payment methods do not. Both are documented; neither is an oversight.

The domain trims the name before comparing, so the only remaining difference
between the two tables is letter case.

### Renaming rewrites how history reads

A payment method is a **label for a channel**, not a fact captured at the moment
of the transaction. Renaming "Point of sale" to "Punto de venta" therefore changes
how every past transaction that used it is displayed — and that is intended, the
same choice `categories.name` already makes.

**What must not be done** is reusing a row for a different meaning: renaming
"Zelle" to "PayPal" instead of creating a second method silently rewrites what
past transactions claim about themselves. No constraint can prevent this; it
belongs with the other invariants not enforced by the database, below.

### Lifecycle

Create → rename → archive / restore. There is no delete: `transactions`
references methods with `ON DELETE RESTRICT`.

An archived method **still holds its name**, so un-archiving it cannot collide
with something created in the meantime, and transactions already recorded against
it keep displaying it.

---

## `transactions` *(M2, extended in M4)*

The financial **event**. It holds no amount and names no account — that is
`account_movements`.

`CREATE TABLE transactions (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `transaction_type` | `TEXT` | NOT NULL | — | `CHECK (… IN ('INCOME','EXPENSE','OPENING_BALANCE','TRANSFER'))` |
| `category_id` | `INTEGER` | **NULL** | — | **FK** → `categories(id)`, `RESTRICT` both ways |
| `payment_method_id` | `INTEGER` | **NULL** | — | **FK** → `payment_methods(id)`, `RESTRICT` both ways |
| `description` | `TEXT` | **NULL** | — | `CHECK (… length(trim(description)) BETWEEN 1 AND 255)` |
| `effective_date` | `TEXT` | NOT NULL | — | date `GLOB` **and** `date(x) IS x` (below) |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

```sql
CREATE INDEX idx_transactions_effective_date    ON transactions(effective_date, id);
CREATE INDEX idx_transactions_category_id       ON transactions(category_id);
CREATE INDEX idx_transactions_payment_method_id ON transactions(payment_method_id);
```

**The shape of each type is a table constraint**, not only a service rule:

```sql
CHECK (
    (transaction_type IN ('INCOME', 'EXPENSE')
        AND category_id IS NOT NULL AND payment_method_id IS NOT NULL)
    OR
    (transaction_type = 'OPENING_BALANCE'
        AND category_id IS NULL AND payment_method_id IS NULL)
    OR
    (transaction_type = 'TRANSFER'
        AND category_id IS NULL AND payment_method_id IS NULL)
)
```

Income and expenses are categorised and have a channel. An opening balance has
neither, because it is a statement about where the account started rather than an
operation that took place. A transfer has neither either, and its clause is
written separately rather than folded into the opening balance's because it says
something different: moving money between two of the user's own accounts is not
income and not an expense, so a category would contaminate every later question
about them, and no channel with the outside world was used at all.

Every clause is `IS NULL` / `IS NOT NULL`, so the expression is always `TRUE` or
`FALSE` and cannot silently pass (TD-025). Every combination is covered by tests.

**What the database cannot enforce:** that an `INCOME` transaction uses an
`INCOME` category. That compares a column of `transactions` with a column of
`categories`, and `CHECK` cannot reach across tables. It is enforced in the
service, inside the same transaction as the insert — the same treatment "an
account's currency must be active" gets.

**`payment_method_id`** *(M3)* — The **channel** an operation with the outside
world used, as a reference to a row the user administers. In M2 this was a `TEXT`
column with a four-value `CHECK`; see [TD-036](technical-decisions.md) for why it
changed and `payment_methods` below for the table.

It is not a transfer between the user's own accounts: a method named "Bank
transfer" means "this external operation moved through a bank transfer", whereas
`transaction_type = 'TRANSFER'` means "money moved between two accounts we own".
Both exist as of M4 and they must never be conflated; see
[TD-041](technical-decisions.md).

**`description`** — Optional free text. The `CHECK` keeps `''` out, so "no
description" has exactly one representation, matching `institution` and `symbol`.

**`effective_date`** — When it happened **financially**, which is not when it was
recorded. See [Dates](#dates) below and [TD-028](technical-decisions.md).

**`updated_at`** — Changes when a transaction is corrected. M3 added editing;
`created_at` stays at the moment the row was first written, because when
something was recorded does not change because its contents were fixed.

**There is no `currency_code` column**, deliberately. A transaction's currency is
its account's; a column here could only ever disagree with it.

### Indexes

- **`(effective_date, id)`** — the transaction list screen runs
  `ORDER BY effective_date DESC, id DESC LIMIT 50` on every load. SQLite walks
  this index backwards to satisfy that without a sort. `id` is part of the key
  because transactions routinely share a date, and without it the order within a
  day would not be stable between calls.
- **`(category_id)`** — SQLite does not index the child columns of a foreign key.
  Without it, the `RESTRICT` check when deleting a category scans every
  transaction, as does the join that resolves a category name for the listing.

### Lifecycle

Create → correct → (never delete).

M3 added editing. An edit runs **exactly the same validation as a creation** —
the account exists and is active, the category matches the type, the payment
method is active, the amount is positive and within the currency's precision —
because an edit that could produce a state a creation would refuse would be a
second door into every invariant here. The transaction and its movement are
rewritten in one `sql.Tx`, so a balance is never briefly wrong.

`INCOME` and `EXPENSE` convert freely between each other, and the movement's sign
is recomputed from the new type rather than carried over. **Nothing else
converts.** Converting to or from `OPENING_BALANCE` is refused because it states
where an account started rather than recording an operation; converting to or
from `TRANSFER` is refused because a transfer has two movements and the
income-and-expense editor rewrites one, which would leave a debit with no
matching credit. Each type has its own use case: `UpdateTransaction`,
`UpdateOpeningBalance`, `UpdateTransfer` (TD-045).

**M3 does not preserve previous versions of edited records.** There is no audit
log and no history table; the undo is a backup. See
[TD-039](technical-decisions.md).

---

## `account_movements` *(M2)*

The **effect** of a transaction on one account. This table is the single source
of truth for every balance in the application.

`CREATE TABLE account_movements (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `transaction_id` | `INTEGER` | NOT NULL | — | **FK** → `transactions(id)`, `RESTRICT` both ways. **Not `UNIQUE`** |
| `account_id` | `INTEGER` | NOT NULL | — | **FK** → `accounts(id)`, `RESTRICT` both ways |
| `amount_minor` | `INTEGER` | NOT NULL | — | `CHECK (amount_minor <> 0)` |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |

```sql
CREATE INDEX idx_account_movements_account_id     ON account_movements(account_id);
CREATE INDEX idx_account_movements_transaction_id ON account_movements(transaction_id);
```

**`transaction_id` is deliberately not `UNIQUE`.** Through M3 every transaction
had exactly one movement, so a unique constraint would have held — and been true
for the wrong reason. **As of M4 it would simply be false:** a `TRANSFER` is one
event with two movements, which is why these were two tables from the start. A
test asserts that this table has **no** unique index, so the constraint cannot be
added by someone tidying up. See [TD-026](technical-decisions.md).

No partial unique index was added either — for instance "at most one negative
movement per transaction" — although it would hold for every row M4 can write.
It would freeze "two movements, one per sign" into the schema permanently, and a
later split transaction would have to migrate away from it. Migrating a ledger is
the thing this project avoids.

**`amount_minor`** — Integer minor units, signed:

| Transaction type | Sign |
| --- | --- |
| `INCOME` | positive |
| `EXPENSE` | negative |
| `OPENING_BALANCE` | whichever the account started at |
| `TRANSFER` | **two rows**: negative in the source account, positive in the destination, equal in magnitude |

The sign is derived from the use case in the service, never taken from input,
which is what makes "an expense that increases a balance" unrepresentable rather
than merely invalid.

**Zero is refused.** A zero-amount movement asserts nothing — the sum over no
rows is already zero — while adding a row to every listing. A zero opening
balance therefore records no transaction and no movement at all
([TD-029](technical-decisions.md)).

**There is no `currency_code` column.** A movement is denominated in its
account's currency by construction. A transfer's two accounts are required to
share a currency, so its two movements are in the same one; cross-currency
movement belongs to the FX milestone.

**What a `CHECK` cannot say about a transfer.** That it has exactly two
movements, that they cancel out, that they land in two different accounts, and
that those accounts share a currency are all statements about several rows at
once. A `CHECK` sees one row. They are maintained instead by one function and one
`sql.Tx` in `internal/ledger`, verified by tests, and re-verified by the read path
before a transfer is presented — see [TD-044](technical-decisions.md). What the
schema does enforce from a single row is that a transfer has no category and no
payment method, and that no movement is ever zero.

**No `updated_at`.** A movement is never edited: correcting one means correcting
the transaction that caused it.

### Indexes

- **`(account_id)`** — every balance query filters or groups on it, and it serves
  the `RESTRICT` check that stops an account with history being deleted. A
  covering index on `(account_id, amount_minor)` would answer the sum from the
  index alone; it was left out because at the scale of one person's finances the
  difference is unmeasurable and an index costs on every write forever.
- **`(transaction_id)`** — the join from a transaction to its movements, which is
  how every listing row is built, and the foreign key's `RESTRICT` check.

### How balances are derived

```sql
SELECT a.id, COALESCE(SUM(m.amount_minor), 0)
  FROM accounts a
  LEFT JOIN account_movements m ON m.account_id = a.id
 GROUP BY a.id;
```

`LEFT JOIN` with `COALESCE` is what gives an account with no movements a balance
of zero rather than dropping it from the result — a newly opened account must
appear on the dashboard.

`SUM` over an `INTEGER` column returns an integer, and SQLite raises
`integer overflow` rather than silently switching to floating point if the total
exceeds `int64`. That is the behaviour this application wants: a loud failure
beats a quietly imprecise total.

### Lifecycle

Create. Movements are immutable and are never deleted.

---

## Referential integrity

- `foreign_keys = ON` on every connection, set through the DSN. See
  [architecture.md](architecture.md).
- **`ON DELETE RESTRICT`** — a currency referenced by any account cannot be
  deleted. This is the counterpart to `is_active` being the retirement
  mechanism: currencies are archived, not removed, and the database enforces it
  rather than trusting nobody to write the `DELETE`.
- **`ON UPDATE RESTRICT`** — a currency code in use cannot be renamed. Renaming
  `USD` would change the financial identity of every account denominated in it.
- **Every foreign key's child column is indexed.** SQLite does not do this
  automatically, so without it every `RESTRICT` check scans the whole child
  table, as does any join. That is `accounts.currency_code`,
  `transactions.category_id`, `transactions.payment_method_id`,
  `account_movements.account_id` and `account_movements.transaction_id`.

The same `RESTRICT` rule now covers the ledger: a category with transactions
filed under it cannot be deleted, an account with movements cannot be deleted,
and a transaction with movements cannot be deleted. Archiving is the retirement
mechanism throughout.

Two legitimate and reachable states that queries must not assume away:

- **An active account whose currency is inactive.** Archiving a currency stops it
  being used for *new* accounts; it does not invalidate accounts that already use
  it, and money keeps moving in them.
- **An active transaction whose category is inactive.** Archiving a category
  stops it classifying *new* transactions; the ones already filed under it keep
  it and keep displaying it.
- **An active transaction whose payment method is inactive.** The same reasoning,
  for the same reason.

---

## Conventions

### Timestamps

```
Storage : TEXT, RFC 3339, UTC, exactly three fractional digits
Format  : 2026-08-29T22:37:55.123Z
Go      : "2006-01-02T15:04:05.000Z"  (database.TimeLayout)
SQL     : strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
CHECK   : GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'
```

TEXT rather than Unix integers, because the owner of this database will open it
with the sqlite3 CLI to look at their own finances and `2026-08-29T22:37:55.123Z`
reads where `1787012275` does not — and because business dates arriving later are
calendar dates, so ISO-8601 keeps system timestamps and business dates on one
convention where `date()`, `strftime()` and `BETWEEN` behave identically over
both.

**The fixed width is what makes lexicographic order chronological.** This is why
the layout is `.000` and not `time.RFC3339Nano`: RFC3339Nano strips trailing
zeros, so `…:05.1Z` would sort after `…:05.09Z` as text. The `CHECK` enforces the
shape in the database, so the ordering invariant survives a hand-written INSERT.

Go writes the value explicitly via `database.FormatTime`; the SQL `DEFAULT` is a
safety net. `database.Now()` truncates to milliseconds so a value held in memory
compares equal to the one read back.

### Calendar dates

A **business date** is a different fact from a timestamp and gets a different
convention.

```
Storage : TEXT, ISO 8601, YYYY-MM-DD
Format  : 2026-09-05
Go      : "2006-01-02"  (database.DateLayout), a time.Time at UTC midnight
CHECK   : GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
          AND date(effective_date) IS effective_date
```

"The purchase happened on 2026-09-03" is true in every timezone; an instant is
not, so a business date stored as a timestamp would change day depending on where
it is read. `transactions.effective_date` is the only such column today.

**Both `CHECK` clauses are needed.** The `GLOB` fixes the shape and rejects
`2026-9-5`. `date(x) IS x` rejects dates that are well-formed but do not exist:
`date('2026-02-31')` normalises to `'2026-03-03'`, which is not equal to the
input, and `date('abcd-ef-gh')` is `NULL`. **`IS` rather than `=`** — `=` would
yield `NULL` for the NULL case, and a `CHECK` that evaluates to `NULL` passes
(see below).

`database.ParseDate` enforces the same two rules in Go: the layout demands
zero-padded components and `time.Parse` validates the calendar.
`database.Today()` gives a form its default.

### `CHECK` constraints and NULL

**A `CHECK` fails only when its expression is `FALSE`. `NULL` passes.**

Every multi-column `CHECK` in this schema is therefore written so it can only be
`TRUE` or `FALSE` — nullable columns are tested with `IS NULL` / `IS NOT NULL`,
never left to a comparison that could yield `NULL`. The first version of the
`account_subtype` constraint got this wrong and accepted exactly the row it
appeared to forbid; it was caught by a test, not by review.

This applies to any constraint added from here. See
[TD-025](technical-decisions.md) for the full worked example.

### Strict typing

Every business table is `STRICT`. SQLite's default type affinity would accept a
string where an integer was intended and store it as-is; `STRICT` makes column
types mean something, which matters most precisely where this project is most
sensitive. Any table that is not `STRICT` needs a written justification here.

goose's own `goose_db_version` table is not a business table and its definition
is not ours to control.

### Money — non-negotiable

**Monetary amounts are never stored as `REAL`, and never represented in Go as
`float32` or `float64`.**

Binary floating point cannot represent ordinary decimal amounts exactly. The
error per operation is tiny and compounds silently across summation and
conversion, which is exactly what a finance application does constantly. A
balance off by a cent cannot be reconciled against a statement, and gives no
signal — it just quietly disagrees.

Amounts are stored as **integer minor units** (`INTEGER` / `int64`) alongside the
currency they are denominated in; an amount without its currency is meaningless
and the two are never separated. Exchange rates will use an exact representation
— integer numerator and denominator, or scaled integers with a recorded scale —
never a float.

**As of M2 this is enforced schema, not a forward-looking rule.**

- `account_movements.amount_minor` is the **only** monetary column in the
  database. It is `INTEGER` in a `STRICT` table, so SQLite refuses a `REAL` value
  outright rather than storing it under type affinity — a tested behaviour, not
  an assumption.
- **No balance is stored anywhere.** No `accounts.balance`, no `current_balance`,
  no cached total. Balances are `SUM(amount_minor)` at read time. A test asserts
  the exact column set of every table, and another asserts that no business
  column anywhere has type `REAL`.
- Conversion between decimal strings and minor units lives in
  [`internal/money`](../internal/money) and is exact: the parser walks the digits
  and builds an integer, and the formatter is string surgery on the decimal
  representation. No `float64` appears in parsing, storage, summation or
  formatting. See [TD-027](technical-decisions.md).
- An amount with more decimals than its currency has is **rejected, not
  rounded**. `"12.345"` in USD is an error, because silently keeping `12.34` or
  `12.35` discards money the user typed under a rule they never agreed to.
- Sums are computed by SQLite over `INTEGER`, which raises `integer overflow`
  rather than switching to floating point.

There is still no `Money` struct in Go, and that is deliberate: an amount never
travels without its currency in this codebase, so a wrapper would carry no
invariant the surrounding code does not already hold.

---

## Invariants not enforced by the database

Four fields are immutable by design but **not** by constraint:
`currencies.code`, `currencies.minor_unit`, `accounts.currency_code` and
`categories.category_type`.

They are protected by the absence of any code path that changes them: no use case
accepts them, no input struct carries them, and no `UPDATE` statement names them.

They are *not* protected against someone writing raw SQL. The only mechanism
SQLite offers for that is a `BEFORE UPDATE` trigger — `CHECK` cannot compare
`OLD` with `NEW` — and triggers were rejected as hidden control flow that only
defends against the same person who could drop the trigger.

**This means a manual `UPDATE currencies SET minor_unit = ...` would silently
reinterpret every amount stored in that currency.** Treat these four columns as
read-only in any hand-written SQL.

Three business rules are likewise enforced in the service rather than by a
constraint, each inside the same transaction as the write they guard:

- **An account's currency must be active at creation.** Not referential — a
  currency may legitimately be archived after accounts already use it — so the
  rule applies only at creation time.
- **A transaction's category must match its type.** An `INCOME` transaction needs
  an `INCOME` category. `CHECK` cannot compare a column of `transactions` with a
  column of `categories`.
- **An account and a category must be active to be used for a new transaction.**
  Same reasoning: archived is a fact about the row, not a property that
  invalidates the history pointing at it.

**A payment method row must not be repurposed.** Renaming one to correct its
wording is intended and changes how history reads; renaming it to mean something
else — "Zelle" becoming "PayPal" rather than a second method being created —
silently rewrites what past transactions claim. Nothing in SQLite can tell the
two apart, so this is a rule rather than a constraint.

Everything a `CHECK` *can* express — the subtype dependency, the shape of each
transaction type, non-zero amounts, date validity — is expressed as one, so that
a hand-written `INSERT` cannot get past it either.

---

## Backups

The schema is not the only thing that has to survive. From M3 the database is
snapshotted with `VACUUM INTO` — before migrations, on shutdown, and on demand —
to a directory **outside** the Docker volume, because `docker compose down -v`
destroys volumes and that is exactly what happened to this project once.

Backups are an operational concern rather than a schema one, so the mechanism,
the retention policy and the restore procedure live in
[operations.md](operations.md). What matters here is that a snapshot is a
complete SQLite database at the schema version it was taken at, validated with
`PRAGMA integrity_check`, and that restoring an older one is expected: the next
ordinary start migrates it forward.

---

## What this document gains next

Transfers arrived in M4. They added a fourth `transaction_type` and the first
transaction with **two** movements, and the prediction this section used to make
held: no table gained a column, `account_movements.transaction_id` was already
not unique, and the balance derivation already summed whatever was there. The
only schema change was widening two `CHECK` constraints, which in SQLite means
rebuilding the table — migration `00010`, following the child-first order
[TD-037](technical-decisions.md) established.

FX adds exchange rates, which need an exact non-integer representation — an
integer numerator and denominator, or scaled integers with a recorded scale.
That is the next real decision this document will carry, and it is the reason
`internal/money` is its own package rather than part of `currency`.
