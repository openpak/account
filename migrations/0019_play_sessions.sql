-- Playtime per title, client and OS, so the website can mark a combination
-- tested once people have actually played it online there.
--
-- os is what an emulator said it runs on ("windows", "macos", "linux",
-- "android", "ios"); empty for a real console, which names no OS, and for an
-- emulator too old to say. Additive and defaulted like client (0018).
ALTER TABLE adapter_sessions ADD COLUMN IF NOT EXISTS os TEXT NOT NULL DEFAULT '';

-- One row per finished session in a title. A live session is not here yet; it
-- is still in adapter_sessions, and readers add the two.
CREATE TABLE IF NOT EXISTS play_sessions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id UUID NOT NULL,
    namespace  TEXT NOT NULL,
    title_id   TEXT NOT NULL,
    client     TEXT NOT NULL,
    os         TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_play_sessions_title ON play_sessions(namespace, title_id);

-- A session ends in one of two ways, and both go through a row change here, so
-- a trigger catches every path the Go code has (explicit expiry, reap on read,
-- ban invalidation) without each of them remembering to:
--   * the row is deleted;
--   * the row is registered over: after its lease ran out (the upsert revives
--     a dead row) or from another client or OS. That is a new session, so the
--     old one is written out and created_at restarts, which also stops presence
--     reporting "since" a session that ended hours ago.
-- The session's length is created_at to its last renewal (updated_at), so a
-- lease nobody renewed adds nothing.
CREATE OR REPLACE FUNCTION record_play_session() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.expires_at > now()
       AND NEW.client = OLD.client AND NEW.os = OLD.os THEN
        RETURN NEW; -- a heartbeat or a re-registration of the same live session
    END IF;
    IF OLD.title_id <> '' AND OLD.updated_at > OLD.created_at THEN
        INSERT INTO play_sessions (account_id, namespace, title_id, client, os, started_at, ended_at)
        VALUES (OLD.account_id, OLD.namespace, OLD.title_id, OLD.client, OLD.os, OLD.created_at, OLD.updated_at);
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    NEW.created_at := now();
    NEW.updated_at := now();
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS adapter_sessions_play ON adapter_sessions;
CREATE TRIGGER adapter_sessions_play BEFORE UPDATE OR DELETE ON adapter_sessions
    FOR EACH ROW EXECUTE FUNCTION record_play_session();
