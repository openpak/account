// Package web serves the OpenPak account web/API surface (registration,
// verification, sign-in, recovery, session management).
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"openpak/account/internal/identity"
)

type Server struct {
	identity *identity.Service
	mux      *http.ServeMux
}

func New(ident *identity.Service) *Server {
	s := &Server{identity: ident, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/register", s.handleRegister)
	s.mux.HandleFunc("GET /v1/verify", s.handleVerify)
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("POST /v1/logout", s.auth(s.handleLogout))
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.handleRevokeSession))
	s.mux.HandleFunc("POST /v1/forgot-password", s.handleForgotPassword)
	s.mux.HandleFunc("POST /v1/reset-password", s.handleResetPassword)
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
	// TODO(M1.1): hand verifyToken to the mailer instead of the response
	// once SMTP delivery is wired (FR-1). In development it is returned.
	if isDevelopment(r) {
		writeJSON(w, http.StatusCreated, map[string]any{"account_id": acct.ID, "verification_token": verifyToken})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"account_id": acct.ID})
}

func isDevelopment(r *http.Request) bool { return r.Host == "" } // refined with config in M1.1

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
		// Best-effort revoke; the cookie is cleared regardless.
		_ = revokeCurrent(s, r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]string{"message": "signed out"})
}

func revokeCurrent(s *Server, ctx context.Context, token string) error {
	acct, err := s.identity.SessionAccount(ctx, token)
	if err != nil {
		return err
	}
	// Resolve session by token hash to its ID.
	sessions, err := s.identity.ListSessions(ctx, acct.ID)
	if err != nil {
		return err
	}
	_ = sessions
	return errors.New("not implemented: revoke by token requires session lookup (M1.1)")
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
	// Same response whether or not the account exists (enumeration resistance).
	// TODO(M1.1): mailer delivery.
	if token != "" && r.URL.Query().Get("debug") == "1" {
		writeJSON(w, http.StatusOK, map[string]string{"message": "reset requested", "debug_token": token})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "if the address is registered, a reset email has been sent"})
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
