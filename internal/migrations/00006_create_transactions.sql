-- +goose Up

-- A transaction is the financial EVENT: what happened, when it happened
-- financially, why, and through which channel. It holds no amount and names no
-- account -- that is the movement's job, in the next migration.
--
-- The split exists because a transfer between two of the user's own accounts is
-- one event with two impacts. That is a later milestone, but a flat
-- transactions(account_id, amount) table would have to be migrated away from
-- when it arrives, and migrating a ledger is exactly the thing worth avoiding.
CREATE TABLE transactions (
    id               INTEGER NOT NULL PRIMARY KEY,

    transaction_type TEXT    NOT NULL
                             CHECK (transaction_type IN ('INCOME', 'EXPENSE', 'OPENING_BALANCE')),

    -- NULL for OPENING_BALANCE, required otherwise; enforced by the table CHECK
    -- below. RESTRICT because categories are archived rather than deleted, and
    -- deleting one out from under its transactions would erase why the money
    -- moved.
    category_id      INTEGER REFERENCES categories(id)
                             ON DELETE RESTRICT
                             ON UPDATE RESTRICT,

    -- The CHANNEL an operation with the outside world used. It is not a
    -- transfer between the user's own accounts: BANK_TRANSFER here means "this
    -- external operation moved through a bank transfer", whereas a future
    -- transaction_type of TRANSFER will mean "money moved between two accounts
    -- we own". Confusing the two would make the ledger unreadable.
    payment_method   TEXT    CHECK (payment_method IS NULL
                                    OR payment_method IN ('BANK_TRANSFER', 'POS', 'CASH', 'OTHER')),

    -- Optional free text. The CHECK keeps '' out, so "no description" has
    -- exactly one representation, matching institution and symbol.
    description      TEXT    CHECK (description IS NULL
                                    OR length(trim(description)) BETWEEN 1 AND 255),

    -- When it happened financially, which is not when it was recorded. A
    -- purchase made on the 3rd and entered on the 5th is dated the 3rd, and
    -- listings order by this rather than by created_at.
    --
    -- A calendar date, deliberately not a timestamp: "the purchase happened on
    -- 2026-09-03" is true in every timezone, whereas an instant is not.
    --
    -- The GLOB fixes the shape and rejects '2026-9-5'. date(x) IS x then
    -- rejects dates that are well-shaped but do not exist: date('2026-02-31')
    -- normalises to '2026-03-03', which is not equal to the input, and
    -- date('abcd-ef-gh') is NULL. IS rather than = because = would yield NULL
    -- for the NULL case, and a CHECK that evaluates to NULL passes.
    effective_date   TEXT    NOT NULL
                             CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                    AND date(effective_date) IS effective_date),

    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                             CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- Present for convention and for the editing that a later milestone will
    -- add. No M2 operation changes a transaction, so today it always equals
    -- created_at.
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                             CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- The shape of each transaction type, in the database rather than only in
    -- the service. Income and expenses are categorised and have a channel; an
    -- opening balance has neither, because it is a statement about where the
    -- account started rather than an operation that took place.
    --
    -- Every clause here is IS NULL / IS NOT NULL, which never evaluates to
    -- NULL, so the expression is always TRUE or FALSE and the constraint cannot
    -- silently pass. Verified against real SQLite for all ten combinations.
    CHECK (
        (transaction_type IN ('INCOME', 'EXPENSE')
            AND category_id     IS NOT NULL
            AND payment_method  IS NOT NULL)
        OR
        (transaction_type = 'OPENING_BALANCE'
            AND category_id     IS NULL
            AND payment_method  IS NULL)
    )
) STRICT;

-- What the transaction list screen does on every load:
--   ORDER BY effective_date DESC, id DESC LIMIT 50
-- SQLite can walk this index backwards to satisfy that ordering without a sort.
-- id is part of the key because several transactions share a date routinely,
-- and without it the order within a day would not be stable between calls.
CREATE INDEX idx_transactions_effective_date ON transactions(effective_date, id);

-- SQLite does not index the child columns of a foreign key. Without this, the
-- RESTRICT check when archiving or deleting a category scans every transaction,
-- as does the join that resolves a category name for the listing.
CREATE INDEX idx_transactions_category_id ON transactions(category_id);

-- +goose Down

DROP INDEX idx_transactions_category_id;
DROP INDEX idx_transactions_effective_date;
DROP TABLE transactions;
