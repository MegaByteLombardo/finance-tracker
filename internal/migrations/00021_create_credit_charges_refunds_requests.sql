-- +goose Up

-- Three small tables of FACTS and ASSERTIONS (M8.1). None of them holds a figure
-- that could be derived: an amount, a balance or an allocation. They say what a
-- transaction IS, beyond its type.

-- A financial charge the issuer made: interest, a fee, a penalty.
--
-- The charge is an EXPENSE on the card -- it is spending, it lowers the net
-- worth, and M6 shows it in the cash flow under its category like any other
-- expense (TD-068). What this row adds is the one fact the type cannot carry:
-- that the issuer charged it, and as what. The FX analytics (M8.3) classify a
-- card's debits STRUCTURALLY by whether this row exists -- never by category
-- and never by description (TD-044).
--
-- Go verifies that the transaction is an EXPENSE on a liability account.
CREATE TABLE credit_charges (
    transaction_id INTEGER NOT NULL PRIMARY KEY
                           REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    kind           TEXT    NOT NULL
                           CHECK (kind IN ('INTEREST', 'FEE', 'PENALTY')),

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- Which purchase a refund gives money back for, when the user says.
--
-- The link is optional -- a refund nobody can trace is still a refund -- and it
-- is an assertion, not a calculation. Go verifies that the original is an
-- EXPENSE on the same account and that the refunds linked to it never add up to
-- more than it: a purchase cannot be refunded twice over.
CREATE TABLE transaction_refunds (
    refund_transaction_id   INTEGER NOT NULL PRIMARY KEY
                                    REFERENCES transactions(id)
                                    ON DELETE RESTRICT
                                    ON UPDATE RESTRICT,

    original_transaction_id INTEGER NOT NULL
                                    REFERENCES transactions(id)
                                    ON DELETE RESTRICT
                                    ON UPDATE RESTRICT,

    created_at              TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                    CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    CHECK (refund_transaction_id <> original_transaction_id)
) STRICT;

-- "How much of this purchase has been refunded already", and the foreign key's
-- RESTRICT check.
CREATE INDEX idx_transaction_refunds_original ON transaction_refunds(original_transaction_id);

-- The idempotency of a form submission (TD-095).
--
-- Every M8 form carries a request key rendered by the server. The first
-- submission records what it produced; a second one -- a double click, a
-- browser retry -- finds the key and returns that result instead of recording
-- the operation again. Go looks for the key first, inside the transaction that
-- would write; the primary key is the backstop.
--
-- What a request produced is a transaction, an account, or both: opening a card
-- creates an account and, with a previous debt, its opening balance.
CREATE TABLE operation_requests (
    request_key    TEXT    NOT NULL PRIMARY KEY
                           CHECK (length(request_key) BETWEEN 16 AND 64
                                  AND NOT request_key GLOB '*[^0-9a-zA-Z_-]*'),

    transaction_id INTEGER REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    account_id     INTEGER REFERENCES accounts(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- IS NOT NULL never evaluates to NULL, so this cannot silently pass (TD-025).
    CHECK (transaction_id IS NOT NULL OR account_id IS NOT NULL)
) STRICT;

CREATE INDEX idx_operation_requests_transaction_id ON operation_requests(transaction_id);
CREATE INDEX idx_operation_requests_account_id     ON operation_requests(account_id);

-- +goose Down

DROP INDEX idx_operation_requests_account_id;
DROP INDEX idx_operation_requests_transaction_id;
DROP TABLE operation_requests;
DROP INDEX idx_transaction_refunds_original;
DROP TABLE transaction_refunds;
DROP TABLE credit_charges;
