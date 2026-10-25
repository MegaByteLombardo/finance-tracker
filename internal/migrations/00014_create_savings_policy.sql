-- +goose Up

-- M7.1: the savings policy, and which income it applies to.
--
-- The policy is a percentage of each eligible income the user means to save. It
-- is a PLAN, not money: nothing in these tables moves a balance, reserves a cent
-- or appears in the net worth. What it produces is a suggestion per income
-- (00015), and setting money aside is a later, separate act (M7.2).
--
-- Three tables, because three different facts are recorded:
--
--   savings_policy_versions   what percentage applied from which day
--   savings_category_rules    which income categories are EXCLUDED
--   savings_income_overrides  explicit per-income exceptions, in both directions
--
-- Nothing here touches the ledger. transactions, account_movements and
-- categories are read, referenced and never altered: eligibility is a fact
-- about saving, not about the income, and a column on categories would have
-- meant rebuilding a parent table for a concern the ledger does not have.

-- A versioned rate. A row says: from effective_from on, save rate_bp basis
-- points of each eligible income.
--
-- The version that applies to an income is chosen by the income's
-- EFFECTIVE DATE -- the day it happened financially -- never by when the
-- version or the income was recorded. That is what keeps a change from 20% to
-- 25% from reaching back: an income dated before the new version's
-- effective_from is governed by the old one, whenever it is entered.
--
-- Append-only, with one narrow exception that loses nothing: a version no
-- suggestion has used yet may have its rate corrected (hence updated_at). Once
-- a suggestion references a version, no statement in the application changes
-- its rate -- the same treatment categories.category_type gets -- because the
-- suggestion's arithmetic depends on it. The rules on WHEN a version may start
-- (only the first may be retroactive) are cross-row and live in Go
-- (internal/savings, TD-077).
CREATE TABLE savings_policy_versions (
    id             INTEGER NOT NULL PRIMARY KEY,

    -- Basis points of each eligible income: 2000 is 20%. An integer, never a
    -- REAL, like every other percentage in this application. Zero is a
    -- legitimate policy -- "save nothing from this day on" -- and produces no
    -- suggestions.
    rate_bp        INTEGER NOT NULL
                           CHECK (rate_bp BETWEEN 0 AND 10000),

    -- A calendar date, with the same two clauses as transactions.effective_date.
    effective_from TEXT    NOT NULL
                           CHECK (effective_from GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                  AND date(effective_from) IS effective_from),

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Two versions starting on the same day would leave the choice between them to
-- row order. The index is also the lookup: the version in force on a date is
-- the one with the latest effective_from on or before it.
CREATE UNIQUE INDEX idx_savings_policy_versions_effective_from
    ON savings_policy_versions(effective_from);

-- Categories whose income is NOT saved from.
--
-- Every INCOME category is eligible by default; a row here is how one is
-- excluded ("Reembolsos", "Préstamos recibidos"), or re-included after having
-- been excluded. Eligible-by-default is the approved choice (TD-076): the
-- common case -- a salary -- works without configuration, and an exception is
-- something the user states.
--
-- That the category is an INCOME one compares two tables and cannot be a CHECK;
-- internal/savings verifies it in the same transaction as the write.
CREATE TABLE savings_category_rules (
    -- RESTRICT, like every reference to categories: they are archived, never
    -- deleted.
    category_id INTEGER NOT NULL PRIMARY KEY
                        REFERENCES categories(id)
                        ON DELETE RESTRICT
                        ON UPDATE RESTRICT,

    is_eligible INTEGER NOT NULL
                        CHECK (is_eligible IN (0, 1)),

    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                        CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                        CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- An explicit exception for one income, which wins over its category's rule.
--
-- In both directions: a reimbursement filed under "Salario" can be excluded,
-- and a one-off income in an excluded category can be included. The reason is
-- required because the exception is a decision the user will want to read back
-- -- and because eligibility is NEVER inferred from the description text.
--
-- The income being an INCOME is checked in Go, for the same reason as above.
CREATE TABLE savings_income_overrides (
    transaction_id INTEGER NOT NULL PRIMARY KEY
                           REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    is_eligible    INTEGER NOT NULL
                           CHECK (is_eligible IN (0, 1)),

    reason         TEXT    NOT NULL
                           CHECK (length(trim(reason)) BETWEEN 1 AND 255),

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- No further indexes. Both rule tables are keyed by the column every read and
-- every foreign key check uses, which is already their primary key.

-- +goose Down

-- Reversible, like 00011 and 00012: purely additive, nothing that existed before
-- is reinterpreted. Undoing it loses the policy and the eligibility rules, which
-- is why Down is still never run automatically. 00015 references this table and
-- must be rolled back first, which goose's ordering guarantees.

DROP TABLE savings_income_overrides;
DROP TABLE savings_category_rules;
DROP INDEX idx_savings_policy_versions_effective_from;
DROP TABLE savings_policy_versions;
