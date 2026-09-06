// Package web serves the OpenPak account web/API surface (registration,
// verification, sign-in, recovery, session management).
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"openpak/account/internal/identity"
)

// MailSender is the outbound email transport (implemented by mailer.Mailer).
type MailSender interface {
	Send(to, subject, body string) error
}

type Server struct {
	identity *identity.Service
	mailer   MailSender // nil = development mode: tokens returned in responses
	baseURL  string
	mux      *http.ServeMux
}

func New(ident *identity.Service, mailer MailSender, baseURL string) *Server {
	if baseURL == "" {
		baseURL = "http://localhost:8000"
	}
	s := &Server{identity: ident, mailer: mailer, baseURL: baseURL, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/register", s.handleRegister)
	s.mux.HandleFunc("GET /v1/verify", s.handleVerify)
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("POST /v1/logout", s.auth(s.handleLogout))
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.handleRevokeSession))
	s.mux.HandleFunc("POST /v1/forgot-password", s.handleForgotPassword)
	s.mux.HandleFunc("POST /v1/reset-password", s.handleResetPassword)
	s.mux.HandleFunc("GET /v1/reset-password", s.handleResetPasswordPage)
	s.mux.HandleFunc("POST /v1/reset-password/confirm", s.handleResetPasswordForm)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleHealth)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// sessionCookieName keeps web sessions in a Secure, HttpOnly, SameSite=Lax cookie.
const sessionCookieName = "openpak_session"

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *identity.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		acct, err := s.identity.SessionAccount(r.Context(), c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		next(w, r, acct)
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	acct, verifyToken, err := s.identity.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrEmailTaken):
			// Enumeration resistance: acknowledge without confirming.
			writeJSON(w, http.StatusOK, map[string]string{
				"message": "if the address is registerable, a verification email has been sent",
			})
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if s.mailer == nil {
		// Development mode (no SMTP configured): surface the token so the
		// flow is completable locally. Never happens in production wiring.
		writeJSON(w, http.StatusCreated, map[string]any{"account_id": acct.ID, "verification_token": verifyToken})
		return
	}
	if err := s.sendVerificationEmail(acct.Email, acct.DisplayName, verifyToken); err != nil {
		// Registration succeeded but delivery failed: report so the caller
		// can retry/resend rather than silently never verifying.
		writeError(w, http.StatusInternalServerError, "account created but the verification email could not be sent")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"account_id": acct.ID})
}

func (s *Server) sendVerificationEmail(to, displayName, token string) error {
	link := s.baseURL + "/v1/verify?token=" + token
	body := "Hi " + displayName + ",\n\nConfirm your OpenPak account email address:\n" + link +
		"\n\nIf you did not create this account, ignore this message."
	return s.mailer.Send(to, "Confirm your OpenPak email", body)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "token required")
		return
	}
	if err := s.identity.VerifyEmail(r.Context(), token); err != nil {
		writeError(w, http.StatusBadRequest, "verification link is invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "email verified"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, _, err := s.identity.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "signed in"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	c, err := r.Cookie(sessionCookieName)
	if err == nil && c.Value != "" {
		// Revoke in storage BEFORE reporting success (review P1 #2).
		if err := s.identity.RevokeByToken(r.Context(), c.Value); err != nil {
			writeError(w, http.StatusInternalServerError, "could not sign out; session still active")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]string{"message": "signed out"})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	sessions, err := s.identity.ListSessions(r.Context(), acct.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	id := r.PathValue("id")
	if err := s.identity.RevokeSession(r.Context(), acct.ID, id); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "session revoked"})
}

func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, err := s.identity.RequestPasswordReset(r.Context(), req.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Same response whether or not the account exists (enumeration
	// resistance). The reset token is NEVER returned in a response
	// (review P0 #1); unauthenticated callers only ever receive this
	// fixed message. Delivery goes to the registered address, when one
	// exists, via the mailer.
	if token != "" {
		if err := s.sendPasswordResetEmail(req.Email, token); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "if the address is registered, a reset email has been sent"})
}

func (s *Server) sendPasswordResetEmail(to, token string) error {
	link := s.baseURL + "/v1/reset-password?token=" + token
	body := "A password reset was requested for your OpenPak account.\n\n" +
		"Open this link to choose a new password:\n" + link +
		"\n\nThe link expires in 24 hours and can be used once.\n" +
		"If you did not request this, ignore this message; your password is unchanged."
	return s.mailer.Send(to, "Reset your OpenPak password", body)
}

// handleResetPasswordPage renders the token-consuming reset form (FR-5
// minimal recovery UI; the API JSON endpoint remains for API clients).
func (s *Server) handleResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "token required")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Reset password</title></head>
<body><form method="post" action="/v1/reset-password/confirm">
<h1>Choose a new password</h1>
<input type="hidden" name="token" value="` + htmlEscape(token) + `">
<label>New password <input type="password" name="password" minlength="8" required></label>
<button type="submit">Reset password</button>
</form></body></html>`))
}

// handleResetPasswordForm applies the reset from the HTML form and shows
// the outcome as a page (never echoes the token).
func (s *Server) handleResetPasswordForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	if err := s.identity.ResetPassword(r.Context(), r.PostFormValue("token"), r.PostFormValue("password")); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><body><p>This reset link is invalid or expired. Request a new one.</p></body></html>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><body><p>Password updated. You can now sign in.</p></body></html>`))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.identity.ResetPassword(r.Context(), req.Token, req.Password); err != nil {
		writeError(w, http.StatusBadRequest, "reset link is invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "password updated; all sessions revoked"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "account"})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": strings.ToLower(msg[:1]) + msg[1:]})
}
