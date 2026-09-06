// Package identity implements account lifecycle: registration, email
// verification, login, sessions, and recovery. Port provenance: behavior
// modeled on Pretendo/account f7b1bc2 (AGPL-3.0); Go implementation is new.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"

	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrEmailTaken      = errors.New("identity: email already registered")
	ErrBadCredentials  = errors.New("identity: invalid credentials")
	ErrNotFound        = errors.New("identity: not found")
	ErrAccountBanned   = errors.New("identity: account banned")
	ErrAccountDeleted  = errors.New("identity: account deleted")
	ErrEmailUnverified = errors.New("identity: email not verified")
	ErrTokenInvalid    = errors.New("identity: token invalid or expired")
)

const (
	bcryptCost     = 12
	tokenTTL       = 24 * time.Hour
	maxLoginEmail  = 254
	maxPasswordLen = 128
)

type Status string

const (
	StatusActive          Status = "active"
	StatusBanned          Status = "banned"
	StatusDeletionPending Status = "deletion_pending"
	StatusDeleted         Status = "deleted"
)

type Account struct {
	ID            string
	Email         string
	DisplayName   string
	Status        Status
	EmailVerified bool
	CreatedAt     time.Time
}

type Service struct {
	pool       *pgxpool.Pool
	pepper     string
	sessionTTL time.Duration
}

func New(pool *pgxpool.Pool, pepper string, sessionTTL time.Duration) *Service {
	return &Service{pool: pool, pepper: pepper, sessionTTL: sessionTTL}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func hashToken(token, pepper string) string {
	h := sha256.New()
	h.Write([]byte(pepper))
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

// dummyHash is a valid bcrypt digest used to equalize login timing for
// unknown emails.
var dummyHash = func() string {
	// bcrypt of "\x00openpak-dummy" — constant, never matches real input pepper
	return "$2a$12$C6UzMDM.H6dfI/f/IKcEe.oO7f39nKPTVzB5nJ8rRsPZnu4zjQCBe"
}()

// randomToken returns 32 bytes of cryptographic randomness, hex-encoded.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: randomness: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validateRegistration(email, password, displayName string) error {
	if e := normalizeEmail(email); e == "" || len(e) > maxLoginEmail || !strings.Contains(e, "@") {
		return errors.New("identity: invalid email")
	}
	if len(password) < 8 || len(password) > maxPasswordLen {
		return errors.New("identity: password must be 8-128 characters")
	}
	dn := strings.TrimSpace(displayName)
	if dn == "" || len(dn) > 32 {
		return errors.New("identity: display name must be 1-32 characters")
	}
	return nil
}

// Register creates an account and returns the email-verification token (the
// caller delivers it; the service never emails directly).
func (s *Service) Register(ctx context.Context, email, password, displayName string) (*Account, string, error) {
	if err := validateRegistration(email, password, displayName); err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password+pepperSuffix(s.pepper)), bcryptCost)
	if err != nil {
		return nil, "", err
	}
	id := newUUID()
	normalized := normalizeEmail(email)
	_, err = s.pool.Exec(ctx, `INSERT INTO accounts (id, email, email_normalized, password_hash, display_name)
		VALUES ($1,$2,$3,$4,$5)`, id, strings.TrimSpace(email), normalized, string(hash), strings.TrimSpace(displayName))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, "", ErrEmailTaken
		}
		return nil, "", fmt.Errorf("identity: register: %w", err)
	}
	token, err := s.issueToken(ctx, id, "email_verify")
	if err != nil {
		return nil, "", err
	}
	return &Account{ID: id, Email: strings.TrimSpace(email), DisplayName: strings.TrimSpace(displayName),
		Status: StatusActive}, token, nil
}

// VerifyEmail consumes a single-use verification token.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	return s.consumeToken(ctx, token, "email_verify", func(ctx context.Context, accountID string) error {
		_, err := s.pool.Exec(ctx,
			`UPDATE accounts SET email_verified_at = now(), updated_at = now() WHERE id = $1`, accountID)
		return err
	})
}

// Login validates credentials and creates a session. Enumeration-safe: the
// same error is returned for unknown email and wrong password.
func (s *Service) Login(ctx context.Context, email, password string) (sessionToken string, acct *Account, err error) {
	acct, err = s.VerifyCredentials(ctx, email, password)
	if err != nil {
		return "", nil, err
	}
	token, err := s.createSession(ctx, acct.ID)
	if err != nil {
		return "", nil, err
	}
	return token, acct, nil
}

// VerifyCredentials checks email+password without creating a session. Used by
// the internal verification contract (PRD §7): adapters receive status only;
// no session, token, or hash ever crosses the boundary.
func (s *Service) VerifyCredentials(ctx context.Context, email, password string) (*Account, error) {
	normalized := normalizeEmail(email)
	var id, hash string
	var status Status
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash, status, email_verified_at
		FROM accounts WHERE email_normalized = $1`, normalized).
		Scan(&id, &hash, &status, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Equalize timing by performing a dummy bcrypt comparison.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password+pepperSuffix(s.pepper)))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := mapStatusErr(status); err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password+pepperSuffix(s.pepper))) != nil {
		return nil, ErrBadCredentials
	}
	return &Account{ID: id, Status: status, EmailVerified: verifiedAt != nil}, nil
}

// GetByID returns minimal account status/profile projection for internal
// callers. It intentionally exposes no email, birthdate, or device data
// (PRD §3 field-minimization note).
func (s *Service) GetByID(ctx context.Context, id string) (*Account, error) {
	var a Account
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, display_name, status, email_verified_at, created_at
		FROM accounts WHERE id = $1 AND status <> 'deleted'`, id).
		Scan(&a.ID, &a.DisplayName, &a.Status, &verifiedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.EmailVerified = verifiedAt != nil
	return &a, nil
}

// SessionAccount resolves a session token to its account. Expired/revoked
// sessions fail synchronously.
func (s *Service) SessionAccount(ctx context.Context, token string) (*Account, error) {
	var a Account
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT a.id, a.email, a.display_name, a.status, a.email_verified_at, a.created_at
		FROM sessions s JOIN accounts a ON a.id = s.account_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND s.revoked_at IS NULL`,
		hashToken(token, s.pepper)).
		Scan(&a.ID, &a.Email, &a.DisplayName, &a.Status, &verifiedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.EmailVerified = verifiedAt != nil
	return &a, nil
}

// ListSessions returns active session metadata for an account.
func (s *Service) ListSessions(ctx context.Context, accountID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, created_at, expires_at FROM sessions
		WHERE account_id = $1 AND expires_at > now() AND revoked_at IS NULL ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id string
		var created, expires time.Time
		if err := rows.Scan(&id, &created, &expires); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "created_at": created, "expires_at": expires})
	}
	return out, rows.Err()
}

// RevokeSession revokes one session owned by accountID.
func (s *Service) RevokeSession(ctx context.Context, accountID, sessionID string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1 AND account_id = $2 AND revoked_at IS NULL`,
		sessionID, accountID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RequestPasswordReset issues a single-use reset token if the account exists.
// The response does not reveal whether the email was registered.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) (token string, err error) {
	var id string
	err = s.pool.QueryRow(ctx, `SELECT id FROM accounts WHERE email_normalized = $1 AND status = 'active'`,
		normalizeEmail(email)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // silent no-op
	}
	if err != nil {
		return "", err
	}
	return s.issueToken(ctx, id, "password_reset")
}

// ResetPassword consumes a reset token and replaces the password.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < 8 || len(newPassword) > maxPasswordLen {
		return errors.New("identity: password must be 8-128 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword+pepperSuffix(s.pepper)), bcryptCost)
	if err != nil {
		return err
	}
	return s.consumeToken(ctx, token, "password_reset", func(ctx context.Context, accountID string) error {
		// Password rotation invalidates all existing sessions (FR-1).
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			`UPDATE accounts SET password_hash = $2, updated_at = now() WHERE id = $1`, accountID, string(hash)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL`, accountID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

func (s *Service) issueToken(ctx context.Context, accountID, purpose string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO lifecycle_tokens (id, account_id, purpose, token_hash, expires_at)
		VALUES ($1,$2,$3,$4,$5)`, newUUID(), accountID, purpose, hashToken(token, s.pepper), time.Now().Add(tokenTTL))
	if err != nil {
		return "", fmt.Errorf("identity: issue token: %w", err)
	}
	return token, nil
}

func (s *Service) consumeToken(ctx context.Context, token, purpose string, apply func(context.Context, string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID string
	err = tx.QueryRow(ctx, `UPDATE lifecycle_tokens SET used_at = now()
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING account_id`, hashToken(token, s.pepper), purpose).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if err := apply(ctx, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) createSession(ctx context.Context, accountID string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions (id, account_id, token_hash, expires_at)
		VALUES ($1,$2,$3,$4)`, newUUID(), accountID, hashToken(token, s.pepper), time.Now().Add(s.sessionTTL))
	if err != nil {
		return "", fmt.Errorf("identity: session: %w", err)
	}
	return token, nil
}

func mapStatusErr(s Status) error {
	switch s {
	case StatusActive:
		return nil
	case StatusBanned:
		return ErrAccountBanned
	case StatusDeleted, StatusDeletionPending:
		return ErrAccountDeleted
	}
	return ErrAccountDeleted
}

func pepperSuffix(pepper string) string { return "\x00" + pepper }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// Pool exposes the storage pool for tests and future internal components.
func (s *Service) Pool() *pgxpool.Pool { return s.pool }
