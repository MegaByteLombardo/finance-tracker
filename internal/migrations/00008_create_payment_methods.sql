-- +goose Up

-- Payment methods stop being a fixed enumeration and become rows the user
-- administers.
--
-- M2 hard-coded four channels in a CHECK constraint. That was right while the
-- set was small and stable, and wrong as soon as real use produced "Pago móvil",
-- "Zelle" and "Débito": adding one meant a migration, and a personal finance
-- application should not need a deployment to record how someone paid for lunch.
--
-- A payment method is the CHANNEL an operation with the outside world used. It
-- is still not a transfer between the user's own accounts -- a method named
-- "Bank transfer" describes how money reached or left an account, whereas a
-- future transaction_type of TRANSFER will mean money moved between two accounts
-- we own. Confusing the two would make the ledger unreadable.
CREATE TABLE payment_methods (
    id         INTEGER NOT NULL PRIMARY KEY,

    name       TEXT    NOT NULL
                       CHECK (length(trim(name)) BETWEEN 1 AND 60),

    -- Archived, never deleted: transactions reference methods permanently, and
    -- the foreign key added in 00009 is ON DELETE RESTRICT to enforce it.
    is_active  INTEGER NOT NULL DEFAULT 1
                       CHECK (is_active IN (0, 1)),

    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                       CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                       CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Exact uniqueness, deliberately NOT `COLLATE NOCASE`.
--
-- categories uses NOCASE, and this table does not. The divergence is considered:
-- NOCASE folds ASCII only, so it would treat "Zelle" and "zelle" as the same
-- while treating "Café" and "CAFÉ" as different. Claiming case-insensitive
-- uniqueness while delivering it for half the alphabet is worse than not
-- claiming it, and importing ICU to do it properly is a dependency this
-- application will not carry for one index. The domain trims the name before
-- comparing, so the remaining difference is only letter case.
--
-- Changing categories to match would mean another destructive migration for no
-- functional gain, so M2's choice stands there. Both are recorded in
-- data-model.md.
CREATE UNIQUE INDEX idx_payment_methods_name ON payment_methods(name);

-- The four methods M2 hard-coded, under the names the interface already showed
-- for them. Migrating the values rather than the codes means nothing visible
-- changes on upgrade: a transaction that displayed "Point of sale" yesterday
-- displays "Point of sale" today.
--
-- Nothing else is seeded. "Pago móvil", "Zelle" and the rest are exactly what
-- the user is now able to create, and guessing at them here would be inventing
-- rows on someone's behalf -- the same reason categories has no seed.
INSERT INTO payment_methods (id, name) VALUES
    (1, 'Bank transfer'),
    (2, 'Point of sale'),
    (3, 'Cash'),
    (4, 'Other');

-- +goose Down

-- This migration is FORWARD-ONLY.
--
-- Once M3 has run, the user can create payment methods that M2's four-value
-- CHECK constraint cannot represent. A Down would have to either discard those
-- rows or collapse them into 'OTHER', and both destroy information about where
-- money actually went. This project does not invent lossy mappings.
--
-- The supported way back is the snapshot taken automatically before these
-- migrations ran:
--
--     docker compose stop
--     docker compose run --rm app restore --file finance-<stamp>-premigration.db
--     docker compose up -d
--
-- The statement below fails deliberately. A Down section that quietly did
-- nothing would let goose mark the migration as rolled back while the schema
-- stayed exactly as it was, which is a worse outcome than an error.
SELECT 'forward-only migration: restore the pre-migration backup instead'
  FROM this_migration_is_forward_only_restore_the_pre_migration_backup;
