# Documentation

Permanent project documentation for Finance Tracker.

This is separate from the two documents at the repository root, which serve
different audiences:

- [`../README.md`](../README.md) — introduction and getting started. What the
  project is and how to run it.
- [`../CLAUDE.md`](../CLAUDE.md) — the working agreement for coding sessions.
  Short, operational rules.

Everything here is the project's technical contract: it describes what has been
decided and what is actually built, and it is expected to be kept current.

## Index

| Document | Contents |
| --- | --- |
| [project-context.md](project-context.md) | What Finance Tracker is for, the problem it replaces, and the full functional vision — with a clear line between what exists today and what is still aspiration. |
| [architecture.md](architecture.md) | The architecture as actually implemented: packages, dependency direction, and the SQLite configuration in force. No speculative layers. |
| [milestones.md](milestones.md) | The living roadmap. Milestone status (`PLANNED` / `IN PROGRESS` / `CLOSED`), what M0 delivered, and the provisional shape of everything after it. |
| [m7-contract.md](m7-contract.md) | The M7 agreement, grown per submilestone. M7.1: the four concepts M7 keeps apart (income, suggested, reserved, assigned), eligibility by type, category and override, the policy versioned by the income's effective date, one suggestion per eligible income in USD at the rate of its date, frozen with the observations used, revisions and derived confirmation states, the previewed sync and backfill, and the savings screens. |
| [m6.5-contract.md](m6.5-contract.md) | The M6.5 agreement between the backend and the presentation layer: the weekly and monthly recap, calendar periods and the like-for-like cut for a period in progress, every category compared with its direction, the largest expenses ranked in the base currency, the change of each account, and the four fixed-rule indicators. Frozen for the duration of M6.5. |
| [m6-contract.md](m6-contract.md) | The M6 agreement between the backend and the presentation layer: balances as of a date and "current" as today, archived accounts valued, one rate resolver for a date and a series, gaps for missing rates, the valuation snapshot and series, the analytics periods, cash flow, savings rate and spending, the dashboard surface, and the server-rendered SVG chart. Frozen for the duration of M6. |
| [m5.5-contract.md](m5.5-contract.md) | The M5.5 agreement between the backend and the presentation layer: `FX_EXCHANGE`, why it is not a `TRANSFER` and not income or an expense, the two amounts, the execution rate derived and never stored, its orientation rule, `RecordExchange` and `UpdateExchange`, the self-sufficient `ExchangeDetail`, and the line between reference FX and execution FX. Frozen for the duration of M5.5. |
| [m5-contract.md](m5-contract.md) | The M5 agreement between the backend and the presentation layer: reference FX and why it is not transaction FX, the `1 base = rate quote` orientation, exact decimals, half-even rounding, source precedence within a date, manual rates, the BCV provider, the rate lookup, inversion and cross rates, missing rates, and the consolidated net worth. Frozen for the duration of M5. |
| [m4-contract.md](m4-contract.md) | The M4 agreement between the backend and the presentation layer: `TRANSFER`, `RecordTransfer` and `UpdateTransfer`, the self-sufficient `TransferDetail`, the same-currency rule, archived accounts, negative balances, and the boundary that keeps transfers out of the income-and-expense form. Frozen for the duration of M4. |
| [m3-contract.md](m3-contract.md) | The M3 agreement between the backend and the presentation layer: payment methods as an entity, the editing APIs, backups, the canonical money format, and what M3 breaks from M2. Frozen for the duration of M3. |
| [m2-contract.md](m2-contract.md) | The M2 agreement between the backend and the presentation layer: the services, models, enumerations, money and date semantics, and errors the UI may rely on. Frozen for the duration of M2. |
| [data-model.md](data-model.md) | The current schema — `currencies` and `accounts` — column by column, with its constraints, relationships and lifecycle, plus the persistence conventions every future table follows. |
| [operations.md](operations.md) | Running, configuring, observing and backing up the application locally and under Docker. |
| [technical-decisions.md](technical-decisions.md) | Numbered record of the architectural decisions taken so far, each with its rationale and the alternatives rejected. |

## Keeping these current

A change that alters architecture, schema, a technical decision, operational
behaviour, or a milestone's status must update the corresponding document in the
same change. Documentation that lags behind the code is worse than no
documentation, because it is trusted.
