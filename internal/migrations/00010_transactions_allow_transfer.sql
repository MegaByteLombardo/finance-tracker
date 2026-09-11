-- +goose Up

-- Admit TRANSFER as a fourth transaction_type: money moving between two of the
-- user's own accounts.
--
-- This is the change the two-table ledger was built for. A transfer is ONE
-- financial event with TWO impacts, and nothing in the shape of the data has to
-- move for it: account_movements.transaction_id was never UNIQUE, and every
-- balance is already SUM(amount_minor) over whatever movements exist. No column
-- is added anywhere. In particular transactions does NOT gain source_account_id,
-- destination_account_id or amount -- those three facts are exactly what the two
-- movements already state, and a second copy could only ever disagree with them.
--
-- What has to change is a CHECK constraint, and SQLite cannot alter one. So the
-- table is rebuilt, and the rebuild follows TD-037 exactly as migration 00009
-- established it:
--
--   transactions is a PARENT and account_movements is its child. Dropping a
--   parent while children still reference it fails. PRAGMA defer_foreign_keys
--   does not help -- it fails at COMMIT instead. PRAGMA foreign_keys = OFF works
--   but is connection-scoped, a no-op inside a transaction, and would force this
--   migration out of goose's transaction.
--
--   So the CHILD is rebuilt as well, and the OLD CHILD IS DROPPED FIRST. With
--   account_movements gone, nothing references the old transactions table and
--   dropping it is ordinary. Renaming the new parent into place makes SQLite
--   rewrite the new child's REFERENCES clause to follow it.
--
-- foreign_keys stays ON throughout and this stays a normal transactional
-- migration. Nothing is lost: ids, types, categories, payment methods, effective
-- dates, descriptions, timestamps and every movement are copied across.

CREATE TABLE transactions_new (
    id                INTEGER NOT NULL PRIMARY KEY,

    -- TRANSFER joins the three types M2 and M3 knew. It is not income and not
    -- an expense: it moves money the user already had from one of their
    -- accounts to another, so it must never appear in a "what did I earn" or
    -- "what did I spend" figure, and it must not change the total held in its
    -- currency.
    --
    -- It is also NOT the payment method named "Bank transfer". That method is
    -- the CHANNEL an operation with the outside world used; this type means
    -- money moved between two accounts we own. Confusing the two would make the
    -- ledger unreadable.
    transaction_type  TEXT    NOT NULL
                              CHECK (transaction_type IN ('INCOME', 'EXPENSE', 'OPENING_BALANCE', 'TRANSFER')),

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
    -- Income and expenses are categorised and have a channel. An opening balance
    -- has neither, because it states where an account started rather than
    -- recording an operation. A transfer has neither either, but for a different
    -- reason worth spelling out separately rather than folding into the same
    -- clause: moving money between two of one's own accounts is not income and
    -- not an expense, so filing it under a category would contaminate every
    -- future analysis of them, and it uses no channel with the outside world at
    -- all.
    --
    -- Every clause is IS NULL / IS NOT NULL, which never evaluates to NULL, so
    -- the expression is always TRUE or FALSE and the constraint cannot silently
    -- pass. See TD-025.
    CHECK (
        (transaction_type IN ('INCOME', 'EXPENSE')
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
    )
) STRICT;

-- A straight copy. Every column carries across unchanged, ids included: no
-- transaction is renumbered, so nothing that references one has to be patched.
INSERT INTO transactions_new
    (id, transaction_type, category_id, payment_method_id, description,
     effective_date, created_at, updated_at)
SELECT id, transaction_type, category_id, payment_method_id, description,
       effective_date, created_at, updated_at
FROM transactions;

-- The child is rebuilt only so that it can be dropped before its parent. Its
-- definition is unchanged from 00009, pointed at transactions_new.
--
-- Nothing here needs to change for transfers. transaction_id is still
-- deliberately NOT UNIQUE -- from this migration on that is no longer a
-- statement about the future but about the present, because a TRANSFER has two
-- rows in this table. amount_minor is still a plain signed integer with no
-- currency column: a movement is denominated in its account's currency by
-- construction, and a transfer's two accounts share one.
CREATE TABLE account_movements_new (
    id             INTEGER NOT NULL PRIMARY KEY,

    transaction_id INTEGER NOT NULL
                           REFERENCES transactions_new(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    account_id     INTEGER NOT NULL
                           REFERENCES accounts(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    amount_minor   INTEGER NOT NULL
                           CHECK (amount_minor <> 0),

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

INSERT INTO account_movements_new (id, transaction_id, account_id, amount_minor, created_at)
SELECT id, transaction_id, account_id, amount_minor, created_at FROM account_movements;

-- Child first. This is what makes the next statement legal.
DROP TABLE account_movements;
DROP TABLE transactions;

-- Renaming the parent rewrites the REFERENCES clause in account_movements_new
-- to name `transactions`, which is why the child does not need patching.
ALTER TABLE transactions_new      RENAME TO transactions;
ALTER TABLE account_movements_new RENAME TO account_movements;

-- Indexes go with the tables they were built on, so all five are recreated,
-- identical to 00009. Each answers a real query; see 00006 and 00007.
--
-- No index is added for transfers. The listing's self-join from a transaction to
-- its second movement is served by idx_account_movements_transaction_id, and the
-- transaction_type predicate that picks a transfer's anchor row drives no search
-- of its own.
CREATE INDEX idx_transactions_effective_date    ON transactions(effective_date, id);
CREATE INDEX idx_transactions_category_id       ON transactions(category_id);
CREATE INDEX idx_transactions_payment_method_id ON transactions(payment_method_id);

CREATE INDEX idx_account_movements_account_id     ON account_movements(account_id);
CREATE INDEX idx_account_movements_transaction_id ON account_movements(transaction_id);

-- +goose Down

-- FORWARD-ONLY, for the same reason as 00008 and 00009 (TD-038).
--
-- Once this migration has run the user can record a TRANSFER, and M3's CHECK
-- constraint has no way to spell one. A Down would have to either discard those
-- transactions and their movements -- destroying the record of money that
-- actually moved -- or relabel them as something they are not. This project does
-- not invent lossy mappings.
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
