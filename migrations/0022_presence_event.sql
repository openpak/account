-- presence_changed: an account's presence moved (a session began, ended or lapsed). account_id is
-- a friend who is online somewhere, namespace is the network that friend is online on, subject_id
-- is the account whose presence changed. One event per (friend, network), so a network with none
-- of the person's friends online hears nothing.
ALTER TABLE invalidation_events DROP CONSTRAINT IF EXISTS invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
    CHECK (type IN ('account_banned','account_unbanned','link_unlinked',
                    'account_delete_started','account_deleted','credential_revoked',
                    'friend_requested','friend_accepted','friend_removed',
                    'invitation_sent','message','email_changed','presence_changed'));
