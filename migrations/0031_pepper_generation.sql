-- Which pepper a stored password hash was made under: 0 is ACCOUNT_SESSION_SECRET as it always was,
-- 1 is ACCOUNT_SESSION_SECRET_NEW. Existing rows are 0. A hash is moved to the current pepper at the
-- next successful check, so the count of rows still at 0 measures the cutover.
ALTER TABLE accounts ADD COLUMN pepper_gen SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE adapter_credentials ADD COLUMN pepper_gen SMALLINT NOT NULL DEFAULT 0;
