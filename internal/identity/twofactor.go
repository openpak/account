package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrTwoFactorRequired: the password was right, and a second factor is
	// still owed before anything counts as signed in.
	ErrTwoFactorRequired = errors.New("identity: two-factor authentication required")
	// ErrBadSecondFactor: wrong, reused or malformed code.
	ErrBadSecondFactor = errors.New("identity: invalid two-factor code")
	// ErrTwoFactorLocked: too many wrong codes; wait for the window to pass.
	ErrTwoFactorLocked     = errors.New("identity: too many two-factor attempts; try again later")
	ErrTwoFactorEnabled    = errors.New("identity: two-factor authentication is already enabled")
	ErrTwoFactorNotEnabled = errors.New("identity: two-factor authentication is not enabled")
	ErrTwoFactorNotPending = errors.New("identity: no two-factor enrolment in progress")
)

const (
	recoveryCodeCount   = 10
	twoFactorMaxFails   = 5
	twoFactorLockWindow = 15 * time.Minute
	loginChallengeTTL   = 10 * time.Minute
	challengeMaxFails   = 5
)

// SetTOTPKey replaces the key TOTP secrets are sealed with (ACCOUNT_TOTP_KEY).
// Without it the key is derived from the session pepper.
func (s *Service) SetTOTPKey(secret string) { s.sealer = newSealer(secret) }

// TwoFactorEnabled reports whether the account has finished enrolment.
func (s *Service) TwoFactorEnabled(ctx context.Context, accountID string) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_two_factor
		WHERE account_id=$1 AND enabled_at IS NOT NULL)`, accountID).Scan(&on)
	return on, err
}

// BeginTwoFactor starts (or restarts) enrolment: a fresh secret, stored
// sealed and pending until EnableTwoFactor sees a code from it. The password
// is required so a borrowed session cannot tie the account to someone else's
// phone. Returns the base32 secret and the otpauth:// URI for a QR code.
func (s *Service) BeginTwoFactor(ctx context.Context, accountID, password string) (secretB32, uri string, err error) {
	if _, err := s.VerifyByID(ctx, accountID, password); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", "", ErrBadCredentials
		}
		return "", "", err
	}
	raw := make([]byte, 20) // 160 bits, the RFC 4226 recommendation
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	sealed, err := s.sealer.seal(raw, accountID)
	if err != nil {
		return "", "", err
	}
	var email string
	if err := s.pool.QueryRow(ctx, `SELECT email FROM accounts WHERE id=$1`, accountID).Scan(&email); err != nil {
		return "", "", err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO account_two_factor (account_id, secret_enc) VALUES ($1,$2)
		ON CONFLICT (account_id) DO UPDATE SET secret_enc=EXCLUDED.secret_enc, last_step=0, created_at=now()
		WHERE account_two_factor.enabled_at IS NULL`, accountID, sealed)
	if err != nil {
		return "", "", err
	}
	if tag.RowsAffected() == 0 {
		return "", "", ErrTwoFactorEnabled
	}
	secretB32 = b32.EncodeToString(raw)
	return secretB32, otpauthURI(secretB32, email), nil
}

// EnableTwoFactor finishes enrolment with a code from the pending secret and
// returns the ten recovery codes, the only time they exist in the clear.
func (s *Service) EnableTwoFactor(ctx context.Context, accountID, code string) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes[i] = c
	}
	var verr error
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, verr = s.checkFactor(ctx, tx, accountID, code, "", false)
		if verr != nil {
			return nil // commit the failure count
		}
		if _, err := tx.Exec(ctx, `UPDATE account_two_factor SET enabled_at=now() WHERE account_id=$1`, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM two_factor_recovery_codes WHERE account_id=$1`, accountID); err != nil {
			return err
		}
		for _, c := range codes {
			if _, err := tx.Exec(ctx, `INSERT INTO two_factor_recovery_codes (account_id, code_hash) VALUES ($1,$2)`,
				accountID, hashToken(normalizeRecoveryCode(c), s.pepper)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if verr != nil {
		return nil, verr
	}
	return codes, nil
}

// DisableTwoFactor turns it off: password and a current code (or a recovery
// code) are both required.
func (s *Service) DisableTwoFactor(ctx context.Context, accountID, password, code string) error {
	if _, err := s.VerifyByID(ctx, accountID, password); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrBadCredentials
		}
		return err
	}
	var verr error
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, verr = s.checkFactor(ctx, tx, accountID, code, code, true)
		if verr != nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM two_factor_recovery_codes WHERE account_id=$1`, accountID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM account_two_factor WHERE account_id=$1`, accountID)
		return err
	})
	if err != nil {
		return err
	}
	return verr
}

// SecondFactorResult says how a second factor was satisfied.
type SecondFactorResult struct {
	RecoveryCodeUsed  bool
	RecoveryCodesLeft int
}

// VerifySecondFactor checks a TOTP code or a recovery code for an enabled
// account, under the same rate limit as sign-in. For callers that verified
// the password themselves (Identity.VerifySecondFactor).
func (s *Service) VerifySecondFactor(ctx context.Context, accountID, code, recovery string) (*SecondFactorResult, error) {
	var verr error
	res := &SecondFactorResult{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		res.RecoveryCodeUsed, verr = s.checkFactor(ctx, tx, accountID, code, recovery, true)
		if verr != nil {
			return nil
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM two_factor_recovery_codes WHERE account_id=$1 AND used_at IS NULL`,
			accountID).Scan(&res.RecoveryCodesLeft)
	})
	if err != nil {
		return nil, err
	}
	if verr != nil {
		return nil, verr
	}
	return res, nil
}

// checkFactor is the one place a second factor is judged. It locks the
// account's row, applies the rate limit, accepts a TOTP code newer than the
// last accepted step (no replays) or, for an enrolled factor, an unused
// recovery code, and records the outcome. The caller commits either way, so
// failures are counted even though checkFactor returns an error. enrolled
// selects what is checked: true is the enabled factor (TOTP or recovery
// code), false the pending enrolment (TOTP only, EnableTwoFactor).
func (s *Service) checkFactor(ctx context.Context, tx pgx.Tx, accountID, code, recovery string, enrolled bool) (usedRecovery bool, err error) {
	var sealed []byte
	var enabledAt *time.Time
	var lastStep int64
	var failures int
	var since *time.Time
	err = tx.QueryRow(ctx, `SELECT secret_enc, enabled_at, last_step, failures, failures_since
		FROM account_two_factor WHERE account_id=$1 FOR UPDATE`, accountID).
		Scan(&sealed, &enabledAt, &lastStep, &failures, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		if enrolled {
			return false, ErrTwoFactorNotEnabled
		}
		return false, ErrTwoFactorNotPending
	}
	if err != nil {
		return false, err
	}
	if enrolled && enabledAt == nil {
		return false, ErrTwoFactorNotEnabled
	}
	if !enrolled && enabledAt != nil {
		return false, ErrTwoFactorEnabled
	}
	now := time.Now()
	if since != nil && now.Sub(*since) >= twoFactorLockWindow {
		failures, since = 0, nil
	}
	if failures >= twoFactorMaxFails {
		return false, ErrTwoFactorLocked
	}

	ok := false
	var step uint64
	// A secret that no longer opens (ACCOUNT_TOTP_KEY changed) still lets a
	// recovery code through; only a TOTP code is impossible then.
	secret, openErr := s.sealer.open(sealed, accountID)
	if openErr == nil {
		if st, match := totpMatch(secret, code, now); match && int64(st) > lastStep {
			ok, step = true, st
		}
	}
	if !ok && enrolled && recovery != "" {
		if rc := normalizeRecoveryCode(recovery); rc != "" {
			tag, err := tx.Exec(ctx, `UPDATE two_factor_recovery_codes SET used_at=now()
				WHERE account_id=$1 AND code_hash=$2 AND used_at IS NULL`, accountID, hashToken(rc, s.pepper))
			if err != nil {
				return false, err
			}
			ok = tag.RowsAffected() == 1
			usedRecovery = ok
		}
	}
	if !ok && openErr != nil {
		return false, errors.New("identity: two-factor secret unreadable (ACCOUNT_TOTP_KEY changed?)")
	}
	if !ok {
		if since == nil {
			t := now
			since = &t
		}
		if _, err := tx.Exec(ctx, `UPDATE account_two_factor SET failures=$2, failures_since=$3 WHERE account_id=$1`,
			accountID, failures+1, since); err != nil {
			return false, err
		}
		return false, ErrBadSecondFactor
	}
	if step == 0 {
		step = uint64(lastStep)
	}
	_, err = tx.Exec(ctx, `UPDATE account_two_factor SET failures=0, failures_since=NULL, last_step=$2 WHERE account_id=$1`,
		accountID, int64(step))
	return usedRecovery, err
}

// BeginLogin checks email and password. Without a second factor it creates
// the session as Login always did; with one it creates no session and returns
// a short-lived challenge for CompleteLogin instead.
func (s *Service) BeginLogin(ctx context.Context, email, password string) (sessionToken, challenge string, acct *Account, err error) {
	acct, err = s.VerifyCredentials(ctx, email, password)
	if err != nil {
		return "", "", nil, err
	}
	on, err := s.TwoFactorEnabled(ctx, acct.ID)
	if err != nil {
		return "", "", nil, err
	}
	if on {
		challenge, err = randomToken()
		if err != nil {
			return "", "", nil, err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO login_challenges (id, account_id, token_hash, expires_at)
			VALUES ($1,$2,$3,$4)`, newUUID(), acct.ID, hashToken(challenge, s.pepper), time.Now().Add(loginChallengeTTL)); err != nil {
			return "", "", nil, err
		}
		return "", challenge, acct, nil
	}
	sessionToken, err = s.createSession(ctx, acct.ID)
	if err != nil {
		return "", "", nil, err
	}
	return sessionToken, "", acct, nil
}

// CompleteLogin trades a challenge and a second factor for a session. A
// challenge is single-use, dies after 10 minutes or 5 wrong codes, and the
// account's own rate limit applies on top. ErrTokenInvalid for a dead
// challenge; ErrBadSecondFactor for a wrong code.
func (s *Service) CompleteLogin(ctx context.Context, challenge, code, recovery string) (string, *Account, error) {
	var accountID string
	var verr error
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var fails int
		err := tx.QueryRow(ctx, `SELECT c.id, c.account_id, c.failures FROM login_challenges c
			JOIN accounts a ON a.id = c.account_id
			WHERE c.token_hash=$1 AND c.used_at IS NULL AND c.expires_at > now() AND a.status='active'
			FOR UPDATE OF c`, hashToken(challenge, s.pepper)).Scan(&id, &accountID, &fails)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && fails >= challengeMaxFails) {
			verr = ErrTokenInvalid
			return nil
		}
		if err != nil {
			return err
		}
		_, verr = s.checkFactor(ctx, tx, accountID, code, recovery, true)
		if verr != nil {
			if errors.Is(verr, ErrBadSecondFactor) {
				_, err := tx.Exec(ctx, `UPDATE login_challenges SET failures=failures+1 WHERE id=$1`, id)
				return err
			}
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE login_challenges SET used_at=now() WHERE id=$1`, id)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	if verr != nil {
		return "", nil, verr
	}
	token, err := s.createSession(ctx, accountID)
	if err != nil {
		return "", nil, err
	}
	acct, err := s.GetByID(ctx, accountID)
	if err != nil {
		return "", nil, err
	}
	return token, acct, nil
}
