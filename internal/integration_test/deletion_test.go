//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"openpak/account/internal/identity"
)

// TestFinalizeDeletionDestroysCredentials walks both stages: request disables
// and revokes, finalize destroys credentials and sweeps every live-state
// table, the handle stays held, and refinalizing is a no-op.
func TestFinalizeDeletionDestroysCredentials(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()

	acct, token, err := ident.Register(ctx, "gone@example.com", "correct horse battery 1", "Gone")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := ident.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := ident.ClaimHandle(ctx, acct.ID, "goneplayer"); err != nil {
		t.Fatalf("claim handle: %v", err)
	}
	sess, _, err := ident.Login(ctx, "gone@example.com", "correct horse battery 1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Satellite rows the sweep must take with it (values are dummies; the
	// point is the DELETE statements, not enrolment ceremonies).
	pool := ident.Pool()
	for _, q := range []string{
		`INSERT INTO account_two_factor (account_id, secret_enc) VALUES ('` + acct.ID + `', '\x00')`,
		`INSERT INTO two_factor_recovery_codes (account_id, code_hash) VALUES ('` + acct.ID + `', '\x01')`,
		`INSERT INTO login_challenges (id, account_id, token_hash, expires_at) VALUES ('22222222-2222-2222-2222-222222222222', '` + acct.ID + `', '\x02', now() + interval '10 minutes')`,
		`INSERT INTO account_avatars (account_id, png_256, png_512, version) VALUES ('` + acct.ID + `', '\x03', '\x04', 1)`,
		`INSERT INTO adapter_sessions (session_id, account_id, namespace, title_id, endpoint_ref, expires_at) VALUES ('33333333-3333-3333-3333-333333333333', '` + acct.ID + `', 'switch', '0100aaaa', 'ref', now() + interval '1 hour')`,
		`INSERT INTO play_sessions (account_id, namespace, title_id, client, os, started_at, ended_at) VALUES ('` + acct.ID + `', 'switch', '0100aaaa', 'c', '', now(), now())`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q[:40], err)
		}
	}

	// Finalize without request is not a deletion.
	if err := ident.FinalizeDeletion(ctx, acct.ID); !errors.Is(err, identity.ErrDeletionNotPending) {
		t.Fatalf("finalize-without-request = %v, want ErrDeletionNotPending", err)
	}

	if err := ident.RequestDeletion(ctx, acct.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, _, err := ident.Login(ctx, "gone@example.com", "correct horse battery 1"); !errors.Is(err, identity.ErrAccountDeleted) {
		t.Fatalf("login after request = %v, want ErrAccountDeleted", err)
	}
	if _, err := ident.SessionAccount(ctx, sess); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("session after request = %v, want ErrNotFound", err)
	}

	if err := ident.FinalizeDeletion(ctx, acct.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := ident.FinalizeDeletion(ctx, acct.ID); err != nil {
		t.Fatalf("refinalize: %v, want nil", err)
	}

	for _, table := range []string{"sessions", "lifecycle_tokens", "adapter_credentials", "adapter_sessions",
		"play_sessions", "account_two_factor", "two_factor_recovery_codes", "login_challenges", "account_avatars"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE account_id=$1`, acct.ID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s keeps %d rows after finalize", table, n)
		}
	}
	var status, email, hash, display string
	if err := pool.QueryRow(ctx, `SELECT status, email, password_hash, display_name FROM accounts WHERE id=$1`, acct.ID).
		Scan(&status, &email, &hash, &display); err != nil {
		t.Fatalf("accounts row: %v", err)
	}
	if status != "deleted" || !strings.HasSuffix(email, "@deleted.invalid") || hash != "" || display != "" {
		t.Errorf("accounts row not anonymized: %q %q hash=%q display=%q", status, email, hash, display)
	}
	// The handle stays held for 90 days and resolves to nobody.
	var release *string
	_ = pool.QueryRow(ctx, `SELECT release_at::text FROM account_handle_claims WHERE account_id=$1`, acct.ID).Scan(&release)
	if release == nil {
		t.Error("handle hold has no release set")
	}
	if _, err := ident.ResolveHandle(ctx, "goneplayer"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("held handle resolves: %v", err)
	}
}
