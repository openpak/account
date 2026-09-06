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

-- Social: deduplicate before enforcing the canonical-pair constraint.
-- The previous RequestFriend checked only reverse PENDING requests, so data
-- written by it can hold several non-removed rows for one unordered pair
-- (e.g. A->B pending alongside B->A accepted). Those cannot satisfy the
-- unique index, so migration would fail. Keep the NEWEST non-removed row
-- per unordered pair and mark older ones removed (migration is
-- unreleased-but-committed; edited in place before any deployment).
UPDATE friendships f SET state = 'removed', updated_at = now()
WHERE f.state <> 'removed'
  AND EXISTS (
    SELECT 1 FROM friendships g
    WHERE g.state <> 'removed'
      AND LEAST(g.requester_id::text, g.addressee_id::text) = LEAST(f.requester_id::text, f.addressee_id::text)
      AND GREATEST(g.requester_id::text, g.addressee_id::text) = GREATEST(f.requester_id::text, f.addressee_id::text)
      AND (g.updated_at, g.created_at, g.id) > (f.updated_at, f.created_at, f.id)
  );

-- One non-removed relationship per unordered pair (review P2 #7).
CREATE UNIQUE INDEX friendships_unordered_active
    ON friendships (LEAST(requester_id::text, addressee_id::text),
                    GREATEST(requester_id::text, addressee_id::text))
    WHERE state <> 'removed';
