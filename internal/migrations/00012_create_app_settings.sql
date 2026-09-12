-- +goose Up

-- The application's own settings. Exactly one row, forever.
--
-- M0 decided configuration lives in the environment, and that was right while
-- every setting described the DEPLOYMENT: where the database file is, what
-- address to listen on, how many backups to keep. The base currency of valuation
-- is a different kind of fact. It is a choice the user makes from inside the
-- application, about their own money, and it has to be a foreign key to a row in
-- currencies -- an environment variable cannot be one, and a typo in it would be
-- discovered at valuation time rather than at the moment of the choice.
--
-- Why a typed column rather than a generic settings(key, value) table: only a
-- typed column can carry that foreign key. A TEXT value column holding 'USD'
-- references nothing, so archiving or renaming a currency could leave the
-- setting pointing at something that no longer exists, and nothing in the
-- database would object. That single argument decides it; the generic table
-- would also invite unrelated settings to accumulate in a place with no schema.
--
-- This is NOT the beginning of a preferences system. There is one user, one
-- process and one database file. There are no per-user settings, no profiles and
-- no scopes, and a second setting arrives as a second column when a second
-- setting actually exists.

CREATE TABLE app_settings (
    -- The singleton, enforced by the database rather than by convention. A
    -- second INSERT fails on the CHECK, so no code path anywhere can create a
    -- second row and leave later readers to pick one arbitrarily.
    id INTEGER NOT NULL PRIMARY KEY
               CHECK (id = 1),

    -- The currency every balance is valued in for the consolidated net worth.
    --
    -- Changing it changes NOTHING about the ledger. No transaction is rewritten,
    -- no movement is touched, and no balance moves: each account keeps its own
    -- currency and its own amount, and this column only decides which currency
    -- the consolidated figure is expressed in. It is presentation and valuation,
    -- never record.
    --
    -- NOT NULL matters independently of the foreign key: a NULL child column
    -- always satisfies a foreign key, so without it the single row could exist
    -- with no currency at all.
    base_currency_code TEXT NOT NULL
                            REFERENCES currencies(code)
                            ON DELETE RESTRICT
                            ON UPDATE RESTRICT,

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- The row is created here rather than lazily on first read, so that every later
-- read is a plain SELECT with no "or insert a default" branch, and so that
-- "there is no settings row" is an integrity failure rather than an ordinary
-- state something has to handle.
--
-- USD as the seeded value: it is one of the three currencies migration 00002
-- seeded, it is what this user's accounts are largely denominated in, and it is
-- the currency the other two are quoted against. It is a starting point, not a
-- constraint -- the setting exists precisely so it can be changed.
--
-- No index on base_currency_code. The rule that every foreign key child column
-- is indexed exists so a RESTRICT check does not scan the child table; this
-- child table has exactly one row, so the scan is the row.
INSERT INTO app_settings (id, base_currency_code) VALUES (1, 'USD');

-- +goose Down

-- Reversible for the same reason as 00011: purely additive, nothing
-- reinterpreted. Undoing it loses the chosen base currency, which is why Down is
-- still never run automatically.

DROP TABLE app_settings;
