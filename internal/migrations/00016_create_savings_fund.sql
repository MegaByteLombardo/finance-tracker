-- +goose Up

-- M7.2: the savings fund -- USD the user has set aside, marked against the real
-- accounts that hold it.
--
-- The fund is NOT an account and holds no money of its own. A reservation says
-- "of the balance of this USD account, this much is savings"; the dollars stay
-- exactly where the ledger says they are. Nothing here writes to transactions or
-- account_movements, and internal/valuation never reads these tables, so setting
-- money aside cannot change the net worth.
--
-- The shape is the ledger's own, one level up (TD-082):
--
--   savings_operations  the EVENT: what the user did, when, why, idempotency key
--   savings_entries     the IMPACT: a signed amount on one account (and, from
--                       M7.3, one goal)
--
-- Every reserved balance -- per account, per goal, the fund's total -- is
-- SUM(amount_minor) over entries, computed when it is read. There is no balance
-- column anywhere, and operations are never edited or deleted: a correction is
-- another operation. That is what makes the history auditable for free.
--
-- The kind enumeration is complete from this migration on, although M7.2 only
-- writes RESERVE and RELEASE (TD-082): the whole of M7 was approved at once, and
-- widening a CHECK on a parent table later would mean the TD-037 rebuild of
-- operations and entries in the middle of the milestone. Go refuses a kind no
-- use case writes yet.

CREATE TABLE savings_operations (
    id             INTEGER NOT NULL PRIMARY KEY,

    -- RESERVE     money set aside (positive entries), with or without a suggestion
    -- RELEASE     money given back to the free balance (negative), with a reason
    -- REALLOCATE  money moved between goals of one account (entries sum to zero
    --             per account) -- M7.3
    -- REBACK      reserved money moved between accounts with a transfer (entries
    --             sum to zero per goal) -- M7.4
    -- SPEND       reserved money consumed by a linked expense (negative) -- M7.4
    kind           TEXT    NOT NULL
                           CHECK (kind IN ('RESERVE', 'RELEASE', 'REALLOCATE', 'REBACK', 'SPEND')),

    -- A token the form carries, so that a double click or a browser retry
    -- replays the operation instead of performing it twice. Go looks for it
    -- first, in the same transaction; the index is the backstop.
    request_key    TEXT    NOT NULL
                           CHECK (length(request_key) BETWEEN 16 AND 64
                                  AND NOT request_key GLOB '*[^0-9a-zA-Z_-]*'),

    -- The suggestion revision a RESERVE confirms, when it confirms one. A
    -- reservation without a suggestion is legitimate: savings that predate M7,
    -- an extra contribution.
    suggestion_id  INTEGER REFERENCES savings_suggestions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    -- The ledger transaction an operation answers to: the expense a SPEND or a
    -- RELEASE was caused by, the transfer a REBACK follows (M7.4).
    transaction_id INTEGER REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    -- Why. Required for a RELEASE and for an exception.
    reason         TEXT    CHECK (reason IS NULL OR length(trim(reason)) BETWEEN 1 AND 255),

    -- 1 when the user explicitly confirmed MORE than what remained of the
    -- suggestion (TD-083). Recorded so the excess is a decision, not an accident.
    is_exception   INTEGER NOT NULL DEFAULT 0
                           CHECK (is_exception IN (0, 1)),

    -- The FINANCIAL date of the operation: the business day (Venezuela) the money
    -- was set aside or released. It is the date a confirmation happened, which is
    -- neither the income's effective date nor created_at. The fund's history is
    -- rebuilt from it.
    effective_date TEXT    NOT NULL
                           CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                  AND date(effective_date) IS effective_date),

    -- When it was recorded. Never updated: an operation is never edited.
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- The shape of each kind. Every clause is IS NULL / IS NOT NULL or a plain
    -- equality on a NOT NULL column, so none can evaluate to NULL (TD-025).
    CHECK (kind <> 'RELEASE' OR reason IS NOT NULL),
    CHECK (kind <> 'SPEND'   OR transaction_id IS NOT NULL),
    CHECK (suggestion_id IS NULL OR kind = 'RESERVE'),
    CHECK (is_exception = 0 OR (suggestion_id IS NOT NULL AND reason IS NOT NULL))
) STRICT;

-- The idempotency key, unique. Also the lookup a replay makes.
CREATE UNIQUE INDEX idx_savings_operations_request_key ON savings_operations(request_key);

-- Foreign-key children, and the joins "what was confirmed for this income" and
-- "what did this expense consume" make.
CREATE INDEX idx_savings_operations_suggestion_id  ON savings_operations(suggestion_id);
CREATE INDEX idx_savings_operations_transaction_id ON savings_operations(transaction_id);

-- The history is read by financial date.
CREATE INDEX idx_savings_operations_effective_date ON savings_operations(effective_date, id);

CREATE TABLE savings_entries (
    id           INTEGER NOT NULL PRIMARY KEY,

    operation_id INTEGER NOT NULL
                         REFERENCES savings_operations(id)
                         ON DELETE RESTRICT
                         ON UPDATE RESTRICT,

    -- The USD account whose balance backs this money. That it is in USD compares
    -- two tables and is checked in Go, in the same transaction.
    account_id   INTEGER NOT NULL
                         REFERENCES accounts(id)
                         ON DELETE RESTRICT
                         ON UPDATE RESTRICT,

    -- USD cents, signed: positive sets aside, negative gives back. Never zero --
    -- a zero entry asserts nothing. Integer minor units, never REAL.
    amount_minor INTEGER NOT NULL
                         CHECK (amount_minor <> 0),

    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                         CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Every reserved balance groups on the account; the RESTRICT checks need both.
CREATE INDEX idx_savings_entries_operation_id ON savings_entries(operation_id);
CREATE INDEX idx_savings_entries_account_id   ON savings_entries(account_id);

-- +goose Down

-- Reversible: additive. Undoing it loses every reservation, which is why Down is
-- never run automatically.

DROP INDEX idx_savings_entries_account_id;
DROP INDEX idx_savings_entries_operation_id;
DROP TABLE savings_entries;
DROP INDEX idx_savings_operations_effective_date;
DROP INDEX idx_savings_operations_transaction_id;
DROP INDEX idx_savings_operations_suggestion_id;
DROP INDEX idx_savings_operations_request_key;
DROP TABLE savings_operations;
