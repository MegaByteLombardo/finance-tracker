# Data model

Current schema version: **13** (M5.5). Migrations live in
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

currencies                     currencies
  code (PK)                      code (PK)
     │ 1                            │ 1
     │ N (base, quote)              │ 1
exchange_rates                 app_settings          (exactly one row)
  effective_date · rate · source   base_currency_code
```

`exchange_rates` and `app_settings` sit **beside** the ledger, not inside it.
Nothing in the two tables above them changed when they arrived: no column was
added anywhere, and `transactions` in particular did **not** gain an
`exchange_rate_id`. They are read by a valuation, never by a write.

**M5.5 did not change that either.** It added one transaction type,
`FX_EXCHANGE`, which records money the user actually exchanged between two of
their own accounts in two different currencies. It is one transaction with two
movements, exactly like a `TRANSFER`, so no table and no column was added; only
a `CHECK` constraint changed. It reads no exchange rate and writes none — the
rate it happened at is derived from its two amounts. See
[m5.5-contract.md](m5.5-contract.md).

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

## `transactions` *(M2, extended in M4 and M5.5)*

The financial **event**. It holds no amount and names no account — that is
`account_movements`.

`CREATE TABLE transactions (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `transaction_type` | `TEXT` | NOT NULL | — | `CHECK (… IN ('INCOME','EXPENSE','OPENING_BALANCE','TRANSFER','FX_EXCHANGE'))` |
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
    OR
    (transaction_type = 'FX_EXCHANGE'
        AND category_id IS NULL AND payment_method_id IS NULL)
)
```

Income and expenses are categorised and have a channel. An opening balance has
neither, because it is a statement about where the account started rather than an
operation that took place. A transfer has neither either, and its clause is
written separately rather than folded into the opening balance's because it says
something different: moving money between two of the user's own accounts is not
income and not an expense, so a category would contaminate every later question
about them, and no channel with the outside world was used at all. An exchange
*(M5.5)* has neither for a third reason, which is why it gets a fourth clause:
money **was** exchanged with the outside world, but nothing was earned and
nothing was consumed — the same wealth is denominated differently afterwards. A
commission charged on top is a separate `EXPENSE` under a category such as "Bank
fees"; it is not a field of this row.

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
its account's; a column here could only ever disagree with it. As of M5.5 a
single transaction can span **two** currencies — an `FX_EXCHANGE` — and it still
needs no column here: its two accounts already state them.

**There is no `exchange_rate_id` and no rate column of any kind** *(M5.5)*, and
there must never be one. The rates in `exchange_rates` are REFERENCE rates: what
a source said a currency was worth on a day, used to value balances. The rate an
`FX_EXCHANGE` actually happened at is stated exactly by its two movements —
source ÷ destination — and is derived on every read. A stored third copy could
only ever disagree with the two amounts that caused it. See
[m5.5-contract.md](m5.5-contract.md) and [TD-055](technical-decisions.md).

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
| `FX_EXCHANGE` *(M5.5)* | **two rows**: negative in the source account, positive in the destination, in **two different currencies** and of **unrelated magnitude** |

The sign is derived from the use case in the service, never taken from input,
which is what makes "an expense that increases a balance" unrepresentable rather
than merely invalid.

**An `FX_EXCHANGE`'s two magnitudes are never compared with each other.** A
transfer's sum to zero, and that invariant is the point of the type; an exchange's
are quantities of two different currencies, so `abs(source) == destination` would
be comparing céntimos with cents. 8,500,000 and 10,000 are the same exchange.

**There is still no `currency_code` column here**, and an exchange did not need
one. A movement is denominated in its account's currency by construction, and an
exchange's two accounts already state that they differ.

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

**Every balance is "as of" a date** (TD-063, M6). The current balance is the one
as of today on the Venezuelan calendar:

```sql
SELECT a.id, COALESCE(b.balance_minor, 0), COALESCE(b.future_count, 0)
  FROM accounts a
  LEFT JOIN (
        SELECT m.account_id,
               SUM(CASE WHEN t.effective_date <= :today THEN m.amount_minor ELSE 0 END) AS balance_minor,
               SUM(CASE WHEN t.effective_date >  :today THEN 1 ELSE 0 END)              AS future_count
          FROM account_movements m
          JOIN transactions t ON t.id = m.transaction_id
         GROUP BY m.account_id
       ) b ON b.account_id = a.id;
```

A movement dated after today exists and is listed, but is not part of the
balance; it is counted instead. Until M6 the balance summed every movement, which
quietly included future-dated ones (allowed since TD-028).

The `LEFT JOIN` back to `accounts` with `COALESCE` is what gives an account with
no movements -- or with only future ones -- a balance of zero rather than
dropping it from the result. A newly opened account must appear on the dashboard.

### How history is derived *(M6)*

A historical net worth reads the ledger once, as daily nets:

```sql
SELECT m.account_id, t.effective_date, SUM(m.amount_minor), count(*)
  FROM account_movements m
  JOIN transactions t ON t.id = m.transaction_id
 GROUP BY t.effective_date, m.account_id
 ORDER BY t.effective_date, m.account_id;
```

and walks the calendar in memory, adding each day's deltas to running balances.
A balance as of D is the sum of the deltas dated on or before D -- the same rule as
above. Nothing is snapshotted (TD-065). The cash flow reads
`transaction_type IN ('INCOME', 'EXPENSE')` per day, category and currency
between two dates, ranging on `idx_transactions_effective_date` (TD-068).

**No index was added for either.** The deltas read covers the whole ledger --
future-dated rows included, because they are the "movimientos futuros" count --
and for a whole-table read a scan is the optimal plan; an index would only add a
lookup per row. The flows read is a range on `effective_date`, which the existing
`(effective_date, id)` index serves; adding `transaction_type` to a key would pay
off only if transfers and exchanges were most of the rows. The rates read
(`is_active = 1 AND effective_date <= ?`) covers a table of a few thousand rows.

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
and the two are never separated. **As of M5 exchange rates are exact too**: a
canonical decimal string, with every operation performed on `math/big` rationals
so that inversion and cross rates never round. See `exchange_rates` below and
[TD-046](technical-decisions.md).

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
- **The shape of a `TRANSFER` and of an `FX_EXCHANGE`** *(M4, M5.5)* — two
  movements, opposite in sign, in two different accounts; the same currency for a
  transfer and **different** currencies for an exchange. Every one of those rules
  spans two rows, and a `CHECK` sees one. They are enforced in Go inside the
  writing transaction and **re-checked on every read**, so a row that was
  modified by something else is reported rather than rendered half-way. See
  [TD-044](technical-decisions.md).

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

---

## `exchange_rates` *(M5)*

What a source said one currency was worth in another, on a given day.

`CREATE TABLE exchange_rates (...) STRICT;`

**This table is REFERENCE / VALUATION FX and nothing else.** It records rates used
to express balances the user already holds in a single currency, for presentation.
It is never the rate a particular operation happened at — a card's rate on a
purchase, a merchant's rate, the rate a cross-currency transfer settled at. None
of that exists in this application, `transactions` gained no `exchange_rate_id`,
and nothing here implies one. Mixing the two would make it impossible to say later
which one a stored number meant.

### Orientation, fixed for the whole project

```
1 base_currency_code  =  rate  quote_currency_code

base = USD, quote = VES, rate = '842.20670000'   →   1 USD = 842.2067 VES
```

The columns are `base` / `quote` rather than `from` / `to` because from/to invites
a reader to assume a direction of conversion that the row does not state.
Direction is the caller's question, answered by `internal/fx`, which inverts
exactly rather than reading a second stored row that could disagree.

Careful: the **base of a pair** is not the application's **base currency of
valuation**. That one lives in `app_settings` and is called the valuation base
everywhere in the Go code.

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | rowid | **PRIMARY KEY** (rowid alias) |
| `base_currency_code` | `TEXT` | NOT NULL | — | **FK** → `currencies(code)`, `RESTRICT` both ways |
| `quote_currency_code` | `TEXT` | NOT NULL | — | **FK** → `currencies(code)`, `RESTRICT` both ways |
| `effective_date` | `TEXT` | NOT NULL | — | date `GLOB` **and** `date(x) IS x` |
| `rate` | `TEXT` | NOT NULL | — | shape and positivity `CHECK` (below) |
| `source` | `TEXT` | NOT NULL | — | `CHECK (source IN ('MANUAL','BCV_OFFICIAL'))` |
| `is_active` | `INTEGER` | NOT NULL | `1` | `CHECK (is_active IN (0,1))` |
| `fetched_at` | `TEXT` | **NULL** | — | timestamp `GLOB`, plus the table `CHECK` below |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

```sql
CREATE UNIQUE INDEX idx_exchange_rates_observation
    ON exchange_rates(base_currency_code, quote_currency_code, effective_date, source);

CREATE INDEX idx_exchange_rates_lookup
    ON exchange_rates(base_currency_code, quote_currency_code, effective_date DESC);

CREATE INDEX idx_exchange_rates_quote_currency_code
    ON exchange_rates(quote_currency_code);
```

**`rate` is `TEXT`, and that is a decision, not a shortcut.** `REAL` is binary
floating point and cannot hold `842.20670000` exactly. TEXT rather than an integer
mantissa plus a scale column because those two would have to be kept consistent by
hand, because the same value would then have several spellings, and because the
owner of this database opens it with the sqlite3 CLI — the same argument that made
timestamps TEXT. **The scale is significant and preserved:** the trailing zeros of
`'842.20670000'` record the precision the source published at. Full reasoning and
the rejected alternatives: [TD-046](technical-decisions.md).

What the `CHECK` honestly guarantees, and no more: the value is made only of digits
and at most one decimal point, starts with a digit — so it carries no sign and no
leading point — and is not zero.

```sql
CHECK (length(rate) BETWEEN 1 AND 48
       AND rate GLOB '[0-9]*'
       AND NOT rate GLOB '*[^0-9.]*'
       AND length(rate) - length(replace(rate, '.', '')) <= 1
       AND ltrim(replace(rate, '.', ''), '0') <> '')
```

The last clause is how **strict positivity is expressed without a cast**: stripping
the point and then the leading zeros leaves an empty string exactly when the value
is zero. `CAST(rate AS REAL) > 0` would have been shorter and would have put a
float in the one place this project refuses to have one. Canonical form and scale
bounds are validated in Go, in `internal/money`.

Nothing in this application **orders or range-filters by rate**, so TEXT not
sorting numerically costs nothing: the lookup is by pair and date, both exact.

**`effective_date` is the ECONOMIC date** — the BCV's "Fecha Valor", or the day a
manually entered rate applies to. It is emphatically not `fetched_at`, and the two
disagree **in both directions**:

- the BCV holds one Fecha Valor across a weekend, so Friday's rate is still the
  rate in force on Sunday;
- the BCV publishes **forward** — its page on Friday 2026-09-11 carried a Fecha
  Valor of Tuesday 2026-09-15.

**`fetched_at` belongs to a fetch.** It is present for an automatic source and
absent for a manual one, enforced by a table `CHECK` written with `IS NULL` /
`IS NOT NULL` on both sides so it can only be `TRUE` or `FALSE` (TD-025):

```sql
CHECK ((source =  'MANUAL' AND fetched_at IS NULL)
       OR
       (source <> 'MANUAL' AND fetched_at IS NOT NULL))
```

**`source` is an enumeration, not an `is_manual` boolean**, because provenance is
the point: two sources can state different values for the same day, and the
application has to know which to prefer and be able to tell the user which it
used. There is no `BCV_MIRROR`; see [TD-054](technical-decisions.md).

**`is_active`** retires an observation without destroying it. Archiving a manual
rate is how an override is withdrawn: valuation ignores archived rows, so the
official rate underneath applies again. There is no delete, here or anywhere.

### Uniqueness, and why the provider upserts but a person does not

The natural identity of an observation is **one source, one pair, one economic
day**, which is what the unique index says.

A second run of the scraper on the same day is the same publication being seen
again, and a corrected publication for the same Fecha Valor is a correction of the
same fact — neither is a second observation, so the provider **upserts**. A person
typing a value that already exists has not been told it is there, so a manual
creation **conflicts** and offers the edit instead. The difference is deliberate.

A manual rate and an official rate for the same day are two different observations
and **both survive**, which is what makes precedence a decision rather than a
destructive write.

### Indexes

- **`(base, quote, effective_date, source)`**, unique — the identity above.
- **`(base, quote, effective_date DESC)`** — the critical path of every valuation:
  `WHERE base = ? AND quote = ? AND effective_date <= ? ORDER BY effective_date
  DESC LIMIT 1`. SQLite walks it backwards with no sort. Source precedence is
  resolved among the rows of the single winning date, a handful at most, so it is
  not part of the key.
- **`(quote_currency_code)`** — SQLite does not index the child columns of a
  foreign key. `base_currency_code` leads both indexes above and is covered;
  `quote_currency_code` is not, and without this every `RESTRICT` check against
  `currencies` would scan this table in full.

### Lifecycle

Create → (manual only) correct the value → archive / restore. **There is no
delete**, and a `BCV_OFFICIAL` row is not editable at all: it records what was
published, and the way to disagree with a publication is a manual observation for
the same day.

The pair and the effective date are the **identity** of an observation and never
change — changing them would name a different observation rather than correct this
one. They are protected the way `currencies.code` is: by the absence of any code
path that names them (TD-022).

---

## `app_settings` *(M5)*

The application's own configuration. **Exactly one row, forever.**

`CREATE TABLE app_settings (...) STRICT;`

| Column | Type | Null | Default | Constraints |
| --- | --- | --- | --- | --- |
| `id` | `INTEGER` | NOT NULL | — | **PRIMARY KEY**, `CHECK (id = 1)` |
| `base_currency_code` | `TEXT` | NOT NULL | — | **FK** → `currencies(code)`, `RESTRICT` both ways |
| `created_at` | `TEXT` | NOT NULL | `strftime(…)` | timestamp `GLOB` |
| `updated_at` | `TEXT` | NOT NULL | same | timestamp `GLOB` |

Seeded by the migration with `USD`.

**`base_currency_code` is the valuation base:** the currency the consolidated net
worth is expressed in. **Changing it rewrites nothing.** No transaction, no
movement and no balance changes; each account keeps its own currency and its own
amounts, and only the presentation of the consolidated figure changes. A test
compares every ledger row before and after.

**Why a typed column rather than `settings(key, value)`:** only a typed column can
carry a real foreign key. A `value TEXT` holding `'USD'` references nothing, so
archiving or renaming a currency could leave the setting pointing at something
that no longer exists and nothing in the database would object. See
[TD-048](technical-decisions.md).

**Why `CHECK (id = 1)`:** with two rows, every reader would have to pick one and
"the settings" would stop being a meaningful phrase. The row is created by the
migration rather than lazily, so every read is a plain `SELECT` and its absence is
an integrity failure rather than a state something has to handle.

**No index on `base_currency_code`**, which is the one exception to the rule that
every foreign key child column is indexed. That rule exists so a `RESTRICT` check
does not scan the child table; this child table has exactly one row, so the scan
*is* the row.

This is **not** the beginning of a preferences system. One user, one process, one
database file. A second setting arrives as a second column when a second setting
exists.

---

## The net worth is not in this document

There is no `current_net_worth`, no `converted_balance` and no `cached_net_worth`
column or table, and there will not be one. The consolidated net worth is a
**function** of three things this schema already holds — the movements, the rates
and the valuation base — recomputed on every read by `internal/valuation`.

A stored copy would be a fourth thing that could disagree with the three that
produce it, and the first time it disagreed nobody would know which was right.
This is the same reasoning that has kept every balance derived since M2, applied
one level up. A test looks for tables whose names suggest a cached valuation and
fails if it finds one.

---

## What this document gains next

FX arrived in M5, and the prediction this section used to make held: exchange
rates needed an exact non-integer representation, and they got one —
[TD-046](technical-decisions.md) chose a canonical decimal string with `math/big`
arithmetic over a mantissa/scale pair and over a third-party decimal package. The
ledger was not touched: no table gained a column, `transactions` has no
`exchange_rate_id`, and the balance derivation is unchanged.

What M5 deliberately did **not** add is the thing the next milestone will need: a
way to ask what the net worth was on a past date and get an answer cheaply.
`valuation.GetNetWorthAt` already takes a date and already refuses to use an
observation effective after it ([TD-053](technical-decisions.md)), so the
arithmetic is ready; what is missing is history of the *balances*, which today
means summing every movement from the beginning for every point on a chart. That
is the next real decision this document will carry, and it is the reason
`account_movements` has an index on `account_id` and not on `(account_id,
created_at)`.

M5.5 then confirmed the shape of the ledger a second time. Recording a real
currency exchange — the operation this application exists to track, in a country
where it happens weekly — needed **no new table, no new column and no new
index**: one `CHECK` constraint, and the two-table split from M2 did the rest.
It also drew the line the next milestone must not cross: the rate an operation
happened at is derived from what moved, and reference rates are a separate
statement by a separate source. Anything that proposes to store the first, or to
reconcile it against the second, is proposing to add a fourth thing that can
disagree with three that already exist.
