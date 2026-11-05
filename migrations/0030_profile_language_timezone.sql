-- The account core owns language (BCP 47 style tag, e.g. 'en', 'pt-BR') and timezone (IANA name) for every
-- network; adapters read them instead of keeping their own copy. Both live on the accounts row, so account
-- erasure already removes them. profile_updated: any public profile change (account_id only; re-read the account).
ALTER TABLE accounts ADD COLUMN language TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN timezone TEXT NOT NULL DEFAULT '';
ALTER TABLE invalidation_events DROP CONSTRAINT invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
 CHECK(type IN ('account_banned','account_unbanned','link_unlinked','account_delete_started','account_deleted',
 'credential_revoked','friend_requested','friend_accepted','friend_removed','invitation_sent','message','email_changed',
 'presence_changed','account_created','handle_assigned','account_restored',
 'leaderboard.record','achievement.unlocked','profile_updated'));
