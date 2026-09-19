// Package web serves the OpenPak account web/API surface (registration,
// verification, sign-in, recovery, session management).
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"openpak/account/internal/emaillink"
	"os"
	"strings"
	"time"

	"openpak/account/internal/identity"
	"openpak/account/internal/mailer"
)

type Server struct {
	identity *identity.Service
	// mailer is concrete so a typed-nil (misconfigured wiring) compares nil
	// and the guards hold; the interface form wrapped nil pointers into
	// non-nil values and panicked (review follow-up P1 #1).
	mailer  *mailer.Mailer // nil = development mode: tokens returned in responses
	baseURL string
	// siteBaseURL: see grpcapi.Options.SiteBaseURL — emailed links point at the site.
	siteBaseURL string
	mux         *http.ServeMux
}

func New(ident *identity.Service, m *mailer.Mailer, baseURL, siteBaseURL string) *Server {
	if baseURL == "" {
		baseURL = "http://localhost:8000"
	}
	s := &Server{identity: ident, mailer: m, baseURL: baseURL, siteBaseURL: siteBaseURL, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/register", s.handleRegister)
	s.mux.HandleFunc("GET /v1/verify", s.handleVerify)
	s.mux.HandleFunc("POST /v1/verify/resend", s.auth(s.handleVerifyResend))
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("POST /v1/logout", s.auth(s.handleLogout))
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.handleRevokeSession))
	s.mux.HandleFunc("POST /v1/change-password", s.auth(s.handleChangePassword))
	s.mux.HandleFunc("POST /v1/profile", s.auth(s.handleUpdateProfile))
	s.mux.HandleFunc("POST /v1/forgot-password", s.handleForgotPassword)
	s.mux.HandleFunc("POST /v1/reset-password", s.handleResetPassword)
	s.mux.HandleFunc("GET /v1/reset-password", s.handleResetPasswordPage)
	s.mux.HandleFunc("POST /v1/reset-password/confirm", s.handleResetPasswordForm)
	// Console linking: the page lives in the Switch adapter, but a phone scanning the QR needs
	// a name with a certificate it trusts — a bare IP gives a warning page, and
	// accounts.nintendo.com resolves only on the console itself. Only /link is forwarded.
	if adapter := os.Getenv("ADAPTER_LINK_URL"); adapter != "" {
		s.mux.Handle("/link", adapterProxy(adapter))
		s.mux.Handle("/link/", adapterProxy(adapter))
	}
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
		// Required by the Switch's user object; collected at registration so no adapter has to
		// invent them later.
		Country  string `json:"country"`
		Birthday string `json:"birthday"`
	}
	if !decode(w, r, &req) {
		return
	}
	acct, verifyToken, err := s.identity.RegisterWithProfile(r.Context(), req.Email, req.Password, req.DisplayName,
		identity.Profile{Country: req.Country, Birthday: req.Birthday})
	switch {
	case err == nil:
		// New account: deliver the verification email. A delivery failure
		// must NOT change the response (enumeration resistance) — the
		// user's repair path is retrying registration, which resends.
		if s.mailer != nil {
			_ = s.sendVerificationEmail(acct.Email, acct.DisplayName, verifyToken)
			break
		}
		// No mailer configured (development): make the flow completable by
		// surfacing the token; never the case in production wiring.
		writeJSON(w, http.StatusCreated, map[string]any{"account_id": acct.ID, "verification_token": verifyToken})
		return
	case errors.Is(err, identity.ErrEmailTaken):
		// Existing unverified account: resending on retry repairs the
		// failed-delivery trap. Response identical to fresh success.
		if token := s.identity.ResendVerificationIfUnverified(r.Context(), req.Email, req.Password); token != "" && s.mailer != nil {
			_ = s.sendVerificationEmail(normalizeEmailAddress(req.Email), "", token)
		}
		writeJSON(w, http.StatusOK, maskedVerificationMessage)
		return
	default:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Identical response for fresh success and (rare) mailer failure; the
	// duplicate path returns the same shape (enumeration resistance).
	writeJSON(w, http.StatusOK, maskedVerificationMessage)
}

// maskedVerificationMessage is the only public registration outcome once a
// mailer is configured: it neither confirms nor denies prior registration.
const maskedVerificationMessage = "if the address is registerable, a verification email has been sent"

func normalizeEmailAddress(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Server) sendVerificationEmail(to, displayName, token string) error {
	link := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.Verify, token)
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

// resendMessage is the only answer POST /v1/verify/resend gives: sent,
// throttled, already verified and mail failure all look the same.
const resendMessage = "if the address still needs confirming, a new link has been sent"

// handleVerifyResend mails a fresh verification link to the signed-in
// account's address. Throttled to one per 10 minutes per account (the same
// throttle the register-retry repair path uses); the old link stops working.
func (s *Server) handleVerifyResend(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	if token := s.identity.ResendVerificationForAccount(r.Context(), acct.ID); token != "" && s.mailer != nil {
		_ = s.sendVerificationEmail(acct.Email, acct.DisplayName, token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": resendMessage})
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
	// exists, via the mailer. A nil mailer (misconfiguration/development)
	// must not panic: respond identically and log (review follow-up).
	if token != "" && s.mailer != nil {
		if err := s.sendPasswordResetEmail(normalizeEmailAddress(req.Email), token); err != nil {
			// Response unchanged; the user can retry (a new token is
			// issued and old one remains valid until expiry).
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "if the address is registered, a reset email has been sent"})
}

func (s *Server) sendPasswordResetEmail(to, token string) error {
	link := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.ResetPassword, token)
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

// handleChangePassword rotates the signed-in account's password. Requires the current one, so
// a hijacked session cannot take the account over.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Keep the caller's own session alive; every other one is revoked inside ChangePassword.
	keep := ""
	if c, err := r.Cookie(sessionCookieName); err == nil {
		keep = c.Value
	}
	if err := s.identity.ChangePassword(r.Context(), acct.ID, req.Current, req.New, keep); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "password changed"})
}

// handleUpdateProfile fills in the country and date of birth of an account that predates
// registration collecting them. Both are optional: an account may legitimately hold neither,
// and nothing downstream invents a value for what is missing.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		Country     string `json:"country"`
		Birthday    string `json:"birthday"`
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	updated, err := s.identity.UpdateProfile(r.Context(), acct.ID, identity.Profile{
		Country: req.Country, Birthday: req.Birthday, DisplayName: req.DisplayName})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"country": updated.Country, "birthday": updated.Birthday,
		"display_name": updated.DisplayName,
	})
}

// adapterProxy forwards the console-link page to the Switch adapter under this service's own
// certificate.
func adapterProxy(target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "link service is not configured", http.StatusServiceUnavailable)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "the link service is unavailable", http.StatusBadGateway)
	}
	return proxy
}
