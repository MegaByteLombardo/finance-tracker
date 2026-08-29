# Data model

## Current domain schema: none (M0)

There is **no financial schema**. The database contains no tables for accounts,
currencies, categories, transactions, movements, exchange rates, credits, loans
or instalments — and no placeholder or example tables either.

M0 built the storage foundation, not the storage. The database file exists, is
correctly configured, and is empty.

There is also no migration machinery yet. Migrations arrive in M1 together with
the first real domain table; see [milestones.md](milestones.md) and
[technical-decisions.md](technical-decisions.md), TD-012.

This section will be replaced during M1 with the real schema: tables, columns,
primary keys, foreign keys, constraints, indexes and relationships.

---

## Persistence conventions

These conventions are already approved and apply to every table added from M1
onward.

### Engine

SQLite, a single local file. See
[technical-decisions.md](technical-decisions.md), TD-001.

### Strict typing

Business tables are declared `STRICT`:

```sql
CREATE TABLE example (
    ...
) STRICT;
```

SQLite's default type affinity accepts a string where an integer was intended
and stores it as-is. `STRICT` makes column types actually mean something, which
matters most precisely where this project is most sensitive: numeric amounts.

Any table that is not `STRICT` needs a written justification recorded here.

### Referential integrity

- `foreign_keys` is `ON` for every connection, enforced through the DSN rather
  than a one-off statement. See [architecture.md](architecture.md).
- Foreign keys are declared explicitly, with deliberate `ON DELETE` / `ON UPDATE`
  behaviour rather than the default.
- Every table has an explicit primary key.

The enforcement is not taken on trust: a test creates a parent/child pair and
asserts that inserting an orphan row actually fails.

### Money — non-negotiable

**Monetary amounts are never stored as `REAL`, and never represented in Go as
`float32` or `float64`.**

Binary floating point cannot represent ordinary decimal amounts exactly. The
error is small per operation and compounds silently across aggregation, which is
exactly what a finance application does constantly. A balance that is wrong by a
cent is a balance that cannot be reconciled against a bank statement.

Amounts are stored as **integer minor units** — `INTEGER` in SQLite, `int64` in
Go — alongside the currency they are denominated in. An amount without its
currency is meaningless and the two are never separated.

Any alternative exact representation must be approved and recorded here before
use. "Approved exact representation" does not mean "a float with rounding".

### Exchange rates

Exchange rates use an **exact representation**: an integer numerator and
denominator, or scaled integers with a recorded scale. Never a float.

Rates are historical, not just current. A past figure is converted at the rate
that applied at the time, not at today's rate — otherwise historical net worth
silently rewrites itself every day.

The concrete representation is decided in the milestone that introduces
multi-currency conversion (currently expected at M4), and will be documented
here at that point.

### Dates and times

Not yet decided. This will be settled in M1 alongside the first table that needs
it, and recorded here.

---

## What this document will contain from M1

- Table definitions with columns and types
- Primary keys
- Foreign keys and their referential actions
- Check constraints and uniqueness constraints
- Indexes, with the queries that justify them
- Relationships between entities
- The migration that introduced each change
