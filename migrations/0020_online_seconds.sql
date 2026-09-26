-- Playtime counts only time a game server saw the player, not time the title
-- sat open: presence says what someone is running, a game server says they are
-- actually online in it. Game servers mark a player online every ~30 s
-- (Sessions.MarkOnline); each mark adds the time since the previous one to the
-- live presence session, unless the gap is too long to have been one stretch.
-- Additive: existing rows start at 0.
ALTER TABLE adapter_sessions
    ADD COLUMN IF NOT EXISTS online_seconds INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS online_seen_at TIMESTAMPTZ;
ALTER TABLE play_sessions ADD COLUMN IF NOT EXISTS online_seconds INTEGER NOT NULL DEFAULT 0;

-- 0019's trigger, carrying the online time into the finished session and
-- starting it over with a new one.
CREATE OR REPLACE FUNCTION record_play_session() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.expires_at > now()
       AND NEW.client = OLD.client AND NEW.os = OLD.os THEN
        RETURN NEW; -- a heartbeat, a game server's mark, or a re-registration of the same live session
    END IF;
    IF OLD.title_id <> '' AND (OLD.updated_at > OLD.created_at OR OLD.online_seconds > 0) THEN
        INSERT INTO play_sessions (account_id, namespace, title_id, client, os, started_at, ended_at, online_seconds)
        VALUES (OLD.account_id, OLD.namespace, OLD.title_id, OLD.client, OLD.os, OLD.created_at, OLD.updated_at, OLD.online_seconds);
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    NEW.created_at := now();
    NEW.updated_at := now();
    NEW.online_seconds := 0;
    NEW.online_seen_at := NULL;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;
