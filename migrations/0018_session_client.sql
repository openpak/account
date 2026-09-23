-- Cross-network presence with client labels.
--
-- client names what a live session is running on inside its namespace:
-- "switch" (a real console), "ryujinx", "eden", "citron", "wiiu", "3ds",
-- "cemu", "azahar". Empty means the adapter could not tell; readers render
-- the namespace's own name then. Additive, defaulted, so older adapters that
-- never send it keep working.
ALTER TABLE adapter_sessions ADD COLUMN IF NOT EXISTS client TEXT NOT NULL DEFAULT '';

-- One live session per (account, namespace, title) instead of per (account,
-- title). Title ids never collide across console families, so for titled
-- sessions nothing changes; it is the title-less "signed in" session that
-- needs it: without the namespace, being online on a Switch and on a Wii U at
-- the same time is one row two adapters keep taking from each other.
--
-- Rolling back to a core that upserts ON CONFLICT (account_id, title_id) needs
-- the old key back:
--   DELETE FROM adapter_sessions a USING adapter_sessions b
--    WHERE a.account_id = b.account_id AND a.title_id = b.title_id
--      AND a.session_id > b.session_id;
--   ALTER TABLE adapter_sessions ADD CONSTRAINT adapter_sessions_account_title_uniq
--     UNIQUE (account_id, title_id);
ALTER TABLE adapter_sessions
    ADD CONSTRAINT adapter_sessions_account_ns_title_uniq
    UNIQUE (account_id, namespace, title_id);
ALTER TABLE adapter_sessions DROP CONSTRAINT IF EXISTS adapter_sessions_account_title_uniq;
ALTER TABLE adapter_sessions DROP CONSTRAINT IF EXISTS adapter_sessions_account_id_title_id_key;
