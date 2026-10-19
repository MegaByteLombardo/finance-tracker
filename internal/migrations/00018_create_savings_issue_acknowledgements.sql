-- +goose Up

-- M7.4: acknowledging a savings issue.
--
-- Issues themselves are DERIVED on every read and stored nowhere: an account
-- holding less than is reserved on it, an income with more confirmed than its
-- live suggestion, a spend whose expense changed. Storing them would be a
-- second copy of facts the ledger and the fund already state.
--
-- What is stored is the user's acknowledgement that they have seen one and
-- decided to leave it for now (TD-087). It changes NO balance and removes no
-- deficit: the issue keeps being reported, marked as acknowledged, and the
-- overallocated figure keeps appearing on the fund and the dashboard until the
-- money is actually released, moved or the balance recovers.
--
-- The key names the issue AND its amount ("overallocated:account:3:10000"), so
-- an acknowledgement covers exactly what was seen: if the deficit grows, the
-- key changes and the issue is unacknowledged again.

CREATE TABLE savings_issue_acknowledgements (
    issue_key  TEXT NOT NULL PRIMARY KEY
                    CHECK (length(issue_key) BETWEEN 1 AND 120),

    note       TEXT NOT NULL
                    CHECK (length(trim(note)) BETWEEN 1 AND 255),

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z')
) STRICT;

-- +goose Down

-- Reversible: additive. Undoing it forgets which issues were acknowledged.

DROP TABLE savings_issue_acknowledgements;
