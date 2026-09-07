//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"openpak/account/internal/sessions"
	"openpak/account/internal/storage"
)

// TestForwardMigrationFromOldRevision reproduces a database that applied the
// earlier revision of 0004 (no invitation session binding, no adapter_sessions
// uniqueness) with 0005 pending, and verifies migration 0005 forward-fixes it:
// queued orphan invitations expire, duplicate sessions are removed, the unique
// constraint exists, and registration upserts work on the migrated schema.
func TestForwardMigrationFromOldRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	reset := `DROP SCHEMA public CASCADE; CREATE SCHEMA public`
	if _, err := pool.Exec(ctx, reset); err != nil {
		t.Fatal(err)
	}
	// Build the current schema, then regress two tables to the OLD 0004 shape
	// and mark 0005 unapplied — exactly what an existing database looks like.
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	const seedAccounts = `INSERT INTO accounts (id, email, email_normalized, password_hash, display_name)
		VALUES ('11111111-1111-1111-1111-111111111111','old-a@x.test','old-a@x.test','h','A'),
		       ('22222222-2222-2222-2222-222222222222','old-b@x.test','old-b@x.test','h','B')`
	const oldShape = `
DROP TABLE invitations, adapter_sessions CASCADE;
CREATE TABLE adapter_sessions (
    session_id UUID PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id),
    namespace TEXT NOT NULL,
    title_id TEXT NOT NULL,
    endpoint_ref TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX idx_adapter_sessions_account ON adapter_sessions(account_id, title_id);
CREATE TABLE invitations (
    id UUID PRIMARY KEY,
    from_id UUID NOT NULL REFERENCES accounts(id),
    to_id UUID NOT NULL REFERENCES accounts(id),
    namespace TEXT NOT NULL,
    title_id TEXT NOT NULL,
    payload BYTEA NOT NULL,
    nonce UUID NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ);
CREATE INDEX idx_invitations_to ON invitations(to_id, namespace, state);
DELETE FROM schema_migrations WHERE name = '0007_invitation_sessions_forward.sql';`
	const seedOldRows = `
INSERT INTO adapter_sessions (session_id, account_id, namespace, title_id, endpoint_ref, expires_at)
VALUES ('33333333-3333-3333-3333-333333333331','11111111-1111-1111-1111-111111111111','switch','t','e1', now() + interval '1 hour'),
       ('33333333-3333-3333-3333-333333333332','11111111-1111-1111-1111-111111111111','switch','t','e2', now() + interval '1 hour');
INSERT INTO invitations (id, from_id, to_id, namespace, title_id, payload, nonce, expires_at)
VALUES ('44444444-4444-4444-4444-444444444444','11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222','switch','t','x',
        '55555555-5555-5555-5555-555555555555', now() + interval '1 hour');`

	for _, ddl := range []string{seedAccounts, oldShape, seedOldRows} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("old-shape setup: %v", err)
		}
	}

	// Forward migration applies 0005 onto the old shape.
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Queued orphan invitation expired; session_id backfilled NOT NULL.
	var state string
	var sessID []byte
	if err := pool.QueryRow(ctx,
		`SELECT state, session_id FROM invitations WHERE id='44444444-4444-4444-4444-444444444444'`,
	).Scan(&state, &sessID); err != nil || state != "expired" {
		t.Fatalf("expected orphan invitation expired, got %q %v", state, err)
	}
	if len(sessID) == 0 {
		t.Fatal("expected session_id backfilled")
	}

	// Duplicate live sessions deduplicated deterministically.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM adapter_sessions WHERE account_id='11111111-1111-1111-1111-111111111111' AND title_id='t'`,
	).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 session row after dedupe, got %d %v", count, err)
	}

	// Registration upserts on the migrated schema.
	svc := sessions.New(pool)
	id, lease, err := svc.Register(ctx, "11111111-1111-1111-1111-111111111111", "switch", "t", "ep-new", 30*time.Second)
	if err != nil || id == "" || lease <= 0 {
		t.Fatalf("register on migrated schema: %q %v %v", id, lease, err)
	}
}
