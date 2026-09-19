//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"openpak/account/internal/identity"
	"openpak/account/internal/mailer"
	"openpak/account/internal/web"
)

type sentMail struct{ To, Subject, Body string }

// mailbox is a mailer fake that keeps whole messages, so a test can follow
// the link a message carries.
type mailbox struct {
	mu   sync.Mutex
	sent []sentMail
}

func (m *mailbox) mailer() *mailer.Mailer {
	return mailer.NewFunc(func(to, subject, body string) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.sent = append(m.sent, sentMail{to, subject, body})
		return nil
	})
}

func (m *mailbox) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

var tokenRe = regexp.MustCompile(`token=([0-9a-f]{64})`)

// lastToken is the token in the newest message to addr.
func (m *mailbox) lastToken(t *testing.T, addr string) string {
	t.Helper()
	msgs := m.all()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].To == addr {
			if g := tokenRe.FindStringSubmatch(msgs[i].Body); g != nil {
				return g[1]
			}
		}
	}
	t.Fatalf("no token mailed to %s: %+v", addr, msgs)
	return ""
}

// webEnv is a web server with a whole-message mailbox and one signed-in account.
func webEnv(t *testing.T) (*identity.Service, http.Handler, *mailbox) {
	t.Helper()
	ident, _, _, _, _ := newEnvAll(t)
	box := &mailbox{}
	return ident, web.New(ident, box.mailer(), "https://accounts.test", "https://openpak.test"), box
}

// call sends a JSON request; session, when set, rides as the cookie.
func call(t *testing.T, h http.Handler, method, path, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "openpak_session", Value: session})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func registerAndLogin(t *testing.T, ident *identity.Service, email, password string) (string, string) {
	t.Helper()
	acct, _, err := ident.Register(context.Background(), email, password, "Web User")
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := ident.Login(context.Background(), email, password)
	if err != nil {
		t.Fatal(err)
	}
	return acct.ID, sess
}

func TestVerifyResend(t *testing.T) {
	ident, h, box := webEnv(t)
	_, sess := registerAndLogin(t, ident, "resend@example.com", "correct horse battery")

	if rec, _ := call(t, h, "POST", "/v1/verify/resend", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	rec, body := call(t, h, "POST", "/v1/verify/resend", sess, "")
	if rec.Code != 200 || body["message"] == "" {
		t.Fatalf("resend: %d %v", rec.Code, body)
	}
	if n := len(box.all()); n != 1 {
		t.Fatalf("want one mail, got %d", n)
	}
	first := box.lastToken(t, "resend@example.com")

	// Throttled: same answer, no second mail.
	rec2, body2 := call(t, h, "POST", "/v1/verify/resend", sess, "")
	if rec2.Code != 200 || body2["message"] != body["message"] || len(box.all()) != 1 {
		t.Fatalf("throttle: %d %v, mails %d", rec2.Code, body2, len(box.all()))
	}
	if rec, _ := call(t, h, "GET", "/v1/verify?token="+first, "", ""); rec.Code != 200 {
		t.Fatalf("verify with resent link: %d", rec.Code)
	}
	// Past the throttle window, a verified account still gets no mail and the same answer.
	if _, err := ident.Pool().Exec(context.Background(), `UPDATE lifecycle_tokens SET created_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	rec3, body3 := call(t, h, "POST", "/v1/verify/resend", sess, "")
	if rec3.Code != 200 || body3["message"] != body["message"] || len(box.all()) != 1 {
		t.Fatalf("verified account: %d %v, mails %d", rec3.Code, body3, len(box.all()))
	}
}

func TestEmailChange(t *testing.T) {
	ident, h, box := webEnv(t)
	ctx := context.Background()
	id, sess := registerAndLogin(t, ident, "old@example.com", "correct horse battery")
	registerAndLogin(t, ident, "taken@example.com", "correct horse battery")
	staleReset, err := ident.RequestPasswordReset(ctx, "old@example.com")
	if err != nil || staleReset == "" {
		t.Fatal(err)
	}

	if rec, _ := call(t, h, "POST", "/v1/email/change", "", `{"new_email":"new@example.com","password":"correct horse battery"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"new@example.com","password":"wrong password 1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":" OLD@example.com","password":"correct horse battery"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("same address: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"nope","password":"correct horse battery"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d", rec.Code)
	}

	// Taken address: the same answer as success, and nobody is mailed.
	recTaken, bodyTaken := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"Taken@example.com","password":"correct horse battery"}`)
	if recTaken.Code != 200 || len(box.all()) != 0 {
		t.Fatalf("taken: %d %v mails=%d", recTaken.Code, bodyTaken, len(box.all()))
	}

	rec, body := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"New@Example.com","password":"correct horse battery"}`)
	if rec.Code != 200 || body["message"] != bodyTaken["message"] {
		t.Fatalf("change: %d %v (taken answered %v)", rec.Code, body, bodyTaken)
	}
	mails := box.all()
	if len(mails) != 2 || mails[0].To != "New@Example.com" || mails[1].To != "old@example.com" || tokenRe.MatchString(mails[1].Body) {
		t.Fatalf("mails: %+v", mails)
	}
	if !strings.Contains(mails[0].Body, "https://openpak.test/email/confirm?token=") {
		t.Fatalf("link points at the site: %s", mails[0].Body)
	}
	token := box.lastToken(t, "New@Example.com")

	// Throttled: one request per 10 minutes, same answer.
	if rec, b := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"other@example.com","password":"correct horse battery"}`); rec.Code != 200 || b["message"] != body["message"] || len(box.all()) != 2 {
		t.Fatalf("throttle: %d mails=%d", rec.Code, len(box.all()))
	}

	if rec, _ := call(t, h, "POST", "/v1/email/confirm", "", `{"token":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus token: %d", rec.Code)
	}
	if rec, b := call(t, h, "POST", "/v1/email/confirm", "", `{"token":"`+token+`"}`); rec.Code != 200 {
		t.Fatalf("confirm: %d %v", rec.Code, b)
	}
	if rec, _ := call(t, h, "POST", "/v1/email/confirm", "", `{"token":"`+token+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("token is single-use: %d", rec.Code)
	}

	// The session survives and sees the new, verified address.
	acct, err := ident.SessionAccount(ctx, sess)
	if err != nil || acct.Email != "New@Example.com" || !acct.EmailVerified {
		t.Fatalf("session after change: %+v %v", acct, err)
	}
	if _, _, err := ident.Login(ctx, "old@example.com", "correct horse battery"); err == nil {
		t.Fatal("old address still signs in")
	}
	if _, _, err := ident.Login(ctx, "new@example.com", "correct horse battery"); err != nil {
		t.Fatalf("new address: %v", err)
	}
	if err := ident.ResetPassword(ctx, staleReset, "another password 1"); err == nil {
		t.Fatal("a reset link mailed to the old address must die with it")
	}
	var events int
	if err := ident.Pool().QueryRow(ctx, `SELECT count(*) FROM invalidation_events WHERE type='email_changed' AND account_id=$1`, id).Scan(&events); err != nil || events != 1 {
		t.Fatalf("email_changed events: %d %v", events, err)
	}

	// The core's own link: GET renders a button, the form post applies it.
	if _, err := ident.Pool().Exec(ctx, `UPDATE lifecycle_tokens SET created_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if rec, _ := call(t, h, "POST", "/v1/email/change", sess, `{"new_email":"third@example.com","password":"correct horse battery"}`); rec.Code != 200 {
		t.Fatalf("second change: %d", rec.Code)
	}
	third := box.lastToken(t, "third@example.com")
	page, _ := call(t, h, "GET", "/v1/email/confirm?token="+third, "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), third) {
		t.Fatalf("confirm page: %d", page.Code)
	}
	if a, _ := ident.SessionAccount(ctx, sess); a.Email != "New@Example.com" {
		t.Fatal("GET must not apply the change")
	}
	req := httptest.NewRequest("POST", "/v1/email/confirm", strings.NewReader("token="+third))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form := httptest.NewRecorder()
	h.ServeHTTP(form, req)
	if !strings.Contains(form.Body.String(), "has been changed") {
		t.Fatalf("form confirm: %s", form.Body.String())
	}
	if a, _ := ident.SessionAccount(ctx, sess); a.Email != "third@example.com" {
		t.Fatalf("form post applies it: %+v", a)
	}
}
