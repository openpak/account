-- Adapter-domain credentials (PRD FR-1/§7): console-transformed secrets,
-- stored by the core so the adapter never holds password material.
-- Distinct from web passwords; never usable as a web session.
CREATE TABLE adapter_credentials (
    namespace   TEXT NOT NULL,
    account_id  UUID NOT NULL REFERENCES accounts(id),
    secret_hash TEXT NOT NULL,           -- bcrypt of the adapter-forwarded secret
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, account_id)
);
