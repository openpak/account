-- The rest of a friendship's life, so every surface a person uses (console friends
-- servers, the Switch adapter, the phone) hears about it without a full resync.
-- friend_accepted: account_id is the requester who was waiting, subject the acceptor.
-- friend_removed: emitted once per side; account_id is who loses the entry, subject the other.
ALTER TABLE invalidation_events DROP CONSTRAINT IF EXISTS invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
    CHECK (type IN ('account_banned','account_unbanned','link_unlinked',
                    'account_delete_started','account_deleted','credential_revoked',
                    'friend_requested','invitation_sent','friend_accepted','friend_removed'));
