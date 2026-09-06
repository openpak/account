-- durable invalidation events (PRD §7): monotonic version, dedup by event_id
CREATE SEQUENCE invalidation_events_version_seq;

CREATE TABLE invalidation_events (
    version     BIGINT PRIMARY KEY DEFAULT nextval('invalidation_events_version_seq'),
    event_id    UUID NOT NULL UNIQUE,
    type        TEXT NOT NULL CHECK (type IN
                    ('account_banned','account_unbanned','link_unlinked',
                     'account_delete_started','account_deleted','credential_revoked')),
    account_id  UUID NOT NULL,
    namespace   TEXT,
    subject_id  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invalidation_events_account ON invalidation_events(account_id);
