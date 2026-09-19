-- Public codes on adapter links (website contract request CR-3).
--
-- A link's subject is the console's own user id: opaque, sometimes secret-ish,
-- never what people read out to each other. The human code (the Switch friend
-- code) is minted by the adapter; the adapter publishes it here so every
-- surface reads one value from one place. The core stores it and never mints
-- or interprets it.
--
-- public_code is the display form ("SW-1234-5678-9012"); public_code_key is
-- the form lookups compare on: whatever the adapter declared, or else the
-- display form's letters and digits lower-cased. Unique per namespace among
-- active links, so a code resolves to at most one account.
ALTER TABLE adapter_links ADD COLUMN public_code TEXT NOT NULL DEFAULT '';
ALTER TABLE adapter_links ADD COLUMN public_code_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_adapter_links_public_code
    ON adapter_links(namespace, public_code_key)
    WHERE state = 'active' AND public_code_key <> '';
