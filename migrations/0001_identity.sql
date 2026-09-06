-- core identity: accounts, sessions, lifecycle tokens, adapter links, audit
CREATE TABLE accounts (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL,
    email_normalized TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','banned','deletion_pending','deleted')),
    email_verified_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id          UUID PRIMARY KEY,
    account_id  UUID NOT NULL REFERENCES accounts(id),
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX idx_sessions_account ON sessions(account_id);

-- single-use expiring credentials for verify/reset/email-change
CREATE TABLE lifecycle_tokens (
    id          UUID PRIMARY KEY,
    account_id  UUID NOT NULL REFERENCES accounts(id),
    purpose     TEXT NOT NULL CHECK (purpose IN ('email_verify','password_reset','email_change')),
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_lifecycle_tokens_account ON lifecycle_tokens(account_id, purpose);

-- generic, console-neutral adapter links
CREATE TABLE adapter_links (
    id           UUID PRIMARY KEY,
    account_id   UUID NOT NULL REFERENCES accounts(id),
    namespace    TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT 'pending'
                 CHECK (state IN ('pending','active','revoking','revoked')),
    idempotency_key TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, subject_id)
);
CREATE INDEX idx_adapter_links_account ON adapter_links(account_id);
CREATE UNIQUE INDEX idx_adapter_links_active_subject
    ON adapter_links(namespace, subject_id) WHERE state = 'active';
