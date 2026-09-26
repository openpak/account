-- Playtime runs from the moment a game server first asked for the player's
-- identity (nx-baas / nn-account call Sessions.MarkOnline at that login) until
-- the presence session ends. A title left open without ever reaching a game
-- server counts nothing. Replaces 0020's per-mark increments, which needed every
-- game server to report on a timer; a login is enough, and every game server
-- already makes one.
ALTER TABLE adapter_sessions ADD COLUMN IF NOT EXISTS online_since TIMESTAMPTZ;
-- ponytail: adapter_sessions.online_seconds / online_seen_at (0020) are unused
-- now; drop them in a later migration once no core that writes them is left.

CREATE OR REPLACE FUNCTION record_play_session() RETURNS trigger AS $$
DECLARE
    secs INTEGER := 0;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.expires_at > now()
       AND NEW.client = OLD.client AND NEW.os = OLD.os THEN
        RETURN NEW; -- a heartbeat, a game server's login, or a re-registration of the same live session
    END IF;
    IF OLD.online_since IS NOT NULL THEN
        secs := GREATEST(0, extract(epoch FROM OLD.updated_at - OLD.online_since))::int;
    END IF;
    IF OLD.title_id <> '' AND (OLD.updated_at > OLD.created_at OR secs > 0) THEN
        INSERT INTO play_sessions (account_id, namespace, title_id, client, os, started_at, ended_at, online_seconds)
        VALUES (OLD.account_id, OLD.namespace, OLD.title_id, OLD.client, OLD.os, OLD.created_at, OLD.updated_at, secs);
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    NEW.created_at := now();
    NEW.updated_at := now();
    NEW.online_since := NULL;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;
