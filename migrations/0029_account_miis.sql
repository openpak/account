-- The account's primary Mii, shared by every console network.
--
-- One row per account. ver3 is the canonical Mii (the Wii U / 3DS store data);
-- rfl (Wii) and char_info (Switch) are its encodings, computed when the row is
-- written so a read is a plain lookup and the two never drift from ver3. The
-- row goes with the account at erasure (identity.FinalizeDeletion).
CREATE TABLE account_miis (
    account_id  UUID PRIMARY KEY REFERENCES accounts(id),
    ver3        BYTEA NOT NULL CHECK (octet_length(ver3) = 96),
    rfl         BYTEA NOT NULL CHECK (octet_length(rfl) = 76),
    char_info   BYTEA NOT NULL CHECK (octet_length(char_info) = 88),
    name        TEXT NOT NULL,
    version     TEXT NOT NULL,
    source      TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
