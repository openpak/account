-- Review fix (P1 #3, #4): commit-ordered event versions + a partial unique
-- index enforcing one active relationship per unordered account pair.
CREATE TABLE invalidation_event_counter (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    n  BIGINT NOT NULL DEFAULT 0
);
INSERT INTO invalidation_event_counter (id, n) VALUES (1, 0);

-- Backfill so new versions continue after existing rows.
SELECT setval('invalidation_events_version_seq', GREATEST((SELECT COALESCE(MAX(version), 0) FROM invalidation_events), 1));
UPDATE invalidation_event_counter SET n = (SELECT COALESCE(MAX(version), 0) FROM invalidation_events);

-- Social: at most one non-removed relationship per unordered pair.
CREATE UNIQUE INDEX friendships_unordered_active
    ON friendships (LEAST(requester_id::text, addressee_id::text),
                    GREATEST(requester_id::text, addressee_id::text))
    WHERE state <> 'removed';
