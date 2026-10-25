-- +goose Up

-- Statements and the assertions that tie movements to them (M8.2).
--
-- Like everything M8 stores, these are FACTS and ASSERTIONS, never results
-- (TD-092). A statement is what the issuer printed: the closing date, what was
-- owed then, the minimum and the due date, and -- when the bank reported them --
-- the previous balance, the payments, the debits and the past-due amount. What a
-- payment settles, what is still pending, which cycle is overdue and how far the
-- ledger is from the bank are derived from these rows and the ledger on every
-- read, and stored nowhere.

-- One statement the issuer emitted for a card.
--
-- closing_balance_minor follows the LEDGER's convention, the card's own
-- currency's minor units: negative when money was owed, positive for a credit
-- balance. It is the bank's figure (B), not the ledger's (L): the difference
-- between the two is what reconciliation shows, and recording a purchase late
-- never changes it.
--
-- previous_balance_minor follows the same convention; the other optional
-- figures are magnitudes. NULL is "the statement does not say", never zero.
--
-- A card's statements never overlap; that is checked in Go, where the
-- neighbouring rows are at hand. due_date is NULL when the statement does not
-- print one: an unknown due date never makes anything overdue.
CREATE TABLE credit_statements (
    id                     INTEGER NOT NULL PRIMARY KEY,

    card_account_id        INTEGER NOT NULL
                                   REFERENCES credit_cards(account_id)
                                   ON DELETE RESTRICT
                                   ON UPDATE RESTRICT,

    period_start           TEXT    NOT NULL
                                   CHECK (period_start GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                          AND date(period_start) IS period_start),
    closing_date           TEXT    NOT NULL
                                   CHECK (closing_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                          AND date(closing_date) IS closing_date),
    due_date               TEXT    CHECK (due_date IS NULL
                                          OR (due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
                                              AND date(due_date) IS due_date)),

    closing_balance_minor  INTEGER NOT NULL,
    minimum_payment_minor  INTEGER CHECK (minimum_payment_minor IS NULL OR minimum_payment_minor >= 0),

    previous_balance_minor INTEGER,
    payments_minor         INTEGER CHECK (payments_minor IS NULL OR payments_minor >= 0),
    debits_minor           INTEGER CHECK (debits_minor IS NULL OR debits_minor >= 0),
    past_due_minor         INTEGER CHECK (past_due_minor IS NULL OR past_due_minor >= 0),

    created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                   CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    updated_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                   CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    -- Both dates are NOT NULL and fixed-width, so the comparison is never NULL.
    CHECK (period_start <= closing_date),
    -- IS NULL first, so a missing due date passes by intent, not by accident
    -- (TD-025).
    CHECK (due_date IS NULL OR due_date > closing_date),

    UNIQUE (card_account_id, closing_date)
) STRICT;

-- The bank included a movement in a statement other than the one its date
-- implies (R7): a purchase posted a day late, a payment made on the closing day
-- and processed the next. The row ASSERTS membership; without one, membership
-- is derived from the date and the calendar and never stored.
--
-- Go checks that the transaction moves the statement's card and that the
-- statement closes on or after the transaction's date: nothing is billed before
-- it happens.
CREATE TABLE credit_statement_assignments (
    transaction_id INTEGER NOT NULL PRIMARY KEY
                           REFERENCES transactions(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    statement_id   INTEGER NOT NULL
                           REFERENCES credit_statements(id)
                           ON DELETE RESTRICT
                           ON UPDATE RESTRICT,

    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                           CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- "Which movements were asserted into this statement", and the foreign key's
-- RESTRICT check.
CREATE INDEX idx_credit_statement_assignments_statement_id ON credit_statement_assignments(statement_id);

-- What a refund after the closing date does to the billed balance (R2.5). It
-- always lowers the debt and the next statement; whether the issuer also lowers
-- what the CURRENT statement asks for is a policy of the card, and for Banco
-- Plaza it is still UNKNOWN until a real statement shows it.
ALTER TABLE credit_cards ADD COLUMN refund_policy TEXT NOT NULL DEFAULT 'NEXT_STATEMENT'
    CHECK (refund_policy IN ('NEXT_STATEMENT', 'REDUCES_BILLED'));

-- +goose Down

ALTER TABLE credit_cards DROP COLUMN refund_policy;
DROP INDEX idx_credit_statement_assignments_statement_id;
DROP TABLE credit_statement_assignments;
DROP TABLE credit_statements;
