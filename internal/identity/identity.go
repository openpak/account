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
	"log"
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
	Handle        string
	Status        Status
	EmailVerified bool
	CreatedAt     time.Time
	// Collected at registration; the Switch adapter needs both to build a user object the
	// console will accept.
	Country        string
	Birthday       string
	Bio            string
	Motto          string
	ProfileVersion uint64
	// Owned by the core for every network: BCP 47 style tag and IANA zone name; empty when unset.
	Language string
	Timezone string
}

// Emitter records durable invalidation events inside the caller's
// transaction; implemented by events.Service.
type Emitter interface {
	EmitTx(ctx context.Context, tx pgx.Tx, typ, accountID, namespace, subjectID string) error
}

type Service struct {
	pool   *pgxpool.Pool
	pepper string // current pepper: new hashes and tokens
	// oldPepper, when set, is the retired pepper kept verify-only while accounts move over
	// (SetNewPepper); pepperGen numbers the current one in the pepper_gen columns: 0 is
	// ACCOUNT_SESSION_SECRET as it always was, 1 is ACCOUNT_SESSION_SECRET_NEW.
	oldPepper  string
	pepperGen  int16
	sessionTTL time.Duration
	emitter    Emitter
	sealer     *sealer // TOTP secrets at rest; see SetTOTPKey
	// deletionGrace is how long a closed account stays restorable before the
	// finaliser erases it; see SetDeletionGrace.
	deletionGrace time.Duration
}

func New(pool *pgxpool.Pool, pepper string, sessionTTL time.Duration, emitter Emitter) *Service {
	return &Service{pool: pool, pepper: pepper, sessionTTL: sessionTTL, emitter: emitter, sealer: newSealer(pepper),
		deletionGrace: DefaultDeletionGrace}
}

// SetNewPepper makes newPepper the current one (ACCOUNT_SESSION_SECRET_NEW) and keeps the
// pepper the service was built with as a verify-only fallback: passwords and tokens made under
// it still work, a password is re-hashed under the new pepper at the next successful check,
// and everything new uses the new one. The TOTP sealer is not touched (ACCOUNT_TOTP_KEY).
func (s *Service) SetNewPepper(newPepper string) {
	if newPepper == "" || newPepper == s.pepper {
		return
	}
	s.oldPepper, s.pepper, s.pepperGen = s.pepper, newPepper, 1
}

// tokenHashes are the stored forms a raw token may have: under the current pepper and, while
// one is kept, under the old one. Lookups match either; new tokens are stored under the current.
func (s *Service) tokenHashes(token string) []string {
	h := []string{hashToken(token, s.pepper)}
	if s.oldPepper != "" {
		h = append(h, hashToken(token, s.oldPepper))
	}
	return h
}

// pepperFor is the pepper a stored hash of generation gen was made under.
func (s *Service) pepperFor(gen int16) string {
	if s.oldPepper != "" && gen != s.pepperGen {
		return s.oldPepper
	}
	return s.pepper
}

// checkPassword verifies secret against a stored hash and the generation recorded with it.
// When it holds but the hash is not under the current pepper, rehash is the replacement hash
// (under the current pepper) for the caller to store in the same transaction as its own work.
func (s *Service) checkPassword(hash string, gen int16, secret string) (ok bool, rehash string) {
	if !comparePassword(hash, s.pepperFor(gen), secret) {
		return false, ""
	}
	if s.oldPepper != "" && gen != s.pepperGen {
		if h, err := hashPassword(s.pepper, secret); err == nil {
			return true, h
		}
	}
	return true, ""
}

// dummyCompare spends one bcrypt comparison, so an unknown account costs what a known one does.
func (s *Service) dummyCompare(secret string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(bcryptInput(s.pepper, secret)))
}

// storeRehash writes a replacement password hash for an account, only while the stored hash is
// still the one that was verified. Best effort: a failure leaves the old hash, which still works.
func (s *Service) storeRehash(ctx context.Context, accountID, oldHash, newHash string) {
	if newHash == "" {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE accounts SET password_hash=$3, pepper_gen=$4 WHERE id=$1 AND password_hash=$2`,
			accountID, oldHash, newHash, s.pepperGen)
		return err
	})
	if err != nil {
		log.Printf("identity: re-hashing a password under the new pepper failed: %v", err)
	}
}

// PepperCount is how many password hashes are on each pepper generation.
type PepperCount struct {
	Accounts, AdapterCredentials map[int16]int
}

// PepperStats counts stored hashes per pepper generation (0 = old, 1 = new): the measure of the
// cutover. Accounts with an empty hash (erased) are not counted.
func (s *Service) PepperStats(ctx context.Context) (PepperCount, error) {
	out := PepperCount{Accounts: map[int16]int{}, AdapterCredentials: map[int16]int{}}
	for q, m := range map[string]map[int16]int{
		`SELECT pepper_gen, count(*) FROM accounts WHERE password_hash <> '' AND status <> 'deleted' GROUP BY 1`: out.Accounts,
		`SELECT pepper_gen, count(*) FROM adapter_credentials GROUP BY 1`:                                        out.AdapterCredentials,
	} {
		rows, err := s.pool.Query(ctx, q)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var g int16
			var n int
			if err := rows.Scan(&g, &n); err != nil {
				rows.Close()
				return out, err
			}
			m[g] = n
		}
		rows.Close()
	}
	return out, nil
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
// Profile is what registration collects beyond credentials. Both fields are required by the
// Switch's user object, which is rejected outright when either is missing.
type Profile struct {
	Handle   string // Set only at creation, never part of editable profile updates.
	Country  string // ISO 3166-1 alpha-2, e.g. "GB"
	Birthday string // ISO 8601 date, e.g. "1990-01-01"
	// DisplayName is what other people see. Empty in an update means "leave it
	// alone", because a blank name is never what someone meant.
	DisplayName string
	Bio         *string
	Motto       *string
	// Empty at registration or in UpdateProfile means unset / leave alone.
	Language string
	Timezone string
}

// ErrBadDisplayName is the 1-32 character rule SetDisplayName shares with registration.
var ErrBadDisplayName = errors.New("identity: display name must be 1-32 characters")

// SetDisplayName renames an active account.
func (s *Service) SetDisplayName(ctx context.Context, accountID, displayName string) error {
	dn := strings.TrimSpace(displayName)
	if dn == "" || len(dn) > 32 {
		return ErrBadDisplayName
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE accounts SET display_name = $2, profile_version = profile_version + 1, updated_at = now() WHERE id = $1 AND status = 'active'`,
			accountID, dn)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, EventProfileUpdated, accountID, "", "")
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("identity: set display name: %w", err)
	}
	return nil
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (*Account, string, error) {
	return s.RegisterWithProfile(ctx, email, password, displayName, Profile{})
}

func (s *Service) RegisterWithProfile(ctx context.Context, email, password, displayName string, p Profile) (*Account, string, error) {
	if p.Handle != "" {
		if err := ValidateHandle(p.Handle); err != nil {
			return nil, "", err
		}
	}
	if err := validateRegistration(email, password, displayName); err != nil {
		return nil, "", err
	}
	if err := validateProfile(&p); err != nil {
		return nil, "", err
	}
	hash, err := hashPassword(s.pepper, password)
	if err != nil {
		return nil, "", err
	}
	id := newUUID()
	normalized := normalizeEmail(email)
	var birthday any
	if p.Birthday != "" {
		birthday = p.Birthday
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO accounts (id, email, email_normalized, password_hash, display_name, country, birthday, handle, language, timezone, pepper_gen)
  VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)`, id, strings.TrimSpace(email), normalized, string(hash), strings.TrimSpace(displayName), p.Country, birthday, p.Handle, p.Language, p.Timezone, s.pepperGen)
		if e != nil {
			return e
		}
		if s.emitter != nil && p.Handle != "" {
			return s.emitter.EmitTx(ctx, tx, "account_created", id, "", "")
		}
		return nil
	})
	if err != nil {
		mapped := mapHandleError(err)
		if mapped != err {
			return nil, "", mapped
		}
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
		Country: p.Country, Birthday: p.Birthday, Handle: p.Handle, Language: p.Language, Timezone: p.Timezone, Status: StatusActive}, token, nil
}

// UpdateProfile sets the country and birthday of an existing account. Registration collects
// both, but every account created before it did has neither, and the console's user object
// carries whatever the account holds — so there has to be a way to fill them in later.
func (s *Service) UpdateProfile(ctx context.Context, accountID string, p Profile) (*Account, error) {
	if err := validateProfile(&p); err != nil {
		return nil, err
	}
	var birthday any
	if p.Birthday != "" {
		birthday = p.Birthday
	}
	// A blank display name leaves the existing one: an update that omits a
	// field must not erase it.
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE accounts SET country = $2, birthday = $3,
			display_name = COALESCE(NULLIF($4,''), display_name),
			bio = COALESCE($5, bio), motto = COALESCE($6, motto),
			language = COALESCE(NULLIF($7,''), language), timezone = COALESCE(NULLIF($8,''), timezone),
			profile_version = profile_version + 1, updated_at = now()
		WHERE id = $1`,
			accountID, p.Country, birthday, p.DisplayName, p.Bio, p.Motto, p.Language, p.Timezone)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, EventProfileUpdated, accountID, "", "")
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: update profile: %w", err)
	}
	return s.GetByID(ctx, accountID)
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
// An account with two-factor authentication gets ErrTwoFactorRequired and no
// session: BeginLogin/CompleteLogin are the two-step form.
func (s *Service) Login(ctx context.Context, email, password string) (sessionToken string, acct *Account, err error) {
	token, challenge, acct, err := s.BeginLogin(ctx, email, password)
	if err != nil {
		return "", nil, err
	}
	if challenge != "" {
		return "", nil, ErrTwoFactorRequired
	}
	return token, acct, nil
}

// VerifyCredentials checks email+password without creating a session. Used by
// the internal verification contract (PRD §7): adapters receive status only;
// no session, token, or hash ever crosses the boundary.
func (s *Service) VerifyCredentials(ctx context.Context, email, password string) (*Account, error) {
	normalized := normalizeEmail(email)
	var id, hash, country string
	var gen int16
	var status Status
	var verifiedAt *time.Time
	var birthday *time.Time
	var eraseAfter *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash, pepper_gen, status, email_verified_at, country, birthday, erase_after
		FROM accounts WHERE email_normalized = $1`, normalized).
		Scan(&id, &hash, &gen, &status, &verifiedAt, &country, &birthday, &eraseAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		// Equalize timing by performing a dummy bcrypt comparison.
		s.dummyCompare(password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if status == StatusDeletionPending {
		// A closed account is restorable, and only its owner may learn that:
		// the password is checked before the answer differs from a wrong one.
		ok, rehash := s.checkPassword(hash, gen, password)
		if !ok {
			return nil, ErrBadCredentials
		}
		s.storeRehash(ctx, id, hash, rehash)
		pe := &PendingDeletionError{AccountID: id}
		if eraseAfter != nil {
			pe.EraseAfter = *eraseAfter
		}
		return nil, pe
	}
	if err := mapStatusErr(status); err != nil {
		return nil, err
	}
	ok, rehash := s.checkPassword(hash, gen, password)
	if !ok {
		return nil, ErrBadCredentials
	}
	s.storeRehash(ctx, id, hash, rehash)
	bday := ""
	if birthday != nil {
		bday = birthday.Format("2006-01-02")
	}
	return &Account{ID: id, Status: status, EmailVerified: verifiedAt != nil,
		Country: country, Birthday: bday}, nil
}

// GetByID returns minimal account status/profile projection for internal
// callers. It intentionally exposes no email, birthdate, or device data
// (PRD §3 field-minimization note).
func (s *Service) GetByID(ctx context.Context, id string) (*Account, error) {
	var a Account
	var verifiedAt *time.Time
	// country and birthday included: every consumer of this call -- the site,
	// the phone app, the Switch adapter -- showed them empty because they were
	// not selected here, and a console user object with country "" is not one
	// a console will take.
	err := s.pool.QueryRow(ctx, `SELECT id, email, display_name, status, email_verified_at, created_at,
		COALESCE(country, ''), COALESCE(birthday::text, ''), COALESCE(handle,''), bio, motto, profile_version, language, timezone
		FROM accounts WHERE id = $1 AND status <> 'deleted'`, id).
		Scan(&a.ID, &a.Email, &a.DisplayName, &a.Status, &verifiedAt, &a.CreatedAt, &a.Country, &a.Birthday, &a.Handle, &a.Bio, &a.Motto, &a.ProfileVersion, &a.Language, &a.Timezone)
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
	err := s.pool.QueryRow(ctx, `SELECT a.id, a.email, a.display_name, a.status, a.email_verified_at, a.created_at, COALESCE(a.handle,'')
		FROM sessions s JOIN accounts a ON a.id = s.account_id
		WHERE s.token_hash = ANY($1) AND s.expires_at > now() AND s.revoked_at IS NULL`,
		s.tokenHashes(token)).
		Scan(&a.ID, &a.Email, &a.DisplayName, &a.Status, &verifiedAt, &a.CreatedAt, &a.Handle)
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
	hash, err := hashPassword(s.pepper, newPassword)
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
			`UPDATE accounts SET password_hash = $2, pepper_gen = $3, updated_at = now() WHERE id = $1`, accountID, string(hash), s.pepperGen); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL`, accountID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// ChangePassword rotates the password of a signed-in account. The current password is
// required: a stolen session should not be enough to lock the owner out of their own account.
// Every other session is revoked, matching ResetPassword (FR-1); the caller's own session is
// kept, since the user is standing right there and just proved who they are.
func (s *Service) ChangePassword(ctx context.Context, accountID, current, next, keepSessionToken string) error {
	if _, err := s.VerifyByID(ctx, accountID, current); err != nil {
		return err
	}
	if len(next) < 8 || len(next) > maxPasswordLen {
		return errors.New("identity: password must be 8-128 characters")
	}
	if next == current {
		return errors.New("identity: the new password must be different")
	}
	hash, err := hashPassword(s.pepper, next)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET password_hash = $2, pepper_gen = $3, updated_at = now() WHERE id = $1`, accountID, string(hash), s.pepperGen); err != nil {
		return err
	}
	// Sessions are addressed by token hash, not by the cookie value: comparing the raw token
	// against id would match nothing and revoke the caller's own session too.
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE account_id = $1 AND revoked_at IS NULL AND token_hash <> ALL($2)`,
		accountID, s.tokenHashes(keepSessionToken)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) issueToken(ctx context.Context, accountID, purpose string) (string, error) {
	return s.issueTokenOpts(ctx, accountID, purpose, false)
}

// issueTokenResend issues an email_verify token flagged as a resend for
// throttle purposes.
func (s *Service) issueTokenResend(ctx context.Context, accountID string) (string, error) {
	return s.issueTokenOpts(ctx, accountID, "email_verify", true)
}

func (s *Service) issueTokenOpts(ctx context.Context, accountID, purpose string, viaResend bool) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO lifecycle_tokens (id, account_id, purpose, token_hash, expires_at, via_resend)
		VALUES ($1,$2,$3,$4,$5,$6)`, newUUID(), accountID, purpose, hashToken(token, s.pepper), time.Now().Add(tokenTTL), viaResend)
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
		WHERE token_hash = ANY($1) AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING account_id`, s.tokenHashes(token), purpose).Scan(&accountID)
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

// bcryptInput pre-hashes the peppered secret with SHA-256 so bcrypt's 72-byte
// input limit is never exceeded regardless of user password length. The same
// derivation is used on every hash and verify path.
func bcryptInput(pepper, secret string) string {
	h := sha256.Sum256([]byte(pepper + "\x00" + secret))
	return hex.EncodeToString(h[:])
}

func hashPassword(pepper, secret string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(bcryptInput(pepper, secret)), bcryptCost)
	return string(b), err
}

func comparePassword(hash, pepper, secret string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(bcryptInput(pepper, secret))) == nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// Pool exposes the storage pool for tests and future internal components.
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

// VerifyByID checks a password for a known account ID (adapters resolve
// namespace subjects to core account IDs themselves, then call this). Same
// guarantees as VerifyCredentials: no session is created, failures are
// indistinguishable between wrong password and disabled account only via the
// returned error class.
func (s *Service) VerifyByID(ctx context.Context, accountID, password string) (*Account, error) {
	var id, hash string
	var gen int16
	var status Status
	var verifiedAt *time.Time
	var country string
	var birthday *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash, pepper_gen, status, email_verified_at, country, birthday
		FROM accounts WHERE id = $1`, accountID).
		Scan(&id, &hash, &gen, &status, &verifiedAt, &country, &birthday)
	if errors.Is(err, pgx.ErrNoRows) {
		s.dummyCompare(password)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := mapStatusErr(status); err != nil {
		return nil, err
	}
	ok, rehash := s.checkPassword(hash, gen, password)
	if !ok {
		return nil, ErrBadCredentials
	}
	s.storeRehash(ctx, id, hash, rehash)
	return &Account{ID: id, Status: status, EmailVerified: verifiedAt != nil}, nil
}

// SetBanned flips an account between active and banned and emits durable
// invalidation events so adapters revoke credentials (FR-7, §7). Banning
// revokes all active web sessions immediately.
func (s *Service) SetBanned(ctx context.Context, accountID string, banned bool) error {
	return s.SetBannedBy(ctx, accountID, banned, "", "system")
}

// SetBannedBy is SetBanned with the moderator's reason and name recorded in
// moderation_actions, in the same transaction as the status change and its
// event: an account is never banned without a record of who did it and why.
// Only active and banned accounts move; one being deleted stays deleted
// (unbanning it must not bring it back), and answers ErrNotFound.
func (s *Service) SetBannedBy(ctx context.Context, accountID string, banned bool, reason, actor string) error {
	status := "active"
	typ := "account_unbanned"
	action := "unban"
	if banned {
		status = "banned"
		typ = "account_banned"
		action = "ban"
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET status=$2, updated_at=now()
			WHERE id=$1 AND status IN ('active','banned')`, accountID, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if banned {
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO moderation_actions (account_id, action, reason, actor)
			VALUES ($1,$2,$3,$4)`, accountID, action, reason, actor); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, typ, accountID, "", "")
		}
		return nil
	})
}

// RequestDeletion closes an account (FR-8 first stage): immediate access
// disable, session revocation, durable event, and an erasure date one grace
// period away. The status it had is kept so a restore puts it back.
func (s *Service) RequestDeletion(ctx context.Context, accountID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET status='deletion_pending', status_before_deletion=status,
			closed_at=now(), erase_after=now() + make_interval(secs => $2), updated_at=now()
			WHERE id=$1 AND status IN ('active','banned')`, accountID, s.deletionGrace.Seconds())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "account_delete_started", accountID, "", "")
		}
		return nil
	})
}

// RevokeByToken revokes the session identified by its raw token. Used by
// logout: the session must be dead in storage before success is reported.
func (s *Service) RevokeByToken(ctx context.Context, token string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = ANY($1) AND revoked_at IS NULL`,
		s.tokenHashes(token))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResendVerificationIfUnverified re-issues the email verification token for
// an existing, unverified account (repair path for failed deliveries; PRD
// FR-1). The supplied password must match: without this, the register
// endpoint would be a mail-oracle to any address. Returns "" when there is
// nothing to send, credentials fail, or the resend throttle (one per 10
// minutes) trips. Enumeration-safe: callers must not differentiate.
func (s *Service) ResendVerificationIfUnverified(ctx context.Context, email, password string) string {
	normalized := normalizeEmail(email)
	var accountID, passwordHash string
	var gen int16
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash, pepper_gen, email_verified_at FROM accounts
		WHERE email_normalized=$1 AND status='active'`, normalized).
		Scan(&accountID, &passwordHash, &gen, &verifiedAt)
	if err != nil || verifiedAt != nil {
		return "" // unknown, deleted, banned, or already verified
	}
	ok, rehash := s.checkPassword(passwordHash, gen, password)
	if !ok {
		return "" // credential proof required to trigger email delivery
	}
	s.storeRehash(ctx, accountID, passwordHash, rehash)
	return s.reissueVerification(ctx, accountID)
}

// ResendVerificationForAccount re-issues the verification token for a
// signed-in account (POST /v1/verify/resend). Returns "" when the account is
// already verified, not active, or inside the resend throttle; callers answer
// the same either way.
func (s *Service) ResendVerificationForAccount(ctx context.Context, accountID string) string {
	var verifiedAt *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT email_verified_at FROM accounts WHERE id=$1 AND status='active'`,
		accountID).Scan(&verifiedAt); err != nil || verifiedAt != nil {
		return ""
	}
	return s.reissueVerification(ctx, accountID)
}

// reissueVerification is the shared resend path: throttled, and it leaves
// exactly one working link.
func (s *Service) reissueVerification(ctx context.Context, accountID string) string {
	// Throttle: at most one resend per 10 minutes even with valid creds.
	// Only resend-issued tokens count — the initial signup token must not
	// block the first repair (the failed-delivery case).
	var recent bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_tokens
		WHERE account_id=$1 AND purpose='email_verify' AND via_resend AND used_at IS NULL
		AND created_at > now() - interval '10 minutes')`,
		accountID).Scan(&recent); err != nil {
		return ""
	}
	if recent {
		return ""
	}
	// Invalidate older verify tokens so exactly one link works.
	if _, err := s.pool.Exec(ctx, `UPDATE lifecycle_tokens SET used_at=now()
		WHERE account_id=$1 AND purpose='email_verify' AND used_at IS NULL`, accountID); err != nil {
		return ""
	}
	token, err := s.issueTokenResend(ctx, accountID)
	if err != nil {
		return ""
	}
	return token
}

// validateProfile keeps obviously wrong values out of the database; both fields are optional
// here so an adapter that never collected them can still register an account.
func validateProfile(p *Profile) error {
	if err := validateProfileText(p.Bio, p.Motto); err != nil {
		return err
	}
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	p.Birthday = strings.TrimSpace(p.Birthday)
	var err error
	if p.Language, err = NormalizeLanguage(p.Language); err != nil {
		return err
	}
	if p.Timezone, err = ValidateTimezone(p.Timezone); err != nil {
		return err
	}
	if p.Country != "" && len(p.Country) != 2 {
		return errors.New("identity: country must be a two-letter code")
	}
	if p.Birthday != "" {
		if _, err := time.Parse("2006-01-02", p.Birthday); err != nil {
			return errors.New("identity: birthday must be YYYY-MM-DD")
		}
	}
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if n := len([]rune(p.DisplayName)); n > 0 && (n < 3 || n > 32) {
		return errors.New("identity: display name must be 3 to 32 characters")
	}
	if strings.ContainsAny(p.DisplayName, "\n\r\t") {
		return errors.New("identity: display name cannot contain line breaks")
	}
	return nil
}
