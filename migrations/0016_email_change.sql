-- Email change (POST /v1/email/change, /v1/email/confirm).
--
-- lifecycle_tokens has admitted purpose 'email_change' since 0001; the token
-- now carries the address it would move the account to. The address is only
-- applied when the link mailed to it is followed, so it is proven on arrival.
ALTER TABLE lifecycle_tokens ADD COLUMN IF NOT EXISTS new_email TEXT;

-- email_changed: account_id is the account whose address changed. The id is
-- unchanged, so sessions and links stay valid; the event is for anything that
-- keeps a copy of the address (the website's index, adapters' caches).
ALTER TABLE invalidation_events DROP CONSTRAINT IF EXISTS invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
    CHECK (type IN ('account_banned','account_unbanned','link_unlinked',
                    'account_delete_started','account_deleted','credential_revoked',
                    'friend_requested','friend_accepted','friend_removed',
                    'invitation_sent','message','email_changed'));
