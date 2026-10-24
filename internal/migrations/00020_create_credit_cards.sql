-- +goose Up

-- What a credit card is beyond being an account (M8.1, TD-089).
--
-- The account itself -- its name, its institution, its one currency, whether it
-- is archived -- is a row of accounts with account_type = 'CREDIT_CARD', and
-- none of that is repeated here. This table holds only what a card has and a
-- bank account does not: the terms the issuer set.
--
-- There is no balance, no debt, no available credit and no cycle here, and there
-- will not be. What is owed is the account's balance, derived from its
-- movements like every other balance (TD-052); what is available is the limit
-- minus that, derived when read; which cycle a movement belongs to is derived
-- from its date and statement_day (M8.2). A stored copy of any of them would be
-- one more figure able to disagree with the ledger.
--
-- Go verifies that the account IS a credit card: a CHECK cannot reach accounts.
CREATE TABLE credit_cards (
    -- One row per card account, and the account's id is the key: a card is that
    -- account, described further.
    account_id                INTEGER NOT NULL PRIMARY KEY
                                      REFERENCES accounts(id)
                                      ON DELETE RESTRICT
                                      ON UPDATE RESTRICT,

    -- The issuer's name for the product ("Mastercard Black"). Optional: the
    -- account's own name is usually enough.
    product_name              TEXT    CHECK (product_name IS NULL
                                             OR length(trim(product_name)) BETWEEN 1 AND 120),

    -- The credit limit in the card's currency, in minor units. NULL means
    -- UNKNOWN, which is not zero and not "no limit": nothing is derived from a
    -- limit nobody stated.
    credit_limit_minor        INTEGER CHECK (credit_limit_minor IS NULL OR credit_limit_minor > 0),

    -- The day of the month the statement closes. 29, 30 and 31 are accepted:
    -- in a month that has no such day the cycle closes on its last day.
    statement_day             INTEGER NOT NULL
                                      CHECK (statement_day BETWEEN 1 AND 31),

    -- How many days after the closing date payment is due. NULL means UNKNOWN,
    -- and an unknown due date never makes anything overdue (M8.2).
    due_days_after_statement  INTEGER CHECK (due_days_after_statement IS NULL
                                             OR due_days_after_statement BETWEEN 1 AND 60),

    -- Business days the issuer takes to apply a payment. Informational until
    -- M8.5 turns it into calendar facts.
    payment_processing_days   INTEGER NOT NULL DEFAULT 0
                                      CHECK (payment_processing_days BETWEEN 0 AND 10),

    -- NOMINAL: the debt is a fixed number of units of the card's currency.
    -- INDEXED: the debt is re-expressed against an index (the UVC). Every
    -- figure M8 derives assumes NOMINAL; the FX analytics (M8.3) refuse an
    -- INDEXED card rather than explain it wrongly. denomination_confirmed says
    -- whether the user checked it against the contract, so an assumption is
    -- shown as one.
    denomination              TEXT    NOT NULL DEFAULT 'NOMINAL'
                                      CHECK (denomination IN ('NOMINAL', 'INDEXED')),
    denomination_confirmed    INTEGER NOT NULL DEFAULT 0
                                      CHECK (denomination_confirmed IN (0, 1)),

    -- The payment method a purchase form preselects. Optional, and only a
    -- convenience: every purchase still names its own.
    default_payment_method_id INTEGER REFERENCES payment_methods(id)
                                      ON DELETE RESTRICT
                                      ON UPDATE RESTRICT,

    created_at                TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                      CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),

    updated_at                TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                                      CHECK (updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- The foreign key's RESTRICT check when a payment method is archived or renamed.
CREATE INDEX idx_credit_cards_default_payment_method_id ON credit_cards(default_payment_method_id);

-- +goose Down

DROP INDEX idx_credit_cards_default_payment_method_id;
DROP TABLE credit_cards;
