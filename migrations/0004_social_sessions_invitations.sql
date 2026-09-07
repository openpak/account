-- Canonical friend graph, blocks, live-session registry, and invitations
-- (PRD SW-05..SW-07). The core owns these; adapters project and enforce via
-- RPC answers only.

CREATE TABLE friendships (
    id           UUID PRIMARY KEY,
    requester_id UUID NOT NULL REFERENCES accounts(id),
    addressee_id UUID NOT NULL REFERENCES accounts(id),
    state        TEXT NOT NULL CHECK (state IN ('pending','accepted','removed')),
    idempotency_key TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (requester_id, addressee_id)
);

CREATE INDEX idx_friendships_addressee ON friendships(addressee_id, state);
CREATE INDEX idx_friendships_requester ON friendships(requester_id, state);

CREATE TABLE account_blocks (
    blocker_id UUID NOT NULL REFERENCES accounts(id),
    blocked_id UUID NOT NULL REFERENCES accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id)
);

CREATE TABLE adapter_sessions (
    session_id   UUID PRIMARY KEY,
    account_id   UUID NOT NULL REFERENCES accounts(id),
    namespace    TEXT NOT NULL,
    title_id     TEXT NOT NULL,
    endpoint_ref TEXT NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- database-enforced single live routing target per (account, title);
    -- registration upserts atomically on this key.
    UNIQUE (account_id, title_id)
);

CREATE INDEX idx_adapter_sessions_account ON adapter_sessions(account_id, title_id);

CREATE TABLE invitations (
    id           UUID PRIMARY KEY,
    from_id      UUID NOT NULL REFERENCES accounts(id),
    to_id        UUID NOT NULL REFERENCES accounts(id),
    namespace    TEXT NOT NULL,
    title_id     TEXT NOT NULL,
    session_id   UUID NOT NULL,       -- live session bound at send time
    payload      BYTEA NOT NULL,
    nonce        UUID NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    state        TEXT NOT NULL DEFAULT 'pending'
                 CHECK (state IN ('pending','delivered','expired','rejected')),
    delivery_started_at TIMESTAMPTZ,  -- redelivery window marker (at-least-once)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);

CREATE INDEX idx_invitations_to ON invitations(to_id, namespace, state);
