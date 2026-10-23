//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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

// TestDeletionGraceRestoreAndErase walks the automatic lifecycle: closing
// sets an erasure date one grace period away, the owner's password restores
// (and only the right one tells them the account is closed), an admin restore
// brings a banned account back banned, a due account cannot be restored, and
// erasure takes friendships, blocks, invitations and console links both ways,
// after which sign-in fails exactly as for an address nobody has.
func TestDeletionGraceRestoreAndErase(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	pool := ident.Pool()
	ident.SetDeletionGrace(48 * time.Hour)

	newAcct := func(email, handle string) string {
		t.Helper()
		a, tok, err := ident.Register(ctx, email, "correct horse battery 1", handle)
		if err != nil {
			t.Fatalf("register %s: %v", email, err)
		}
		if err := ident.VerifyEmail(ctx, tok); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	gone := newAcct("leaver@example.com", "Leaver")
	friend := newAcct("friend@example.com", "Friend")

	// Close: erase_after one grace period out, not yet due.
	if err := ident.RequestDeletion(ctx, gone); err != nil {
		t.Fatalf("request: %v", err)
	}
	var eraseAfter time.Time
	var before string
	if err := pool.QueryRow(ctx, `SELECT erase_after, status_before_deletion FROM accounts WHERE id=$1`, gone).Scan(&eraseAfter, &before); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(eraseAfter); d < 47*time.Hour || d > 49*time.Hour || before != "active" {
		t.Fatalf("erase_after in %v, before=%q; want ~48h, active", d, before)
	}
	if due, _ := ident.DueDeletions(ctx, 10); len(due) != 0 {
		t.Fatalf("due before the grace ends: %v", due)
	}

	// Sign-in: a wrong password learns nothing; the right one learns the date.
	if _, _, err := ident.Login(ctx, "leaver@example.com", "wrong password here"); !errors.Is(err, identity.ErrBadCredentials) {
		t.Fatalf("wrong password on closed account = %v, want ErrBadCredentials", err)
	}
	_, _, err := ident.Login(ctx, "leaver@example.com", "correct horse battery 1")
	var pending *identity.PendingDeletionError
	if !errors.As(err, &pending) || !errors.Is(err, identity.ErrAccountDeleted) || pending.EraseAfter.IsZero() {
		t.Fatalf("right password on closed account = %v, want PendingDeletionError", err)
	}
	if err := ident.RestoreByPassword(ctx, "leaver@example.com", "wrong password here"); !errors.Is(err, identity.ErrBadCredentials) {
		t.Fatalf("restore with wrong password = %v", err)
	}
	if err := ident.RestoreByPassword(ctx, "leaver@example.com", "correct horse battery 1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, _, err := ident.Login(ctx, "leaver@example.com", "correct horse battery 1"); err != nil {
		t.Fatalf("login after restore: %v", err)
	}
	if err := ident.RestoreByPassword(ctx, "leaver@example.com", "correct horse battery 1"); err != nil {
		t.Fatalf("restore of an open account must be a no-op: %v", err)
	}

	// Admin restore keeps a ban.
	if _, err := pool.Exec(ctx, `UPDATE accounts SET status='banned' WHERE id=$1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := ident.RequestDeletion(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if err := ident.RestoreDeletion(ctx, gone); err != nil {
		t.Fatalf("admin restore: %v", err)
	}
	var st string
	_ = pool.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1`, gone).Scan(&st)
	if st != "banned" {
		t.Fatalf("restored banned account is %q", st)
	}
	if err := ident.RestoreDeletion(ctx, gone); !errors.Is(err, identity.ErrDeletionNotPending) {
		t.Fatalf("restore of a non-closed account = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET status='active' WHERE id=$1`, gone); err != nil {
		t.Fatal(err)
	}

	// Social graph and console links, both directions.
	for _, q := range []string{
		`INSERT INTO friendships (id, requester_id, addressee_id, state) VALUES (gen_random_uuid(), $2, $1, 'accepted')`,
		`INSERT INTO account_blocks (blocker_id, blocked_id) VALUES ($1, $2)`,
		`INSERT INTO account_blocks (blocker_id, blocked_id) VALUES ($2, $1)`,
		`INSERT INTO invitations (id, from_id, to_id, namespace, title_id, session_id, payload, nonce, expires_at)
			VALUES (gen_random_uuid(), $1, $2, 'switch', '0100aaaa', gen_random_uuid(), '\x00', gen_random_uuid(), now() + interval '1 hour')`,
		`INSERT INTO invitations (id, from_id, to_id, namespace, title_id, session_id, payload, nonce, expires_at)
			VALUES (gen_random_uuid(), $2, $1, 'switch', '0100aaaa', gen_random_uuid(), '\x00', gen_random_uuid(), now() + interval '1 hour')`,
		`INSERT INTO adapter_links (id, account_id, namespace, subject_id, state)
			SELECT gen_random_uuid(), $1, 'switch', 'nsa-console-1', 'active' WHERE $2::uuid IS NOT NULL`,
	} {
		if _, err := pool.Exec(ctx, q, gone, friend); err != nil {
			t.Fatalf("seed %q: %v", q[:50], err)
		}
	}

	// Close again, then let the grace period run out.
	if err := ident.RequestDeletion(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET erase_after = now() - interval '1 minute' WHERE id=$1`, gone); err != nil {
		t.Fatal(err)
	}
	due, err := ident.DueDeletions(ctx, 10)
	if err != nil || len(due) != 1 || due[0] != gone {
		t.Fatalf("due = %v %v, want [%s]", due, err, gone)
	}
	if err := ident.RestoreDeletion(ctx, gone); !errors.Is(err, identity.ErrGraceOver) {
		t.Fatalf("restore after the grace = %v, want ErrGraceOver", err)
	}
	if err := ident.RestoreByPassword(ctx, "leaver@example.com", "correct horse battery 1"); !errors.Is(err, identity.ErrGraceOver) {
		t.Fatalf("owner restore after the grace = %v, want ErrGraceOver", err)
	}
	if err := ident.FinalizeDeletion(ctx, gone); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM friendships WHERE requester_id=$1 OR addressee_id=$1`: 0,
		`SELECT count(*) FROM account_blocks WHERE blocker_id=$1 OR blocked_id=$1`:  0,
		`SELECT count(*) FROM invitations WHERE from_id=$1 OR to_id=$1`:             0,
		`SELECT count(*) FROM adapter_links WHERE account_id=$1`:                    0,
	} {
		var n int
		if err := pool.QueryRow(ctx, q, gone).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", q, n, err, want)
		}
	}
	if due, _ := ident.DueDeletions(ctx, 10); len(due) != 0 {
		t.Fatalf("erased account still due: %v", due)
	}
	// After erasure the address is nobody's.
	if _, _, err := ident.Login(ctx, "leaver@example.com", "correct horse battery 1"); !errors.Is(err, identity.ErrBadCredentials) {
		t.Fatalf("login after erasure = %v, want ErrBadCredentials", err)
	}
	// The console can be linked to a new account.
	if _, err := pool.Exec(ctx, `INSERT INTO adapter_links (id, account_id, namespace, subject_id, state) VALUES (gen_random_uuid(), $1, 'switch', 'nsa-console-1', 'active')`, friend); err != nil {
		t.Fatalf("relink the console: %v", err)
	}
}

// The sign-in surface: a closed account answers 409 with its erasure date to
// the right password only, and /v1/login/restore reopens it and signs in.
func TestLoginOffersRestore(t *testing.T) {
	ident, h, _ := webEnv(t)
	id, _ := registerAndLogin(t, ident, "comeback@example.com", "correct horse battery")
	if err := ident.RequestDeletion(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if rec, _ := call(t, h, "POST", "/v1/login", "", `{"email":"comeback@example.com","password":"wrong wrong wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", rec.Code)
	}
	rec, body := call(t, h, "POST", "/v1/login", "", `{"email":"comeback@example.com","password":"correct horse battery"}`)
	if rec.Code != http.StatusConflict || body["deletion_pending"] != true || body["erase_after"] == "" {
		t.Fatalf("closed account login = %d %v, want 409 with erase_after", rec.Code, body)
	}
	if rec, _ := call(t, h, "POST", "/v1/login/restore", "", `{"email":"comeback@example.com","password":"wrong wrong wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("restore with wrong password = %d", rec.Code)
	}
	rec, body = call(t, h, "POST", "/v1/login/restore", "", `{"email":"comeback@example.com","password":"correct horse battery"}`)
	if rec.Code != 200 || body["account_id"] != id || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("restore = %d %v", rec.Code, body)
	}
}
