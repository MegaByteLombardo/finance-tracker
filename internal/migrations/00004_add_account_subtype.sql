-- +goose Up

-- A real case arrived after M1: bank accounts need to be told apart as current
-- or savings accounts.
--
-- This is NOT two more values in account_type. That column classifies the
-- logical container the money sits in, and CHECKING/SAVINGS is a different axis
-- entirely -- one that only exists for BANK. Folding them in would have made
-- "a savings account at Mercantil" and "cash" siblings in the same enumeration,
-- and it would have left CASH, WALLET and OTHER with no way to say which they
-- are. A second, dependent column says exactly what is true: this account is
-- held at an institution, and there it is a current or a savings account.
--
-- The dependency is enforced here rather than only in Go:
--
--     account_type =  BANK  ->  account_subtype is CHECKING or SAVINGS
--     account_type <> BANK  ->  account_subtype is NULL
--
-- The IS NOT NULL clause is load-bearing. A CHECK constraint fails only when
-- its expression is FALSE, and NULL is not FALSE: without it, a BANK row with a
-- NULL subtype evaluates `NULL IN ('CHECKING','SAVINGS')` to NULL, the whole
-- expression to NULL, and the row is ACCEPTED. This was verified against real
-- SQLite before the constraint was written, not assumed.
--
-- SQLite cannot add a constraint to an existing table, so this is the
-- documented table rebuild. It is done here, before anything references
-- accounts, which is what makes dropping the table safe with foreign keys on:
-- there are no children to orphan and no other table whose REFERENCES clause
-- needs rewriting. Doing it after account_movements existed would have required
-- deferring foreign keys, so the ordering of these four migrations is
-- deliberate.
CREATE TABLE accounts_new (
    id              INTEGER NOT NULL PRIMARY KEY,

    name            TEXT    NOT NULL
                            CHECK (length(trim(name)) BETWEEN 1 AND 120),

    account_type    TEXT    NOT NULL
                            CHECK (account_type IN ('BANK', 'CASH', 'WALLET', 'OTHER')),

    -- Editable metadata, like name and institution: an account genuinely
    -- reclassified from current to savings is corrected, not re-created.
    account_subtype TEXT    CHECK (account_subtype IS NULL
                                   OR account_subtype IN ('CHECKING', 'SAVINGS')),

    currency_code   TEXT    NOT NULL
                            REFERENCES currencies(code)
                            ON DELETE RESTRICT
                            ON UPDATE RESTRICT,

    institution     TEXT    CHECK (institution IS NULL
                                   OR length(trim(institution)) BETWEEN 1 AND 120),

    is_active       INTEGER NOT NULL DEFAULT 1
                            CHECK (is_active IN (0, 1)),

    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                            CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                            CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    CHECK (
        (account_type =  'BANK' AND account_subtype IS NOT NULL
                                AND account_subtype IN ('CHECKING', 'SAVINGS'))
        OR
        (account_type <> 'BANK' AND account_subtype IS NULL)
    )
) STRICT;

-- Existing bank accounts predate the distinction, so there is no recorded
-- answer for them. CHECKING is the neutral default -- it is what an unqualified
-- "bank account" means -- and the column is editable, so correcting one is a
-- normal edit rather than a migration. Copying through the new constraint is
-- also what validates the existing data: a rebuild applies the rule to rows
-- that are already there, which ALTER TABLE ADD COLUMN would not have done.
INSERT INTO accounts_new (id, name, account_type, account_subtype,
                          currency_code, institution, is_active, created_at, updated_at)
SELECT id,
       name,
       account_type,
       CASE WHEN account_type = 'BANK' THEN 'CHECKING' END,
       currency_code,
       institution,
       is_active,
       created_at,
       updated_at
FROM accounts;

DROP TABLE accounts;

ALTER TABLE accounts_new RENAME TO accounts;

-- Dropped with the old table, so it has to be recreated. SQLite does not index
-- the child columns of a foreign key automatically; without this, every
-- RESTRICT check on a currency scans the whole table.
CREATE INDEX idx_accounts_currency_code ON accounts(currency_code);

-- +goose Down

CREATE TABLE accounts_old (
    id            INTEGER NOT NULL PRIMARY KEY,
    name          TEXT    NOT NULL
                          CHECK (length(trim(name)) BETWEEN 1 AND 120),
    account_type  TEXT    NOT NULL
                          CHECK (account_type IN ('BANK', 'CASH', 'WALLET', 'OTHER')),
    currency_code TEXT    NOT NULL
                          REFERENCES currencies(code)
                          ON DELETE RESTRICT
                          ON UPDATE RESTRICT,
    institution   TEXT    CHECK (institution IS NULL
                                 OR length(trim(institution)) BETWEEN 1 AND 120),
    is_active     INTEGER NOT NULL DEFAULT 1
                          CHECK (is_active IN (0, 1)),
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- The subtype is discarded. That is what rolling this back means.
INSERT INTO accounts_old (id, name, account_type, currency_code, institution,
                          is_active, created_at, updated_at)
SELECT id, name, account_type, currency_code, institution, is_active, created_at, updated_at
FROM accounts;

DROP TABLE accounts;

ALTER TABLE accounts_old RENAME TO accounts;

CREATE INDEX idx_accounts_currency_code ON accounts(currency_code);
