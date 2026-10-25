-- +goose NO TRANSACTION
-- +goose Up

-- Admit CREDIT_CARD as an account_type and REFUND as a transaction_type (M8.1).
--
--   CREDIT_CARD is the first LIABILITY: an account whose balance is what the
--   user owes. Nothing about the ledger's arithmetic changes for it -- a debt is
--   a negative balance, a purchase a negative movement, a payment a positive
--   one -- but it is a different KIND of account, and the application has to be
--   able to tell (TD-089). Its card-specific facts live in credit_cards (00020),
--   not here: an account is still a name, an institution and one currency.
--
--   REFUND is money given back for an expense: a POSITIVE movement filed under
--   an EXPENSE category, with a channel. It is not an income (nothing was
--   earned), and an expense cannot carry it because an expense's movement is
--   negative by construction (TD-090).
--
-- Both are values of a CHECK constraint, and SQLite cannot alter one. So both
-- tables are rebuilt. This time the TD-037 device -- rebuild the child too and
-- drop it first -- does not reach far enough: accounts and transactions are
-- referenced by account_movements AND by five tables of M7, and rebuilding every
-- child to drop it first would mean rebuilding half the schema to change two
-- lists of words. Instead this follows SQLite's own twelve-step procedure
-- (https://www.sqlite.org/lang_altertable.html, section 8), which is TD-088:
--
--   1. foreign_keys OFF, OUTSIDE any transaction ("This pragma is a no-op
--      within a transaction"). That is why this migration is NO TRANSACTION:
--      goose opens none, and the transaction below is our own.
--   2. BEGIN IMMEDIATE, written out because _txlock only governs BeginTx.
--   3. CREATE the new table under a temporary name, copy, DROP the old one,
--      RENAME the new one into place. NOT "rename the old one first": the
--      documentation warns that "might corrupt references to that table in
--      triggers, views, and foreign key constraints". The children keep naming
--      `accounts` and `transactions`, and those names resolve to the new tables
--      once the renames are done. Nothing that references them is rebuilt.
--   4. Recreate the indexes the DROP took with it.
--   5. foreign_key_check and integrity_check -- both, because "integrity_check
--      does not find FOREIGN KEY errors" -- then COMMIT, then foreign_keys ON.
--
-- Plain SQL cannot branch, so every precondition is an INSERT into a TEMP table
-- whose CHECK refuses a false guard. A failed guard fails its statement, goose
-- stops, and migrations.Up returns the error; cmd/web then closes the *sql.DB,
-- which discards the open transaction AND the connection that still has
-- foreign_keys OFF. Nothing is committed, and the next connection gets
-- foreign_keys from the DSN again. migrations.Up checks the post-condition
-- foreign_keys = 1 after a successful run (database.CheckForeignKeys).
--
-- The body is idempotent. If the process dies between our COMMIT and goose
-- recording the version, the next start runs this again: it rebuilds from
-- whatever is there and produces the same schema.
--
-- Columns, their order, every other CHECK, every default and every index are
-- exactly what 00004 and 00013 left. Only the two lists change.

PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

CREATE TEMP TABLE m00019_guard (
    guard TEXT    NOT NULL,
    ok    INTEGER NOT NULL CHECK (ok = 1)
);

-- With legacy_alter_table ON, RENAME would not rewrite references the way the
-- procedure relies on. With foreign_keys still ON, DROP TABLE would run an
-- implicit DELETE that the children's RESTRICT refuses.
INSERT INTO m00019_guard (guard, ok) VALUES ('legacy_alter_table', (SELECT legacy_alter_table FROM pragma_legacy_alter_table) IS 0);
INSERT INTO m00019_guard (guard, ok) VALUES ('foreign_keys_off', (SELECT foreign_keys FROM pragma_foreign_keys) IS 0);

CREATE TABLE accounts_new (
    id              INTEGER NOT NULL PRIMARY KEY,

    name            TEXT    NOT NULL
                            CHECK (length(trim(name)) BETWEEN 1 AND 120),

    -- CREDIT_CARD joins the four asset containers. Whether an account is an
    -- asset or a liability is DERIVED from this column in Go
    -- (account.Type.Nature) and never stored: a second column could only ever
    -- disagree with it.
    account_type    TEXT    NOT NULL
                            CHECK (account_type IN ('BANK', 'CASH', 'WALLET', 'OTHER', 'CREDIT_CARD')),

    -- A credit card has no subtype: the CHECK at the bottom keeps it NULL, as
    -- it does for everything that is not a BANK.
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

INSERT INTO accounts_new (id, name, account_type, account_subtype, currency_code, institution,
                          is_active, created_at, updated_at)
SELECT id, name, account_type, account_subtype, currency_code, institution,
       is_active, created_at, updated_at
  FROM accounts;

INSERT INTO m00019_guard (guard, ok) VALUES ('accounts_copied', (SELECT count(*) FROM accounts_new) IS (SELECT count(*) FROM accounts));

DROP TABLE accounts;
ALTER TABLE accounts_new RENAME TO accounts;

CREATE INDEX idx_accounts_currency_code ON accounts(currency_code);

CREATE TABLE transactions_new (
    id                INTEGER NOT NULL PRIMARY KEY,

    -- REFUND joins the five types M2 to M5.5 knew. There is no BUY_USD and no
    -- SELL_USD (see 00013), and there is no CARD_PURCHASE or CARD_PAYMENT
    -- either: a purchase with a card is an EXPENSE whose account is a
    -- liability, and a payment is a TRANSFER or an FX_EXCHANGE into one
    -- (TD-091). The labels are inferred, never stored.
    transaction_type  TEXT    NOT NULL
                              CHECK (transaction_type IN ('INCOME', 'EXPENSE', 'OPENING_BALANCE',
                                                          'TRANSFER', 'FX_EXCHANGE', 'REFUND')),

    category_id       INTEGER REFERENCES categories(id)
                              ON DELETE RESTRICT
                              ON UPDATE RESTRICT,

    payment_method_id INTEGER REFERENCES payment_methods(id)
                              ON DELETE RESTRICT
                              ON UPDATE RESTRICT,

    description       TEXT    CHECK (description IS NULL
                                     OR length(trim(description)) BETWEEN 1 AND 255),

    effective_date    TEXT    NOT NULL
                              CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                     AND date(effective_date) IS effective_date),

    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                              CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                              CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- The shape of each transaction type, in the database rather than only in
    -- the service.
    --
    -- A refund is shaped like an expense: it is categorised -- under the EXPENSE
    -- category whose spending it gives back, which Go verifies because a CHECK
    -- cannot reach the categories table -- and it has a channel. Its movement
    -- is positive, which is the one thing an expense cannot be.
    --
    -- Every clause is IS NULL / IS NOT NULL, which never evaluates to NULL, so
    -- the expression is always TRUE or FALSE and the constraint cannot silently
    -- pass. See TD-025.
    CHECK (
        (transaction_type IN ('INCOME', 'EXPENSE', 'REFUND')
            AND category_id       IS NOT NULL
            AND payment_method_id IS NOT NULL)
        OR
        (transaction_type = 'OPENING_BALANCE'
            AND category_id       IS NULL
            AND payment_method_id IS NULL)
        OR
        (transaction_type = 'TRANSFER'
            AND category_id       IS NULL
            AND payment_method_id IS NULL)
        OR
        (transaction_type = 'FX_EXCHANGE'
            AND category_id       IS NULL
            AND payment_method_id IS NULL)
    )
) STRICT;

INSERT INTO transactions_new (id, transaction_type, category_id, payment_method_id, description,
                              effective_date, created_at, updated_at)
SELECT id, transaction_type, category_id, payment_method_id, description,
       effective_date, created_at, updated_at
  FROM transactions;

INSERT INTO m00019_guard (guard, ok) VALUES ('transactions_copied', (SELECT count(*) FROM transactions_new) IS (SELECT count(*) FROM transactions));

DROP TABLE transactions;
ALTER TABLE transactions_new RENAME TO transactions;

CREATE INDEX idx_transactions_effective_date    ON transactions(effective_date, id);
CREATE INDEX idx_transactions_category_id       ON transactions(category_id);
CREATE INDEX idx_transactions_payment_method_id ON transactions(payment_method_id);

-- Every child of the two tables -- account_movements and the savings tables --
-- must still find its parent row, and the file must be sound.
INSERT INTO m00019_guard (guard, ok) VALUES ('foreign_key_check', (SELECT count(*) FROM pragma_foreign_key_check) IS 0);
INSERT INTO m00019_guard (guard, ok) VALUES ('integrity_check', (SELECT group_concat(integrity_check) FROM pragma_integrity_check) IS 'ok');

DROP TABLE temp.m00019_guard;

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down

-- FORWARD-ONLY, for the same reason as 00013 (TD-038). Once this has run the
-- user can open a credit card and record a refund, and the previous CHECK
-- constraints have no way to spell either. A Down would have to delete them --
-- with their movements, which are real debt and real money given back -- or
-- relabel them as something they are not.
--
-- The supported way back is the snapshot taken automatically before these
-- migrations ran:
--
--     docker compose stop
--     docker compose run --rm app restore --file finance-<stamp>-premigration.db
--     docker compose up -d
--
-- The statement below fails deliberately: a Down that quietly did nothing would
-- let goose mark the migration as rolled back while the schema stayed as it is.
SELECT 'forward-only migration: restore the pre-migration backup instead'
  FROM this_migration_is_forward_only_restore_the_pre_migration_backup;
