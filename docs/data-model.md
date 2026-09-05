# Data model

Current schema version: **3** (M1). Migrations live in
[`internal/migrations/`](../internal/migrations) and are applied automatically at startup.

```
currencies
  code (PK)
     │ 1
     │
     │ N
  currency_code (FK, NOT NULL, ON DELETE/UPDATE RESTRICT)
accounts
```

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

Create → update metadata (`name`, `account_type`, `institution`) → archive /
restore.

There is no delete, and no operation that changes `currency_code`.

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
- **The index on `accounts.currency_code` is not optional.** SQLite does not
  index the child columns of a foreign key automatically, so without it every
  `RESTRICT` check scans the whole table, as would any future join.

A legitimate and reachable state: **an active account whose currency is
inactive.** Archiving a currency stops it being used for *new* accounts; it does
not invalidate accounts that already use it. Queries must not assume otherwise.

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

Amounts will be stored as **integer minor units** (`INTEGER` / `int64`) alongside
the currency they are denominated in; an amount without its currency is
meaningless and the two are never separated. Exchange rates will use an exact
representation — integer numerator and denominator, or scaled integers with a
recorded scale — never a float.

**No monetary column exists yet.** `accounts` has no balance of any kind, and
that is deliberate rather than pending: balances will be derived from the movement
ledger. A stored balance would be a denormalised value that can silently disagree
with its own history. A test asserts the exact column set of both tables, so a
monetary column cannot appear by accident.

There is no `Money` type in Go yet either. The invariant is recorded so that the
first code to handle an amount is written correctly rather than retrofitted.

---

## Invariants not enforced by the database

Three fields are immutable by design but **not** by constraint:
`currencies.code`, `currencies.minor_unit` and `accounts.currency_code`.

They are protected by the absence of any code path that changes them: no use case
accepts them, no input struct carries them, and no `UPDATE` statement names them.

They are *not* protected against someone writing raw SQL. The only mechanism
SQLite offers for that is a `BEFORE UPDATE` trigger — `CHECK` cannot compare
`OLD` with `NEW` — and triggers were rejected as hidden control flow that only
defends against the same person who could drop the trigger.

**This means a manual `UPDATE currencies SET minor_unit = ...` would silently
reinterpret every amount stored in that currency.** Treat these three columns as
read-only in any hand-written SQL.

Likewise, "an account's currency must be active at creation" is a business rule
rather than a referential one — a currency may legitimately be archived after
accounts already use it — so it is enforced in the service, inside the same
transaction as the insert.

---

## What this document gains next

Movements and categories (M2) will add the ledger, and with it the first monetary
columns. That is when the money conventions above stop being forward-looking
rules and start being enforced schema.
