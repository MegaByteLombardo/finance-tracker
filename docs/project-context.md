# Project context

## What this is

Finance Tracker is a personal application for getting complete visibility over
one person's finances: what is held, where, in which currency, what comes in,
what goes out, and how all of that has moved over time.

It is single-user and runs locally. There is no cloud component, no
multi-tenancy, no authentication and no telemetry.

## The problem it replaces

The finances it tracks currently live in a spreadsheet. A spreadsheet is a
genuinely good tool for this up to a point, and the point has been reached:

- Multiple accounts across multiple currencies mean the same amount has to be
  restated every time a rate changes, by hand.
- Historical exchange rates have to be kept manually if past figures are to stay
  meaningful, and they are easy to get subtly wrong.
- Credit, instalments and scheduled commitments do not fit a flat grid; they
  need dates, remaining balances and a calendar.
- Net worth over time requires reconstructing the past rather than reading it.
- Every new dimension makes the file harder to trust and harder to change.

The aim is not to be a better spreadsheet. It is to hold the same information in
a structure that can answer questions the spreadsheet cannot answer reliably.

## Functional vision

Everything in this section is the target. Most of it is **not implemented**; what
exists today is listed in the next section. It is recorded here so that the
technical decisions being taken now can be judged against where the project is
going.

**Accounts and balances**
- Multiple accounts of different kinds — *partly built (M1)*
- Cash held outside any institution — *partly built (M1)*
- Savings held separately from day-to-day money
- Balances, derived from the movement ledger

**Currencies**
- Multiple currencies held simultaneously — *partly built (M1)*
- Historical exchange rates, so that past figures remain accurate rather than
  being restated at today's rate
- Consolidated totals in a chosen reporting currency

**Movements**
- Income and expenses
- Categories, so that spending can be grouped and compared
- Transfers between accounts, including between accounts in different currencies

**Net worth**
- Total position at a point in time
- Historical evolution of net worth, not just the current figure

**Credit and commitments**
- Credit cards and lines of credit
- Loans taken and loans given
- Instalment plans with remaining balances
- A financial calendar of dated obligations and expected movements

**Analysis**
- Historical analysis across all of the above

## What exists today

The project is at milestone **M1**.

**Built:**

- The technical foundation from M0 — configuration, a correctly configured
  SQLite database, a health endpoint, graceful shutdown, a container image.
- **Currencies.** A seeded set (USD, EUR, VES), each with its name, symbol and
  minor unit. They can be created, renamed and archived; they are never deleted.
- **Accounts.** Multiple accounts across the four container types — bank, cash,
  wallet, other — each denominated in exactly one currency that never changes.
  They can be created, edited and archived; they are never deleted.
- Both are reachable through Go services, and covered by tests against real
  SQLite.

**Not built:**

- **No user interface.** Nothing renders any of this yet; the domain is exercised
  through its tests.
- **No amounts.** Accounts have no balance, and no monetary value is stored
  anywhere in the system. Balances will be derived from the movement ledger once
  it exists.
- No categories, transactions, transfers, exchange rates, credit, instalments,
  calendar or analysis.

The ordering is deliberate. M0 gave the first real financial data somewhere
correct to land — durable storage settings, enforced foreign keys, and the rule
that money is never a floating-point number. M1 gave it a shape to land in.
Amounts come next, into a schema that already knows what currency they are in.

See [milestones.md](milestones.md) for what M0 delivered and what is planned
next, and [data-model.md](data-model.md) for the persistence conventions that
the future domain will follow.

## Constraints that shape the design

- **Local-first.** The application runs on one machine. Deployment is Docker
  Compose. This keeps the whole system small enough to reason about.
- **Correctness over throughput.** This is financial data owned by one person.
  Durability settings are chosen to never lose an acknowledged write, even
  though that costs write performance that will never be missed at this volume.
- **Exact money.** Amounts and exchange rates are never represented as binary
  floating point. See [technical-decisions.md](technical-decisions.md), TD-013.
- **One account, one currency.** A bank offering several denominations is
  modelled as several accounts. This keeps the ledger single-currency by
  construction and makes cross-currency movement explicit. See TD-020.
- **Deliberate simplicity.** A modular monolith, the Go standard library, and as
  few dependencies as the job allows. Abstractions are added when a second case
  exists, not in anticipation of one.
