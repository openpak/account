package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSameEmail: the requested address is the one the account already has.
var ErrSameEmail = errors.New("identity: that is already the account's address")

// EmailChange is what RequestEmailChange hands the caller to mail: the token
// goes to NewEmail, a notice to OldEmail. Token is "" when nothing is to be
// sent (address taken, or throttled); callers answer identically.
type EmailChange struct {
	Token    string
	OldEmail string
	NewEmail string
}

// RequestEmailChange checks the password and, when the new address is free
// and the account has not asked in the last 10 minutes, issues a single-use
// token bound to that address. Nothing about the account changes until the
// token comes back through ConfirmEmailChange. Whether the address is taken
// is never reported: the result looks the same.
func (s *Service) RequestEmailChange(ctx context.Context, accountID, password, newEmail string) (*EmailChange, error) {
	if _, err := s.VerifyByID(ctx, accountID, password); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrBadCredentials
		}
		return nil, err
	}
	next := strings.TrimSpace(newEmail)
	normalized := normalizeEmail(next)
	if normalized == "" || len(normalized) > maxLoginEmail || !strings.Contains(normalized, "@") ||
		strings.ContainsAny(normalized, " \r\n\t") {
		return nil, errors.New("identity: invalid email")
	}
	var current, currentNorm string
	if err := s.pool.QueryRow(ctx, `SELECT email, email_normalized FROM accounts WHERE id=$1`, accountID).
		Scan(&current, &currentNorm); err != nil {
		return nil, err
	}
	if normalized == currentNorm {
		return nil, ErrSameEmail
	}
	out := &EmailChange{OldEmail: current, NewEmail: next}
	var taken, recent bool
	if err := s.pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM accounts WHERE email_normalized=$2),
		EXISTS(SELECT 1 FROM lifecycle_tokens WHERE account_id=$1 AND purpose='email_change'
			AND created_at > now() - interval '10 minutes')`, accountID, normalized).Scan(&taken, &recent); err != nil {
		return nil, err
	}
	if taken || recent {
		return out, nil
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Only the newest request's link works.
		if _, err := tx.Exec(ctx, `UPDATE lifecycle_tokens SET used_at=now()
			WHERE account_id=$1 AND purpose='email_change' AND used_at IS NULL`, accountID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO lifecycle_tokens (id, account_id, purpose, token_hash, expires_at, new_email)
			VALUES ($1,$2,'email_change',$3,$4,$5)`, newUUID(), accountID, hashToken(token, s.pepper),
			time.Now().Add(tokenTTL), next)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.Token = token
	return out, nil
}

// ConfirmEmailChange applies the address a change token was issued for. The
// address counts as verified (the link was mailed to it). Outstanding
// password-reset, verification and change links are voided, since they were
// mailed to the old address, and email_changed is emitted in the same
// transaction. The account id is unchanged, so sessions and links stay valid.
// An address claimed by another account in the meantime is ErrTokenInvalid,
// like any other dead link.
func (s *Service) ConfirmEmailChange(ctx context.Context, token string) (accountID string, err error) {
	if token == "" {
		return "", ErrTokenInvalid
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var next string
		err := tx.QueryRow(ctx, `UPDATE lifecycle_tokens SET used_at = now()
			WHERE token_hash = $1 AND purpose = 'email_change' AND used_at IS NULL AND expires_at > now()
			RETURNING account_id, new_email`, hashToken(token, s.pepper)).Scan(&accountID, &next)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE accounts SET email=$2, email_normalized=$3, email_verified_at=now(), updated_at=now()
			WHERE id=$1 AND status='active'`, accountID, next, normalizeEmail(next))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrTokenInvalid
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTokenInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE lifecycle_tokens SET used_at=now()
			WHERE account_id=$1 AND used_at IS NULL AND purpose IN ('password_reset','email_verify','email_change')`,
			accountID); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "email_changed", accountID, "", "")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return accountID, nil
}
