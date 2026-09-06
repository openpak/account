-- Distinguish resend-issued verification tokens from the initial signup
-- token so the resend throttle (10 min) never blocks the first repair.
ALTER TABLE lifecycle_tokens ADD COLUMN via_resend BOOLEAN NOT NULL DEFAULT FALSE;
