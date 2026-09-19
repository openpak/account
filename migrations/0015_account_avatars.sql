-- Account avatars (website contract request CR-2).
--
-- The website processes uploads (validate, crop, resize) and hands the core the
-- two PNGs it produced; adapters fetch them by account id to serve consoles.
-- Both sizes live in one row so a change is atomic: no reader ever sees a new
-- 512 next to an old 256. version is a content hash, for conditional fetches.
-- Postgres rather than a directory: the core already has exactly one piece of
-- state to back up, and at <=1 MiB + 256 KiB per account it stays that way.
CREATE TABLE account_avatars (
    account_id  UUID PRIMARY KEY REFERENCES accounts(id),
    png_256     BYTEA NOT NULL CHECK (octet_length(png_256) <= 262144),
    png_512     BYTEA NOT NULL CHECK (octet_length(png_512) <= 1048576),
    version     TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
