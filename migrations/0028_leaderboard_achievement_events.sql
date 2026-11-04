-- leaderboard.record: a new best on a core leaderboard (account_id = the player, subject_id =
-- "<title>/<board>"). achievement.unlocked: a trophy unlock (subject_id = "<title>/<achievement>").
-- Both are announced by the core leaderboards and achievements services so console adapters can
-- push a notification.
ALTER TABLE invalidation_events DROP CONSTRAINT invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
 CHECK(type IN ('account_banned','account_unbanned','link_unlinked','account_delete_started','account_deleted',
 'credential_revoked','friend_requested','friend_accepted','friend_removed','invitation_sent','message','email_changed',
 'presence_changed','account_created','handle_assigned','account_restored',
 'leaderboard.record','achievement.unlocked'));
