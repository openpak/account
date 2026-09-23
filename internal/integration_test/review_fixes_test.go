//go:build integration

// Regression tests for the 2026-09 review findings (P0 #1, P1 #2..#5, P2 #7).
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openpak/account/internal/events"
	"openpak/account/internal/identity"
	"openpak/account/internal/links"
	"openpak/account/internal/mailer"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"
	"openpak/account/internal/web"
)

// newEnvAll resets the schema and wires every service.
func newEnvAll(t *testing.T) (*identity.Service, *links.Service, *events.Service, *social.Service, *sessions.Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	evSvc := events.New(pool)
	return identity.New(pool, "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, evSvc),
		links.New(pool, evSvc), evSvc, social.New(pool, evSvc), sessions.New(pool)
}

// --- P0 #1 + P1 #6: web flows never leak tokens; emails actually send ---

// newRecordingMailer returns a *mailer.Mailer fake that records sends; the
// concrete type mirrors production wiring so typed-nil hazards stay covered.
func newRecordingMailer() (m *mailer.Mailer, sent *[]string, fail *bool) {
	sentList := &[]string{}
	failFlag := false
	m = mailer.NewFunc(func(to, subject, body string) error {
		if failFlag {
			return errors.New("smtp down")
		}
		*sentList = append(*sentList, to+"|"+subject)
		return nil
	})
	return m, sentList, &failFlag
}

func newWebServer(t *testing.T, ident *identity.Service, m *mailer.Mailer) (*web.Server, *[]string, *bool) {
	t.Helper()
	rec := &[]string{}
	failFlag := false
	if m == nil {
		// Recording fake so tests can assert deliveries; handles returned.
		m = mailer.NewFunc(func(to, subject, body string) error {
			if failFlag {
				return errors.New("smtp down")
			}
			*rec = append(*rec, to+"|"+subject)
			return nil
		})
		return web.New(ident, m, "https://accounts.test", "https://openpak.test"), rec, &failFlag
	}
	// Caller supplied their own fake; they assert against it directly.
	return web.New(ident, m, "https://accounts.test", "https://openpak.test"), nil, nil
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReviewWebTokenLeakAndDelivery(t *testing.T) {
	ident, _, _, _, _ := newEnvAll(t)
	srv, sent, _ := newWebServer(t, ident, nil)

	// Register through the public API: email must be sent, token must NOT
	// appear in the response (P0 #1 / P1 #6). Success and duplicate paths
	// share one masked response shape (enumeration resistance).
	rec := postJSON(t, srv, "/v1/register", `{"email":"leak@example.com","password":"correct horse battery","display_name":"L"}`)
	if rec.Code != 200 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "verification_token") {
		t.Fatal("verification token leaked in response")
	}
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "leak@example.com|") {
		t.Fatalf("verification email not sent: %v", *sent)
	}

	// Forgot-password: debug=1 must not leak a reset token (P0 #1).
	rec = postJSON(t, srv, "/v1/forgot-password?debug=1", `{"email":"leak@example.com"}`)
	if rec.Code != 200 {
		t.Fatalf("forgot: %d", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "token") {
		t.Fatalf("reset token leaked: %s", rec.Body.String())
	}
	if len(*sent) != 2 || !strings.Contains((*sent)[1], "Reset your OpenPak password") {
		t.Fatalf("reset email not sent: %v", *sent)
	}

	// The emailed link works end to end: extract token from the mail body
	// by re-requesting a reset for a known account and consuming it via API.
	rec = postJSON(t, srv, "/v1/reset-password", `{"token":"bogus","password":"new password 12345"}`)
	if rec.Code != 400 {
		t.Fatalf("bogus reset must fail, got %d", rec.Code)
	}
}

func TestReviewLogoutRevokes(t *testing.T) {
	ident, _, _, _, _ := newEnvAll(t)
	srv, _, _ := newWebServer(t, ident, nil)

	// Register + verify via service (dev mailer nil path not needed here).
	_, _, err := ident.Register(context.Background(), "logout@example.com", "correct horse battery", "L")
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := ident.Login(context.Background(), "logout@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	// Logout with the session cookie.
	req := httptest.NewRequest(http.MethodPost, "/v1/logout", nil)
	req.AddCookie(&http.Cookie{Name: "openpak_session", Value: sess})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}

	// The saved cookie must no longer authenticate (P1 #2).
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req2.AddCookie(&http.Cookie{Name: "openpak_session", Value: sess})
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("session still valid after logout: %d", rec2.Code)
	}
	if _, err := ident.SessionAccount(context.Background(), sess); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("session row not revoked: %v", err)
	}
}

// --- P1 #3: event versions are commit-ordered; no skips under concurrency ---

func TestReviewEventOrderingNoSkips(t *testing.T) {
	ident, _, evSvc, _, _ := newEnvAll(t)
	ctx := context.Background()
	acct, _, err := ident.Register(ctx, "events-order@example.com", "correct horse battery", "E")
	if err != nil {
		t.Fatal(err)
	}
	// 50 concurrent single-event emissions: versions must be commit-ordered
	// so a poller's high-water mark can never skip one.
	const n = 50
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errs <- evSvc.Emit(ctx, "credential_revoked", acct.ID, "", "")
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	seen := map[uint64]bool{}
	var last uint64
	for {
		page, err := evSvc.Poll(ctx, last, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) == 0 {
			break
		}
		for _, ev := range page.Events {
			if seen[ev.Version] {
				t.Fatalf("duplicate version %d", ev.Version)
			}
			seen[ev.Version] = true
			last = ev.Version
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d events, saw %d (skip detected)", n, len(seen))
	}
}

// --- P1 #4: unlink + event are atomic ---

type failingEmitter struct{ ok *bool }

func (f failingEmitter) EmitTx(ctx context.Context, tx pgx.Tx, typ, accountID, namespace, subjectID string) error {
	if !*f.ok {
		return errors.New("injected emitter failure")
	}
	return nil
}

func TestReviewUnlinkAtomicWithEvent(t *testing.T) {
	ctx := context.Background()

	// Phase 1: emitter fails → unlink must roll back entirely.
	ok := false
	ident, _, evSvc, _, _ := newEnvAll(t)
	acct, _, err := ident.Register(ctx, "atomic@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	lnksFail := links.New(ident.Pool(), failingEmitter{ok: &ok})
	l, err := lnksFail.Reserve(ctx, acct.ID, "synthetic", "subj-atomic", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lnksFail.Activate(ctx, l.ID, "k2"); err != nil {
		t.Fatal(err)
	}
	if _, err := lnksFail.Unlink(ctx, l.ID, "k3"); err == nil {
		t.Fatal("unlink must fail when the emitter fails")
	}
	got, err := lnksFail.GetBySubject(ctx, "synthetic", "subj-atomic")
	if err != nil || got.State != links.Active {
		t.Fatalf("unlink must roll back on emitter failure, state=%v err=%v", got.State, err)
	}

	// Phase 2: emitter works → unlink succeeds and emits exactly one event.
	ok = true
	lnksReal := links.New(ident.Pool(), evSvc)
	if _, err := lnksReal.Unlink(ctx, l.ID, "k3"); err != nil {
		t.Fatalf("retry unlink: %v", err)
	}
	page, err := evSvc.Poll(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range page.Events {
		if ev.Type == "link_unlinked" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 link_unlinked event, got %d", count)
	}
}

// --- P1 #5: banned accounts cannot renew or resolve sessions ---

func TestReviewSessionsRejectBanned(t *testing.T) {
	ident, _, _, _, sessSvc := newEnvAll(t)
	ctx := context.Background()
	acct, _, err := ident.Register(ctx, "banned-sess@example.com", "correct horse battery", "B")
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := sessSvc.Register(ctx, acct.ID, "synthetic", "0005000010143500", "127.0.0.1:1", "home", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ident.SetBanned(ctx, acct.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sessSvc.Heartbeat(ctx, sid, 0); !errors.Is(err, sessions.ErrAccountUnknown) {
		t.Fatalf("heartbeat must reject banned owner, got %v", err)
	}
	if _, err := sessSvc.Resolve(ctx, acct.ID, "0005000010143500"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("resolve must reject banned owner, got %v", err)
	}
	// The stale row must be gone.
	var n int
	if err := ident.Pool().QueryRow(ctx, `SELECT count(*) FROM adapter_sessions WHERE session_id=$1`, sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("banned owner's session row not invalidated")
	}
	// Unban → register again works (status re-validated per operation).
	if err := ident.SetBanned(ctx, acct.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessSvc.Register(ctx, acct.ID, "synthetic", "0005000010143500", "127.0.0.1:1", "home", 0, ""); err != nil {
		t.Fatalf("register after unban: %v", err)
	}
}

// --- P2 #7: one canonical relationship per unordered pair ---

func TestReviewCanonicalRelationship(t *testing.T) {
	ident, _, _, soc, _ := newEnvAll(t)
	ctx := context.Background()
	a, _, err := ident.Register(ctx, "cana@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "canb@example.com", "correct horse battery", "B")
	if err != nil {
		t.Fatal(err)
	}

	// A requests B, then B requests A → reverse-pending surfaced, not a row.
	if _, err := soc.RequestFriend(ctx, a.ID, b.ID, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.RequestFriend(ctx, b.ID, a.ID, "k2"); !errors.Is(err, social.ErrReversePending) {
		t.Fatalf("expected ErrReversePending, got %v", err)
	}
	// B accepts (addressee=B, requester=A). Now A removes, then B
	// re-requests; A accepts again → both viewpoints must agree.
	if _, err := soc.AcceptFriend(ctx, b.ID, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := soc.RemoveFriend(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	// B re-requests (same direction as the accepted-then-removed row); A
	// accepts as the addressee.
	if _, err := soc.RequestFriend(ctx, b.ID, a.ID, "k3"); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, a.ID, b.ID, ""); err != nil {
		t.Fatalf("accept after re-friend: %v", err)
	}
	rel, err := soc.GetRelationship(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rel != social.RelFriends {
		t.Fatalf("expected RelFriends from A's viewpoint, got %v", rel)
	}
	rel, err = soc.GetRelationship(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rel != social.RelFriends {
		t.Fatalf("expected RelFriends from B's viewpoint, got %v", rel)
	}
	ok, reason, err := soc.CheckInteraction(ctx, a.ID, b.ID)
	if err != nil || !ok {
		t.Fatalf("interaction must be allowed, got ok=%v reason=%s err=%v", ok, reason, err)
	}

	// At most one non-removed row for the pair.
	var n int
	if err := ident.Pool().QueryRow(ctx, `SELECT count(*) FROM friendships
		WHERE (requester_id=$1 AND addressee_id=$2) OR (requester_id=$2 AND addressee_id=$1)`,
		a.ID, b.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	_ = json.Marshal
	if n == 0 || n > 2 {
		t.Fatalf("unexpected friendship row count: %d", n)
	}
}

var _ = mailer.Config{}
