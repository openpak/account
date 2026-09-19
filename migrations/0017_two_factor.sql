-- Two-factor authentication: TOTP (RFC 6238) with single-use recovery codes.
--
-- secret_enc is AES-256-GCM over the raw TOTP secret, nonce-prefixed, with the
-- account id as associated data; the key comes from ACCOUNT_TOTP_KEY (or the
-- session secret when that is unset). enabled_at is NULL while an enrolment
-- waits for its first code. last_step is the newest time step accepted, so a
-- code cannot be replayed. failures/failures_since rate-limit guessing across
-- every path that checks a code: 5 wrong codes lock the account's second
-- factor for 15 minutes.
CREATE TABLE account_two_factor (
    account_id      UUID PRIMARY KEY REFERENCES accounts(id),
    secret_enc      BYTEA NOT NULL,
    enabled_at      TIMESTAMPTZ,
    last_step       BIGINT NOT NULL DEFAULT 0,
    failures        INT NOT NULL DEFAULT 0,
    failures_since  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Ten per enrolment, peppered SHA-256 at rest like every other token.
CREATE TABLE two_factor_recovery_codes (
    account_id  UUID NOT NULL REFERENCES accounts(id),
    code_hash   TEXT NOT NULL,
    used_at     TIMESTAMPTZ,
    PRIMARY KEY (account_id, code_hash)
);

-- The half-finished sign-in between a correct password and a correct code.
-- Short-lived, single-use, and burned after 5 wrong codes.
CREATE TABLE login_challenges (
    id          UUID PRIMARY KEY,
    account_id  UUID NOT NULL REFERENCES accounts(id),
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    failures    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_login_challenges_account ON login_challenges(account_id);
