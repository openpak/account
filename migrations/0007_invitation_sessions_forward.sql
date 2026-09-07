-- Forward migration for databases that applied the earlier revision of 0004:
-- invitations gain session binding + a delivery-redelivery marker, and
-- adapter_sessions gains the uniqueness that backs atomic registration.
-- Every statement is a safe no-op on databases created from the revised 0004.

ALTER TABLE invitations ADD COLUMN IF NOT EXISTS session_id UUID;
ALTER TABLE invitations ADD COLUMN IF NOT EXISTS delivery_started_at TIMESTAMPTZ;

-- Rows queued before session binding cannot be validated for delivery.
UPDATE invitations SET state = 'expired'
WHERE state = 'pending' AND session_id IS NULL;

-- Backfill then enforce NOT NULL (expired rows keep a synthetic value).
UPDATE invitations SET session_id = gen_random_uuid() WHERE session_id IS NULL;
ALTER TABLE invitations ALTER COLUMN session_id SET NOT NULL;

-- Ensure exactly one unique index on (account_id, title_id) exists, whichever
-- revision created the table. Deduplicate first, deterministically.
DO $$
DECLARE
    have boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class tbl ON tbl.oid = i.indrelid
        JOIN pg_namespace n ON n.oid = tbl.relnamespace
        WHERE tbl.relname = 'adapter_sessions'
          AND n.nspname = current_schema()
          AND i.indisunique
          AND (SELECT array_agg(a.attname ORDER BY a.attnum)
               FROM pg_attribute a
               WHERE a.attrelid = tbl.oid
                 AND a.attnum = ANY(i.indkey::smallint[]) AND a.attnum > 0)
              = ARRAY['account_id'::name, 'title_id'::name]
    ) INTO have;
    IF NOT COALESCE(have, false) THEN
        DELETE FROM adapter_sessions a USING adapter_sessions b
        WHERE a.account_id = b.account_id
          AND a.title_id = b.title_id
          AND a.session_id > b.session_id;
        ALTER TABLE adapter_sessions
            ADD CONSTRAINT adapter_sessions_account_title_uniq
            UNIQUE (account_id, title_id);
    END IF;
END $$;
