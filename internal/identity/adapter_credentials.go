package identity

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	_, err = s.pool.Exec(ctx, `INSERT INTO adapter_credentials (namespace, account_id, secret_hash, pepper_gen)
		VALUES ($1,$2,$3,$4) ON CONFLICT (namespace, account_id)
		DO UPDATE SET secret_hash = EXCLUDED.secret_hash, pepper_gen = EXCLUDED.pepper_gen, updated_at = now()`,
		strings.ToLower(namespace), accountID, string(hash), s.pepperGen)
	return err
}

// VerifyAdapterCredential checks an adapter-domain secret. Returns the
// account status outcome; never creates sessions.
func (s *Service) VerifyAdapterCredential(ctx context.Context, namespace, accountID, secret string) (*Account, error) {
	var hash string
	var gen int16
	var status Status
	var verifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT c.secret_hash, c.pepper_gen, a.status, a.email_verified_at
		FROM adapter_credentials c JOIN accounts a ON a.id = c.account_id
		WHERE c.namespace=$1 AND c.account_id=$2`, strings.ToLower(namespace), accountID).
		Scan(&hash, &gen, &status, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		s.dummyCompare(secret)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := mapStatusErr(status); err != nil {
		return nil, err
	}
	ok, rehash := s.checkPassword(hash, gen, secret)
	if !ok {
		return nil, ErrBadCredentials
	}
	if rehash != "" {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE adapter_credentials SET secret_hash=$4, pepper_gen=$5
				WHERE namespace=$1 AND account_id=$2 AND secret_hash=$3`, strings.ToLower(namespace), accountID, hash, rehash, s.pepperGen)
			return err
		})
		if err != nil {
			log.Printf("identity: re-hashing an adapter credential under the new pepper failed: %v", err)
		}
	}
	return &Account{ID: accountID, Status: status, EmailVerified: verifiedAt != nil}, nil
}
