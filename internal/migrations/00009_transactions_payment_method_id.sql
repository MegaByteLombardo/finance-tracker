-- +goose Up

-- Point transactions at payment_methods instead of carrying the channel as text.
--
-- This rebuilds BOTH transactions and account_movements, and the order of
-- operations is the whole trick.
--
-- SQLite cannot alter a CHECK constraint, so transactions has to be rebuilt.
-- But transactions is a PARENT: account_movements references it. Dropping the
-- old parent while children still point at it fails with a foreign key
-- violation, and three approaches were tested against real SQLite before this
-- one was written:
--
--   1. Rebuild transactions alone.            DROP TABLE fails, as above.
--   2. Add PRAGMA defer_foreign_keys = ON.    Also fails -- at COMMIT. Dropping
--      the parent increments the deferred-violation counter once per child row,
--      and renaming a new table into its place does not decrement it. The data
--      ends up consistent and the transaction is refused anyway.
--   3. PRAGMA foreign_keys = OFF around it.   Works, but the PRAGMA is
--      connection-scoped and a no-op inside a transaction, so it would force
--      this migration out of goose's transaction and leave the connection in a
--      state the rest of the schema depends on being restored.
--
-- What is done instead needs none of that: rebuild the child as well, and drop
-- the OLD CHILD FIRST. With account_movements gone, nothing references the old
-- transactions table, so dropping it is ordinary. The new child is created
-- pointing at the new parent, and when the new parent is renamed into place
-- SQLite rewrites the child's REFERENCES clause to follow it.
--
-- foreign_keys stays ON throughout, and this stays a normal transactional
-- migration.
--
-- Nothing is lost: ids, transaction types, categories, effective dates,
-- descriptions, timestamps and every account movement are copied across.

CREATE TABLE transactions_new (
    id                INTEGER NOT NULL PRIMARY KEY,

    transaction_type  TEXT    NOT NULL
                              CHECK (transaction_type IN ('INCOME', 'EXPENSE', 'OPENING_BALANCE')),

    category_id       INTEGER REFERENCES categories(id)
                              ON DELETE RESTRICT
                              ON UPDATE RESTRICT,

    -- Replaces the payment_method TEXT column. RESTRICT because methods are
    -- archived rather than deleted, and removing one out from under its
    -- transactions would erase how the money moved.
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

    -- Unchanged in meaning from M2, restated against the new column: income and
    -- expenses are categorised and have a channel; an opening balance has
    -- neither, because it states where an account started rather than recording
    -- an operation.
    --
    -- Every clause is IS NULL / IS NOT NULL, which never evaluates to NULL, so
    -- the constraint is always TRUE or FALSE and cannot silently pass. See
    -- TD-025.
    CHECK (
        (transaction_type IN ('INCOME', 'EXPENSE')
            AND category_id       IS NOT NULL
            AND payment_method_id IS NOT NULL)
        OR
        (transaction_type = 'OPENING_BALANCE'
            AND category_id       IS NULL
            AND payment_method_id IS NULL)
    )
) STRICT;

-- The old text value maps to the row seeded for it in 00008. An opening balance
-- had no method and still has none.
INSERT INTO transactions_new
    (id, transaction_type, category_id, payment_method_id, description,
     effective_date, created_at, updated_at)
SELECT t.id,
       t.transaction_type,
       t.category_id,
       CASE t.payment_method
           WHEN 'BANK_TRANSFER' THEN 1
           WHEN 'POS'           THEN 2
           WHEN 'CASH'          THEN 3
           WHEN 'OTHER'         THEN 4
       END,
       t.description,
       t.effective_date,
       t.created_at,
       t.updated_at
FROM transactions t;

-- The child is rebuilt only so that it can be dropped before its parent. Its
-- definition is otherwise identical to 00007, pointed at transactions_new.
CREATE TABLE account_movements_new (
    id             INTEGER NOT NULL PRIMARY KEY,

    -- Still deliberately NOT UNIQUE: a transfer between two of the user's own
    -- accounts is one transaction with two movements, and that is why these are
    -- two tables.
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

-- Indexes go with the tables they were built on, so all of them are recreated.
-- Each answers a real query; see 00006 and 00007 for the reasoning.
CREATE INDEX idx_transactions_effective_date ON transactions(effective_date, id);
CREATE INDEX idx_transactions_category_id    ON transactions(category_id);

-- New, and required by the same rule as the others: SQLite does not index the
-- child columns of a foreign key, so without this every RESTRICT check when
-- archiving a payment method scans every transaction, as does the join that
-- resolves a method's name for the listing.
CREATE INDEX idx_transactions_payment_method_id ON transactions(payment_method_id);

CREATE INDEX idx_account_movements_account_id     ON account_movements(account_id);
CREATE INDEX idx_account_movements_transaction_id ON account_movements(transaction_id);

-- +goose Down

-- FORWARD-ONLY. See the Down section of 00008 for the reasoning: after M3 a
-- transaction can reference a payment method M2 has no way to spell, and any
-- reverse mapping would discard that.
--
-- Roll back by restoring the pre-migration snapshot instead.
SELECT 'forward-only migration: restore the pre-migration backup instead'
  FROM this_migration_is_forward_only_restore_the_pre_migration_backup;
