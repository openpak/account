-- Chat rides the universal event stream (universal-social-prd §4d): a
-- message is a fact an account must hear, delivered by each family's own
-- transport. account_id names the recipient; subject_id carries the chat
-- message id, so the receiver fetches the content from the chat service and
-- the event log never holds message bodies.
ALTER TABLE invalidation_events DROP CONSTRAINT IF EXISTS invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
    CHECK (type IN ('account_banned','account_unbanned','link_unlinked',
                    'account_delete_started','account_deleted','credential_revoked',
                    'friend_requested','friend_accepted','friend_removed',
                    'invitation_sent','message'));
