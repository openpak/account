-- Events a companion app is notified about.
--
-- The type column is deliberately a closed set, so the two events push hangs on
-- have to be admitted explicitly. Both name the person to be notified as
-- account_id: the addressee of a friend request, the recipient of an
-- invitation. The subject is who or what it is about.
ALTER TABLE invalidation_events DROP CONSTRAINT IF EXISTS invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
    CHECK (type IN ('account_banned','account_unbanned','link_unlinked',
                    'account_delete_started','account_deleted','credential_revoked',
                    'friend_requested','invitation_sent'));
