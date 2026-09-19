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
