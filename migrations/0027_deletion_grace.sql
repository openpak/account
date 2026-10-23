-- Account erasure is automatic (owner decision 2026-10-23). Deleting closes
-- the account at once (status deletion_pending); erase_after is when the
-- website's finaliser erases it everywhere; until then signing in or an admin
-- restores it, back to the status it had before (a banned account stays banned).
-- closed_at is the request; deleted_at (0023) stays the moment of erasure.
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS erase_after TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS status_before_deletion TEXT;

-- Requests made before this rule never had a date; they get the full grace
-- period from the day it ships rather than an erasure nobody announced.
UPDATE accounts SET closed_at = updated_at, erase_after = now() + interval '30 days',
       status_before_deletion = 'active'
 WHERE status = 'deletion_pending' AND erase_after IS NULL;

CREATE INDEX IF NOT EXISTS accounts_erase_due ON accounts (erase_after) WHERE status = 'deletion_pending';

-- account_restored: a closed account came back within its grace period;
-- account_id is the account. Consumers undo what account_delete_started did.
ALTER TABLE invalidation_events DROP CONSTRAINT invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
 CHECK(type IN ('account_banned','account_unbanned','link_unlinked','account_delete_started','account_deleted',
 'credential_revoked','friend_requested','friend_accepted','friend_removed','invitation_sent','message','email_changed',
 'presence_changed','account_created','handle_assigned','account_restored'));
