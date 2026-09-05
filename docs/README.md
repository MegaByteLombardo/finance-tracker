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
| [data-model.md](data-model.md) | The current schema — `currencies` and `accounts` — column by column, with its constraints, relationships and lifecycle, plus the persistence conventions every future table follows. |
| [operations.md](operations.md) | Running, configuring, observing and backing up the application locally and under Docker. |
| [technical-decisions.md](technical-decisions.md) | Numbered record of the architectural decisions taken so far, each with its rationale and the alternatives rejected. |

## Keeping these current

A change that alters architecture, schema, a technical decision, operational
behaviour, or a milestone's status must update the corresponding document in the
same change. Documentation that lags behind the code is worse than no
documentation, because it is trusted.
