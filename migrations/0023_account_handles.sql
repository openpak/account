-- Nullable only for the explicitly reviewed pre-handle account migration.
ALTER TABLE accounts ADD COLUMN handle TEXT;
ALTER TABLE accounts ADD COLUMN handle_assigned_at TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE accounts ADD CONSTRAINT accounts_handle_format
 CHECK (handle IS NULL OR handle COLLATE "C" ~ '^[A-Za-z][A-Za-z0-9]{5,14}$');
CREATE UNIQUE INDEX accounts_handle_unique ON accounts(lower(handle)) WHERE status <> 'deleted';

-- One claim per case-folded handle, retained for 90 days after final deletion.
CREATE TABLE account_handle_claims (
 handle_lower TEXT PRIMARY KEY,
 account_id UUID NOT NULL REFERENCES accounts(id),
 claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 release_at TIMESTAMPTZ
);
CREATE INDEX account_handle_claims_account ON account_handle_claims(account_id);

CREATE FUNCTION enforce_account_handle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF OLD.handle IS NOT NULL AND NEW.handle IS DISTINCT FROM OLD.handle THEN
   RAISE EXCEPTION 'handle_immutable' USING ERRCODE='23514', CONSTRAINT='account_handle_immutable';
  END IF;
  IF OLD.status = 'deleted' AND NEW.status <> 'deleted' THEN
   RAISE EXCEPTION 'deleted accounts cannot be restored' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.handle IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.handle IS NULL) THEN
  IF NEW.status <> 'active' THEN RAISE EXCEPTION 'handle requires active account' USING ERRCODE='23514'; END IF;
  -- Same lock is taken by name-bearing links, closing the registration/link race.
  PERFORM pg_advisory_xact_lock(hashtextextended(lower(NEW.handle), 431));
  IF EXISTS(SELECT 1 FROM adapter_links l JOIN accounts a ON a.id=l.account_id
    WHERE l.account_id <> NEW.id AND a.status <> 'deleted'
      AND l.state IN ('active','pending','revoking')
      AND l.namespace IN ('wiiu','xbox','x360','np') AND l.public_code_key=lower(NEW.handle)) THEN
   RAISE EXCEPTION 'handle_taken' USING ERRCODE='23505', CONSTRAINT='account_handle_taken';
  END IF;
  INSERT INTO account_handle_claims(handle_lower,account_id) VALUES(lower(NEW.handle),NEW.id)
   ON CONFLICT(handle_lower) DO UPDATE SET account_id=EXCLUDED.account_id, claimed_at=now(), release_at=NULL
   WHERE account_handle_claims.release_at <= now();
  IF NOT FOUND THEN RAISE EXCEPTION 'handle_taken' USING ERRCODE='23505', CONSTRAINT='account_handle_taken'; END IF;
  NEW.handle_assigned_at=now();
 END IF;
 IF TG_OP = 'UPDATE' AND NEW.status='deleted' AND OLD.status <> 'deleted' THEN
  NEW.deleted_at=now();
  -- A PSN online ID is a link subject. Retire the old textual subject while
  -- preserving its link id (and therefore historical derived user ids).
  UPDATE adapter_links SET state='revoked', public_code='', public_code_key='',
    subject_id=CASE WHEN namespace='np' THEN 'deleted:'||id::text ELSE subject_id END,
    updated_at=now() WHERE account_id=NEW.id;
  UPDATE account_handle_claims SET release_at=now()+interval '90 days' WHERE account_id=NEW.id AND release_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
-- Claims reference the account, so defer the FK until its INSERT has completed.
ALTER TABLE account_handle_claims DROP CONSTRAINT account_handle_claims_account_id_fkey;
ALTER TABLE account_handle_claims ADD FOREIGN KEY(account_id) REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED;
CREATE TRIGGER accounts_handle BEFORE INSERT OR UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION enforce_account_handle();

CREATE FUNCTION enforce_link_handle() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE chosen TEXT;
BEGIN
 IF NEW.namespace='np' AND NEW.state IN ('pending','active','revoking') THEN
  SELECT handle INTO chosen FROM accounts WHERE id=NEW.account_id FOR SHARE;
  IF chosen IS NOT NULL AND NEW.subject_id IS DISTINCT FROM chosen THEN
   RAISE EXCEPTION 'link must use account handle' USING ERRCODE='23514', CONSTRAINT='link_handle_mismatch';
  END IF;
 END IF;
 IF NEW.namespace IN ('wiiu','xbox','x360','np') AND NEW.state IN ('active','pending','revoking') AND COALESCE(NEW.public_code_key,'') <> '' THEN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.public_code_key,431));
  SELECT handle INTO chosen FROM accounts WHERE id=NEW.account_id FOR SHARE;
  IF chosen IS NOT NULL AND NEW.public_code IS DISTINCT FROM chosen THEN
   RAISE EXCEPTION 'link must use account handle' USING ERRCODE='23514', CONSTRAINT='link_handle_mismatch';
  END IF;
  IF EXISTS(SELECT 1 FROM account_handle_claims WHERE handle_lower=NEW.public_code_key AND account_id <> NEW.account_id
   AND (release_at IS NULL OR release_at > now())) THEN
   RAISE EXCEPTION 'handle_taken' USING ERRCODE='23505', CONSTRAINT='account_handle_taken';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER adapter_links_handle BEFORE INSERT OR UPDATE OF public_code, public_code_key, subject_id, state ON adapter_links
 FOR EACH ROW EXECUTE FUNCTION enforce_link_handle();

ALTER TABLE invalidation_events DROP CONSTRAINT invalidation_events_type_check;
ALTER TABLE invalidation_events ADD CONSTRAINT invalidation_events_type_check
 CHECK(type IN ('account_banned','account_unbanned','link_unlinked','account_delete_started','account_deleted',
 'credential_revoked','friend_requested','friend_accepted','friend_removed','invitation_sent','message','email_changed',
 'presence_changed','account_created','handle_assigned'));
