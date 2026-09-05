-- +goose Up

-- An account is denominated in exactly one currency. A bank offering both USD
-- and VES under one contract is modelled as two accounts, which keeps the
-- future ledger single-currency by construction: every movement inherits its
-- account's currency, balances are a plain SUM, and a cross-currency transfer
-- becomes explicit instead of an invisible change of composition.
--
-- There is deliberately no balance column of any kind. Balances will be derived
-- from the movement ledger; storing one here would mean maintaining a
-- denormalised value that can silently disagree with its own history.
CREATE TABLE accounts (
    -- INTEGER PRIMARY KEY is a rowid alias. No UUID: this is one local SQLite
    -- file, so there is no distributed generation or database merging to
    -- accommodate. No AUTOINCREMENT either -- it only prevents reuse of rowids
    -- from deleted rows, and rows here are never deleted.
    id            INTEGER NOT NULL PRIMARY KEY,

    -- Not UNIQUE on purpose: two accounts legitimately share a name across
    -- different institutions, and forcing uniqueness would force artificial
    -- names.
    name          TEXT    NOT NULL
                          CHECK (length(trim(name)) BETWEEN 1 AND 120),

    -- Classifies the logical container the money sits in, and nothing else.
    -- Purpose ("savings", "emergency fund") is a different axis: one can save
    -- in a bank account, in cash or in a wallet, so folding it in here would
    -- force a false choice between two simultaneously true facts. If a purpose
    -- dimension is ever needed it arrives as its own decision.
    account_type  TEXT    NOT NULL
                          CHECK (account_type IN ('BANK', 'CASH', 'WALLET', 'OTHER')),

    -- NOT NULL matters independently of the foreign key: a NULL child column
    -- always satisfies a foreign key, so without this an account could exist
    -- with no currency at all.
    --
    -- RESTRICT on both sides: currencies are retired with is_active, never
    -- deleted, and the code must not be renamed out from under the accounts
    -- denominated in it -- that would rewrite their financial identity.
    currency_code TEXT    NOT NULL
                          REFERENCES currencies(code)
                          ON DELETE RESTRICT
                          ON UPDATE RESTRICT,

    -- Nullable: cash held outside any institution genuinely has none, which is
    -- different from an empty string. The CHECK keeps '' out so that "no
    -- institution" has exactly one representation.
    institution   TEXT
                          CHECK (institution IS NULL OR length(trim(institution)) BETWEEN 1 AND 120),

    -- Accounts are archived, not deleted. Once movements exist, deleting an
    -- account would orphan history or cascade destructively over it.
    is_active     INTEGER NOT NULL DEFAULT 1
                          CHECK (is_active IN (0, 1)),

    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                          CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- SQLite does not index the child columns of a foreign key automatically.
-- Without this, every RESTRICT check on a currency delete or update scans the
-- whole accounts table, as would any future join.
CREATE INDEX idx_accounts_currency_code ON accounts(currency_code);

-- +goose Down

DROP INDEX idx_accounts_currency_code;
DROP TABLE accounts;
