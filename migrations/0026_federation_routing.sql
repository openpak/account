-- Test nodes and the accounts allowed to reach them. A node flagged test is
-- never in the public /api/v1/regions list, and the routing table hands it
-- only to accounts on the allow-list: everyone else is routed as if it did
-- not exist. Rules for approval and rotation still apply to it.
ALTER TABLE federation_nodes ADD COLUMN test BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE federation_testers (
    account_id UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
