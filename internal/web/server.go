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
	"openpak/account/internal/mailtext"
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
	s.mux.HandleFunc("POST /v1/email/change", s.auth(s.handleEmailChange))
	s.mux.HandleFunc("POST /v1/email/confirm", s.handleEmailConfirm)
	s.mux.HandleFunc("GET /v1/email/confirm", s.handleEmailConfirmPage)
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("POST /v1/login/2fa", s.handleLogin2FA)
	s.mux.HandleFunc("POST /v1/2fa/setup", s.auth(s.handle2FASetup))
	s.mux.HandleFunc("POST /v1/2fa/enable", s.auth(s.handle2FAEnable))
	s.mux.HandleFunc("POST /v1/2fa/disable", s.auth(s.handle2FADisable))
	s.mux.HandleFunc("POST /v1/logout", s.auth(s.handleLogout))
	s.mux.HandleFunc("GET /v1/whoami", s.auth(s.handleWhoami))
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

// sessionToken is the web session a request carries: the cookie, or the same
// token as "Authorization: Bearer <token>" for server-side callers (the
// website holds the core session it created at sign-in).
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *identity.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		acct, err := s.identity.SessionAccount(r.Context(), token)
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
		Handle      string `json:"handle"`
		// Required by the Switch's user object; collected at registration so no adapter has to
		// invent them later.
		Country  string `json:"country"`
		Birthday string `json:"birthday"`
	}
	if !decode(w, r, &req) {
		return
	}
	acct, verifyToken, err := s.identity.RegisterWithHandle(r.Context(), req.Email, req.Password, req.DisplayName, req.Handle,
		identity.Profile{Country: req.Country, Birthday: req.Birthday})
	switch {
	case err == nil:
		// New account: deliver the verification email. A delivery failure
		// must NOT change the response (enumeration resistance) — the
		// user's repair path is retrying registration, which resends.
		if s.mailer != nil {
			_ = s.sendVerificationEmail(r, acct.Email, acct.DisplayName, verifyToken)
			break
		}
		// No mailer configured (development): make the flow completable by
		// surfacing the token; never the case in production wiring.
		writeJSON(w, http.StatusCreated, map[string]any{"account_id": acct.ID, "verification_token": verifyToken})
		return
	case errors.Is(err, identity.ErrHandleTaken):
		writeError(w, http.StatusConflict, "handle_taken")
		return
	case errors.Is(err, identity.ErrHandleInvalid), errors.Is(err, identity.ErrHandleReserved), errors.Is(err, identity.ErrHandleProfane):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, identity.ErrEmailTaken):
		// Existing unverified account: resending on retry repairs the
		// failed-delivery trap. Response identical to fresh success.
		if token := s.identity.ResendVerificationIfUnverified(r.Context(), req.Email, req.Password); token != "" && s.mailer != nil {
			_ = s.sendVerificationEmail(r, normalizeEmailAddress(req.Email), "", token)
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

func (s *Server) sendVerificationEmail(r *http.Request, to, displayName, token string) error {
	link := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.Verify, token)
	subject, body := mailtext.Get(mailtext.Lang(r.Header.Get("Accept-Language")), mailtext.Verify, displayName, link)
	return s.mailer.Send(to, subject, body)
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
		_ = s.sendVerificationEmail(r, acct.Email, acct.DisplayName, token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": resendMessage})
}

// emailChangeMessage is the one answer to an accepted change request: the
// response never says whether the new address belongs to someone else.
const emailChangeMessage = "if the address can be used, a confirmation link has been sent to it"

// handleEmailChange starts moving the signed-in account to a new address. The
// password is required (a borrowed session must not be enough to take the
// account's recovery address). The link goes to the NEW address, a notice to
// the old one; nothing changes until the link is followed.
func (s *Server) handleEmailChange(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		NewEmail string `json:"new_email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	ch, err := s.identity.RequestEmailChange(r.Context(), acct.ID, req.Password, req.NewEmail)
	switch {
	case errors.Is(err, identity.ErrBadCredentials):
		writeError(w, http.StatusForbidden, "password is incorrect")
		return
	case errors.Is(err, identity.ErrSameEmail):
		writeError(w, http.StatusBadRequest, "that is already your address")
		return
	case err != nil && strings.HasPrefix(err.Error(), "identity: invalid"):
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ch.Token != "" && s.mailer != nil {
		link := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.ConfirmEmail, ch.Token)
		lang := mailtext.Lang(r.Header.Get("Accept-Language"))
		subject, body := mailtext.Get(lang, mailtext.EmailChange, acct.DisplayName, link)
		_ = s.mailer.Send(ch.NewEmail, subject, body)
		subject, body = mailtext.Get(lang, mailtext.EmailChangeNotice, acct.DisplayName, "")
		_ = s.mailer.Send(ch.OldEmail, subject, body)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": emailChangeMessage})
}

// handleEmailConfirm applies an email change from its token. JSON in, JSON
// out; a form post (the core's own page below) gets a page back.
func (s *Server) handleEmailConfirm(w http.ResponseWriter, r *http.Request) {
	form := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	var token string
	if form {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid form")
			return
		}
		token = r.PostFormValue("token")
	} else {
		var req struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &req) {
			return
		}
		token = req.Token
	}
	_, err := s.identity.ConfirmEmailChange(r.Context(), token)
	if form {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "Your email address has been changed."
		if err != nil {
			msg = "This confirmation link is invalid or expired."
		}
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><body><p>` + msg + `</p></body></html>`))
		return
	}
	if err != nil {
		if errors.Is(err, identity.ErrTokenInvalid) {
			writeError(w, http.StatusBadRequest, "confirmation link is invalid or expired")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "email address changed"})
}

// handleEmailConfirmPage is where the mailed link lands when the core serves
// its own links: a button, so a mail scanner fetching the URL changes nothing.
func (s *Server) handleEmailConfirmPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "token required")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Confirm email address</title></head>
<body><form method="post" action="/v1/email/confirm">
<h1>Confirm your new email address</h1>
<input type="hidden" name="token" value="` + htmlEscape(token) + `">
<button type="submit">Confirm</button>
</form></body></html>`))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, challenge, acct, err := s.identity.BeginLogin(r.Context(), req.Email, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if challenge != "" {
		// Password right, second factor owed: no cookie until /v1/login/2fa.
		writeJSON(w, http.StatusAccepted, map[string]any{"two_factor_required": true, "challenge": challenge})
		return
	}
	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"message": "signed in", "account_id": acct.ID})
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

// handleLogin2FA completes a sign-in that POST /v1/login answered with a
// challenge: {challenge, code} or {challenge, recovery_code}.
func (s *Server) handleLogin2FA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge    string `json:"challenge"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, acct, err := s.identity.CompleteLogin(r.Context(), req.Challenge, req.Code, req.RecoveryCode)
	switch {
	case err == nil:
		setSessionCookie(w, token)
		writeJSON(w, http.StatusOK, map[string]string{"message": "signed in", "account_id": acct.ID})
	case errors.Is(err, identity.ErrTokenInvalid):
		writeError(w, http.StatusUnauthorized, "sign-in challenge is invalid or expired; sign in again")
	default:
		writeTwoFactorError(w, err)
	}
}

// writeTwoFactorError maps the two-factor outcomes every route shares.
func writeTwoFactorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrBadCredentials):
		writeError(w, http.StatusForbidden, "password is incorrect")
	case errors.Is(err, identity.ErrBadSecondFactor):
		writeError(w, http.StatusUnauthorized, "invalid code")
	case errors.Is(err, identity.ErrTwoFactorLocked):
		w.Header().Set("Retry-After", "900")
		writeError(w, http.StatusTooManyRequests, "too many attempts; try again in 15 minutes")
	case errors.Is(err, identity.ErrTwoFactorEnabled), errors.Is(err, identity.ErrTwoFactorNotEnabled),
		errors.Is(err, identity.ErrTwoFactorNotPending):
		writeError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "identity: "))
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// handle2FASetup starts enrolment and returns the secret to put in an
// authenticator app. Requires the password: a borrowed session must not be
// able to tie the account to someone else's phone.
func (s *Server) handle2FASetup(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	secret, uri, err := s.identity.BeginTwoFactor(r.Context(), acct.ID, req.Password)
	if err != nil {
		writeTwoFactorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri})
}

// handle2FAEnable finishes enrolment with a first code and hands out the ten
// recovery codes, once.
func (s *Server) handle2FAEnable(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	codes, err := s.identity.EnableTwoFactor(r.Context(), acct.ID, req.Code)
	if err != nil {
		writeTwoFactorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handle2FADisable needs the password and a current code (a recovery code
// counts, for someone who lost the phone).
func (s *Server) handle2FADisable(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.identity.DisableTwoFactor(r.Context(), acct.ID, req.Password, req.Code); err != nil {
		writeTwoFactorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "two-factor authentication disabled"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	if token := sessionToken(r); token != "" {
		// Revoke in storage BEFORE reporting success (review P1 #2).
		if err := s.identity.RevokeByToken(r.Context(), token); err != nil {
			writeError(w, http.StatusInternalServerError, "could not sign out; session still active")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]string{"message": "signed out"})
}

// handleWhoami answers who the session belongs to (website contract request
// CR-4), so a caller that just signed someone in never has to re-verify the
// password to learn the account id.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request, acct *identity.Account) {
	twoFA, err := s.identity.TwoFactorEnabled(r.Context(), acct.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	status := string(acct.Status)
	if acct.Status == identity.StatusDeletionPending {
		status = "deletion-pending"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id":         acct.ID,
		"email":              acct.Email,
		"display_name":       acct.DisplayName,
		"handle":             acct.Handle,
		"status":             status,
		"email_verified":     acct.EmailVerified,
		"two_factor_enabled": twoFA,
	})
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
		if err := s.sendPasswordResetEmail(r, normalizeEmailAddress(req.Email), token); err != nil {
			// Response unchanged; the user can retry (a new token is
			// issued and old one remains valid until expiry).
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "if the address is registered, a reset email has been sent"})
}

func (s *Server) sendPasswordResetEmail(r *http.Request, to, token string) error {
	link := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.ResetPassword, token)
	subject, body := mailtext.Get(mailtext.Lang(r.Header.Get("Accept-Language")), mailtext.Reset, "", link)
	return s.mailer.Send(to, subject, body)
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
	keep := sessionToken(r)
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
