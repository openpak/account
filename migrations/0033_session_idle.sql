-- A title left open is not play. A console holds its presence lease for as long
-- as it is powered on, so a game left running (or suspended) overnight kept its
-- session renewing for a day and a half, and the play history counted all of it
-- (card #142). Heartbeats prove the console is on; only a game server seeing the
-- player (MarkOnline) or a fresh session proves someone is playing.
--
--   last_active_at  the last evidence of play: the session starting, or a game
--                   server marking the player online.
--   idle            the reaper (sessions.ReapIdle) found no evidence for the idle
--                   timeout and closed the stretch up to last_active_at. The row
--                   stays, so presence still shows the console signed in and the
--                   adapter is not made to re-register, but it counts nothing
--                   until a game server marks the player online again.
-- Additive. Existing rows start with last_active_at = now(), which gives every
-- live session one full idle timeout before it can be reaped.
ALTER TABLE adapter_sessions
    ADD COLUMN IF NOT EXISTS last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS idle BOOLEAN NOT NULL DEFAULT false;

-- 0021's trigger, except that an idle row has already been written out by the
-- reaper and must not be written a second time with its idle tail.
CREATE OR REPLACE FUNCTION record_play_session() RETURNS trigger AS $$
DECLARE
    secs INTEGER := 0;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.expires_at > now()
       AND NEW.client = OLD.client AND NEW.os = OLD.os THEN
        RETURN NEW; -- a heartbeat, a game server's login, or a re-registration of the same live session
    END IF;
    IF NOT OLD.idle THEN
        IF OLD.online_since IS NOT NULL THEN
            secs := GREATEST(0, extract(epoch FROM OLD.updated_at - OLD.online_since))::int;
        END IF;
        IF OLD.title_id <> '' AND (OLD.updated_at > OLD.created_at OR secs > 0) THEN
            INSERT INTO play_sessions (account_id, namespace, title_id, client, os, started_at, ended_at, online_seconds)
            VALUES (OLD.account_id, OLD.namespace, OLD.title_id, OLD.client, OLD.os, OLD.created_at, OLD.updated_at, secs);
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    NEW.created_at := now();
    NEW.updated_at := now();
    NEW.online_since := NULL;
    NEW.last_active_at := now();
    NEW.idle := false;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;
