package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// SetAdapterCredential stores an adapter-domain secret (e.g. a console's
// transformed password hash) under the caller's namespace. Separate domain
// from web passwords (PRD FR-1/FR-3): never accepted as a web credential.
func (s *Service) SetAdapterCredential(ctx context.Context, namespace, accountID, secret string) error {
	if namespace == "" || accountID == "" || secret == "" {
		return errors.New("identity: namespace, account, and secret are required")
	}
	hash, err := hashPassword(s.pepper, secret)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO adapter_credentials (namespace, account_id, secret_hash)
		VALUES ($1,$2,$3) ON CONFLICT (namespace, account_id)
		DO UPDATE SET secret_hash = EXCLUDED.secret_hash, updated_at = now()`,
		strings.ToLower(namespace), accountID, string(hash))
	return err
}

// VerifyAdapterCredential checks an adapter-domain secret. Returns the
// account status outcome; never creates sessions.
func (s *Service) VerifyAdapterCredential(ctx context.Context, namespace, accountID, secret string) (*Account, error) {
	var hash string
	var status Status
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT c.secret_hash, a.status, a.email_verified_at
		FROM adapter_credentials c JOIN accounts a ON a.id = c.account_id
		WHERE c.namespace=$1 AND c.account_id=$2`, strings.ToLower(namespace), accountID).
		Scan(&hash, &status, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(bcryptInput(s.pepper, secret)))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := mapStatusErr(status); err != nil {
		return nil, err
	}
	if !comparePassword(hash, s.pepper, secret) {
		return nil, ErrBadCredentials
	}
	return &Account{ID: accountID, Status: status, EmailVerified: verifiedAt != nil}, nil
}
