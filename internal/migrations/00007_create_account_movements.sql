-- +goose Up

-- A movement is the IMPACT of a transaction on one account: a signed amount, in
-- that account's currency, and nothing else.
--
-- This table is the single source of truth for every balance in the
-- application. A balance is SUM(amount_minor) over these rows, computed when it
-- is read. There is no balance column on accounts and there will not be one: a
-- stored balance is a denormalised value that can drift from the history it
-- claims to summarise, and when it does, neither figure can be trusted.
CREATE TABLE account_movements (
    id             INTEGER NOT NULL PRIMARY KEY,

    -- Deliberately NOT UNIQUE. In M2 every transaction has exactly one
    -- movement, so a unique constraint would hold today and be true for the
    -- wrong reason: a transfer between two accounts is one transaction with two
    -- movements, and that is the whole reason these are separate tables.
    transaction_id INTEGER NOT NULL
                           REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    account_id     INTEGER NOT NULL
                           REFERENCES accounts(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    -- Integer minor units -- cents, céntimos -- never REAL. Binary floating
    -- point cannot represent ordinary decimal amounts exactly and the error
    -- compounds silently across summation, which is precisely what this column
    -- exists to be summed for. STRICT is what makes that stick: it refuses a
    -- REAL value outright instead of storing it under type affinity.
    --
    -- There is no currency column here, and none on transactions. A movement is
    -- denominated in its account's currency by construction, because an account
    -- has exactly one; a second column could only ever disagree with it.
    -- Cross-currency movement belongs to the FX milestone.
    --
    -- Signed: income is positive, an expense negative, an opening balance
    -- whichever the account started at. The sign is derived from the use case
    -- in the service, never taken from input, which is what makes "an expense
    -- that increases a balance" unrepresentable.
    --
    -- Zero is refused. A zero-amount movement asserts nothing -- the sum over
    -- no rows is already zero -- while adding a row to every listing, so a zero
    -- opening balance records no transaction at all.
    amount_minor   INTEGER NOT NULL
                           CHECK (amount_minor <> 0),

    -- There is no updated_at. A movement is never edited: correcting one means
    -- correcting the transaction that caused it.
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Every balance query filters or groups on this column:
--   SELECT COALESCE(SUM(amount_minor), 0) ... WHERE account_id = ?
-- It also serves the RESTRICT check that stops an account with history being
-- deleted, which SQLite would otherwise answer with a full scan.
--
-- A covering index on (account_id, amount_minor) would let the sum be answered
-- from the index alone. It was considered and left out: at the scale of one
-- person's finances the difference is unmeasurable, and an index carries its
-- cost on every write forever.
CREATE INDEX idx_account_movements_account_id ON account_movements(account_id);

-- The join from a transaction to its movements, which is how every listing is
-- built, and the foreign key's RESTRICT check.
CREATE INDEX idx_account_movements_transaction_id ON account_movements(transaction_id);

-- +goose Down

DROP INDEX idx_account_movements_transaction_id;
DROP INDEX idx_account_movements_account_id;
DROP TABLE account_movements;
