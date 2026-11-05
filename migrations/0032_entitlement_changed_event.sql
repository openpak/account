-- entitlement_changed (openpak/prds#141): the rewards service announces that what an account owns or has equipped
-- in a game changed (account_id set, namespace empty, subject_id = the game id). Game adapters drop their cached
-- inventory for that account and game on it.
ALTER TABLE invalidation_events DROP CONSTRAINT invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
 CHECK(type IN ('account_banned','account_unbanned','link_unlinked','account_delete_started','account_deleted',
 'credential_revoked','friend_requested','friend_accepted','friend_removed','invitation_sent','message','email_changed',
 'presence_changed','account_created','handle_assigned','account_restored',
 'leaderboard.record','achievement.unlocked','profile_updated','entitlement_changed'));
