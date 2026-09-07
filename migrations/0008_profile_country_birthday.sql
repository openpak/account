-- Country and birthday, collected at registration.
--
-- The Switch's BAAS user object carries both, and the console rejects the whole object when
-- either is missing or malformed (an empty country or "0000-00-00" fails the link with
-- 2124-3121). They belong to the account, not to one adapter's guesswork.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS country text NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS birthday date;
