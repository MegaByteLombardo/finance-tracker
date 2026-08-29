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

Everything in this section is **future work**. None of it is implemented. It is
recorded here so that the technical decisions being taken now can be judged
against where the project is going.

**Accounts and balances**
- Multiple accounts of different kinds
- Cash held outside any institution
- Savings held separately from day-to-day money

**Currencies**
- Multiple currencies held simultaneously
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

**Nothing from the list above.**

The project is at milestone M0, which built the technical foundation only:
configuration, a correctly configured SQLite database, a health endpoint,
graceful shutdown, a container image, and tests. There are no accounts, no
currencies, no categories, no transactions, no rates, no credit and no
instalments — and no database tables for any of them.

This is deliberate. M0 exists so that the first real financial data has somewhere
correct to land: durable storage settings, enforced foreign keys, and a rule
that money is never stored as a floating-point number.

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
- **Deliberate simplicity.** A modular monolith, the Go standard library, and as
  few dependencies as the job allows. Abstractions are added when a second case
  exists, not in anticipation of one.
