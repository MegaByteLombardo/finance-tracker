-- +goose Up

-- Admit FX_EXCHANGE as a fifth transaction_type: a real exchange of money
-- between two accounts denominated in DIFFERENT currencies.
--
--     Banesco VES  -85,000.00 VES   →   Mercantil USD  +100.00 USD
--
-- It is not a TRANSFER. A transfer moves one sum of money between two accounts
-- that share a currency, so its two movements cancel out and the total held in
-- that currency is identical before and after. An exchange reduces a quantity of
-- one currency and increases a quantity of another; the two movements do NOT
-- cancel, and comparing their minor units to each other would be meaningless --
-- 8,500,000 céntimos of bolívar is not 10,000 cents of dollar.
--
-- It is also not income and not an expense: nothing was earned and nothing was
-- consumed, the same wealth is simply denominated differently afterwards. So it
-- carries no category and no payment method, exactly like a transfer, and the
-- table CHECK below refuses the row if either survives an edit.
--
-- NOTHING IS ADDED TO transactions. In particular there is no exchange_rate_id
-- and no rate column, and there must never be one. The rate this operation
-- happened at is stated exactly by the two amounts the user entered -- it is
-- source ÷ destination -- and a stored copy could only ever disagree with them.
-- The rates in exchange_rates are a different thing entirely: what a source SAID
-- a currency was worth on a day, used to value balances (M5, TD-052). Recording
-- an exchange touches that table not at all.
--
-- What has to change is a CHECK constraint, and SQLite cannot alter one. So the
-- table is rebuilt, and the rebuild follows TD-037 exactly as migrations 00009
-- and 00010 established it:
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

    -- FX_EXCHANGE joins the four types M2, M3 and M4 knew.
    --
    -- The DIRECTION of the pair of accounts is what says whether the user bought
    -- or sold a currency, so there is no BUY_USD and no SELL_USD: those would be
    -- two names for one event, distinguished by a fact the movements already
    -- state. "Compra de USD" and "Venta de USD" are labels a screen infers, not
    -- types the ledger stores.
    transaction_type  TEXT    NOT NULL
                              CHECK (transaction_type IN ('INCOME', 'EXPENSE', 'OPENING_BALANCE',
                                                          'TRANSFER', 'FX_EXCHANGE')),

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
    -- has neither, because it states where an account started. A transfer has
    -- neither, because moving money between one's own accounts is not an
    -- operation with the outside world. An exchange has neither either, and its
    -- reason is its own: money WAS exchanged with the outside world, but nothing
    -- was earned or spent, so filing it under a category would contaminate every
    -- later question about income and expenditure. A commission charged on top
    -- of the exchange is a separate EXPENSE, under a category such as "Bank
    -- fees"; it is not a field of this row.
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
        OR
        (transaction_type = 'FX_EXCHANGE'
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
-- definition is unchanged from 00010, pointed at transactions_new.
--
-- Nothing here needs to change for an exchange, and that is the point of the
-- two-table ledger. amount_minor is still a plain signed integer with NO
-- currency column: a movement is denominated in its account's currency by
-- construction. An exchange is the first transaction whose two movements are in
-- two different currencies, and it needs no new column to say so -- the two
-- accounts already do.
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
-- identical to 00010. Each answers a real query; see 00006 and 00007.
--
-- No index is added for exchanges, for the same reason none was added for
-- transfers: the listing's self-join from a transaction to its second movement
-- is served by idx_account_movements_transaction_id.
CREATE INDEX idx_transactions_effective_date    ON transactions(effective_date, id);
CREATE INDEX idx_transactions_category_id       ON transactions(category_id);
CREATE INDEX idx_transactions_payment_method_id ON transactions(payment_method_id);

CREATE INDEX idx_account_movements_account_id     ON account_movements(account_id);
CREATE INDEX idx_account_movements_transaction_id ON account_movements(transaction_id);

-- +goose Down

-- FORWARD-ONLY, for the same reason as 00008, 00009 and 00010 (TD-038).
--
-- Once this migration has run the user can record an FX_EXCHANGE, and M4's CHECK
-- constraint has no way to spell one. A Down would have to either discard those
-- transactions and their movements -- destroying the record of money that
-- actually changed hands, in two currencies -- or relabel them as something they
-- are not. There is no honest mapping: an exchange cannot become a transfer
-- (the currencies differ), and it cannot become an income plus an expense
-- without inventing a category for each.
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
