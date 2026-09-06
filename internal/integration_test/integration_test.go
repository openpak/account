//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"openpak/account/internal/identity"
	"openpak/account/internal/links"
	"openpak/account/internal/storage"
)

const testDBURL = "postgres://postgres:test@127.0.0.1:54329/account_test?sslmode=disable"

func newEnv(t *testing.T) (*identity.Service, *links.Service) {
	t.Helper()
	if os.Getenv("SKIP_PG") == "1" {
		t.Skip("postgres integration tests disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Reset schema for isolation.
	for _, ddl := range []string{
		`DROP TABLE IF EXISTS sessions, lifecycle_tokens, adapter_links, accounts, schema_migrations, invalidation_events CASCADE; DROP SEQUENCE IF EXISTS invalidation_events_version_seq CASCADE`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("reset: %v", err)
		}
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return identity.New(pool, "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, nil), links.New(pool, nil)
}

func TestRegisterVerifyLogin(t *testing.T) {
	ident, lnks := newEnv(t)
	ctx := context.Background()

	acct, token, err := ident.Register(ctx, "User@Example.com", "correct horse battery", "Tester")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if token == "" {
		t.Fatal("expected verification token")
	}

	// Duplicate registration is rejected (unique constraint) even though the
	// public API masks it: the service itself must know.
	if _, _, err := ident.Register(ctx, "user@example.com", "another password 1", "Tester"); !errors.Is(err, identity.ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}

	// Login before verification is allowed at service level; email gate is
	// policy for console linking, enforced by callers (see FR-2).
	sess, _, err := ident.Login(ctx, "user@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := ident.SessionAccount(ctx, sess); err != nil {
		t.Fatalf("session: %v", err)
	}

	if err := ident.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Token is single-use.
	if err := ident.VerifyEmail(ctx, token); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid on reuse, got %v", err)
	}

	// Wrong password rejected, unknown email indistinguishable.
	if _, _, err := ident.Login(ctx, "user@example.com", "wrong password 123"); !errors.Is(err, identity.ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
	if _, _, err := ident.Login(ctx, "nobody@example.com", "correct horse battery"); !errors.Is(err, identity.ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}

	// Password reset flow: request, consume, sessions revoked.
	reset, err := ident.RequestPasswordReset(ctx, "user@example.com")
	if err != nil || reset == "" {
		t.Fatalf("reset request: %v %v", reset, err)
	}
	if err := ident.ResetPassword(ctx, reset, "new password 12345"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := ident.SessionAccount(ctx, sess); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected old session revoked, got %v", err)
	}
	if _, _, err := ident.Login(ctx, "user@example.com", "new password 12345"); err != nil {
		t.Fatalf("login after reset: %v", err)
	}

	// Password reset for unknown email is a silent no-op.
	silent, err := ident.RequestPasswordReset(ctx, "ghost@example.com")
	if err != nil || silent != "" {
		t.Fatalf("expected silent no-op, got %q %v", silent, err)
	}

	// Generic link lifecycle for a synthetic (non-Nintendo) adapter.
	l, err := lnks.Reserve(ctx, acct.ID, "synthetic", "subject-123", "idem-1")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Conflicting claim by another account.
	other, _, err := ident.Register(ctx, "other@example.com", "correct horse battery", "Other")
	if err != nil {
		t.Fatalf("register other: %v", err)
	}
	if _, err := lnks.Reserve(ctx, other.ID, "synthetic", "subject-123", "idem-2"); !errors.Is(err, links.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Idempotent reserve for same account.
	if l2, err := lnks.Reserve(ctx, acct.ID, "synthetic", "subject-123", "idem-1"); err != nil || l2.ID != l.ID {
		t.Fatalf("idempotent reserve: %v %v", l2, err)
	}
	if st, err := lnks.Activate(ctx, l.ID, "idem-act"); err != nil || st != links.Active {
		t.Fatalf("activate: %v %v", st, err)
	}
	// Activate is idempotent.
	if st, err := lnks.Activate(ctx, l.ID, "idem-act"); err != nil || st != links.Active {
		t.Fatalf("idempotent activate: %v %v", st, err)
	}
	got, err := lnks.GetBySubject(ctx, "synthetic", "subject-123")
	if err != nil || got.AccountID != acct.ID || got.State != links.Active {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A second active link for the same subject must be impossible.
	if _, err := lnks.Reserve(ctx, other.ID, "synthetic", "subject-123", "idem-3"); !errors.Is(err, links.ErrConflict) {
		t.Fatalf("active link must conflict, got %v", err)
	}
	if st, err := lnks.Unlink(ctx, l.ID, "idem-un"); err != nil || st != links.Revoked {
		t.Fatalf("unlink: %v %v", st, err)
	}
	// Unlink idempotent.
	if st, err := lnks.Unlink(ctx, l.ID, "idem-un"); err != nil || st != links.Revoked {
		t.Fatalf("idempotent unlink: %v %v", st, err)
	}
	// Subject can be re-linked by a different account only per policy; here
	// relink after revoke succeeds for the original owner (policy: allow).
	if _, err := lnks.Reserve(ctx, acct.ID, "synthetic", "subject-123", "idem-4"); err != nil {
		t.Fatalf("relink after unlink: %v", err)
	}
}

func TestBannedAccount(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	acct, _, err := ident.Register(ctx, "banned@example.com", "correct horse battery", "Banned")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ident.GetByID(ctx, acct.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	// Directly ban for the test (moderation service arrives in M3).
	_, err = ident.Pool().Exec(ctx, `UPDATE accounts SET status='banned' WHERE id=$1`, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ident.Login(ctx, "banned@example.com", "correct horse battery"); !errors.Is(err, identity.ErrAccountBanned) {
		t.Fatalf("expected ErrAccountBanned, got %v", err)
	}
	// Deleted accounts are invisible to GetByID.
	_, err = ident.Pool().Exec(ctx, `UPDATE accounts SET status='deleted' WHERE id=$1`, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ident.GetByID(ctx, acct.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSessionListing(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	acct, _, err := ident.Register(ctx, "sessions@example.com", "correct horse battery", "S")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := ident.Login(ctx, "sessions@example.com", "correct horse battery"); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := ident.ListSessions(ctx, acct.ID)
	if err != nil || len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %v %v", sessions, err)
	}
	first := fmt.Sprintf("%s", sessions[0]["id"])
	if err := ident.RevokeSession(ctx, acct.ID, first); err != nil {
		t.Fatal(err)
	}
	sessions, _ = ident.ListSessions(ctx, acct.ID)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions after revoke, got %d", len(sessions))
	}
	// Cannot revoke another account's session (IDs are unique, but check).
	if err := ident.RevokeSession(ctx, acct.ID, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
