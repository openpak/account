-- Moderation (website contract requests CR-5 and CR-6).
--
-- moderation_actions is the core's own audit of what a moderator did to an
-- account: who (actor, as the calling surface names them), why (reason), and
-- when. It is written in the same transaction as the status change, so an
-- account is never banned without a record of it.
CREATE TABLE moderation_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES accounts(id),
    action      TEXT NOT NULL CHECK (action IN ('ban','unban')),
    reason      TEXT NOT NULL,
    actor       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_moderation_actions_account ON moderation_actions(account_id, created_at DESC);

-- Reports: the moderation queue's read model. A report is filed by an account
-- against an account; adapters and the website file them, moderators resolve
-- them. kind is a short slug chosen by the filing surface.
CREATE TABLE reports (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    reporter_id         UUID NOT NULL REFERENCES accounts(id),
    subject_account_id  UUID NOT NULL REFERENCES accounts(id),
    kind                TEXT NOT NULL,
    detail              TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    resolution          TEXT,
    resolved_by         TEXT,
    resolved_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_reports_status ON reports(status, created_at);
-- One open report per reporter, subject and kind: filing again while it is
-- open answers with the existing report instead of growing the queue.
CREATE UNIQUE INDEX idx_reports_open_once ON reports(reporter_id, subject_account_id, kind)
    WHERE status = 'open';
