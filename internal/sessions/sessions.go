// Package sessions implements the live-adapter-session registry (PRD SW-07):
// short core-owned leases give stale-session cleanup a shared clock. One
// live session per (account, title): a new registration replaces the old.
package sessions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid        = errors.New("sessions: invalid request")
	ErrAccountUnknown = errors.New("sessions: account unknown or inactive")
	ErrNotFound       = errors.New("sessions: session not found or expired")
)

const (
	DefaultLease = 30 * time.Second
	MaxLease     = 5 * time.Minute
)

type Session struct {
	ID          string
	AccountID   string
	Namespace   string
	TitleID     string
	EndpointRef string
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Register inserts a live session, replacing any previous session for the
// same (account, title). Returns the server-assigned session ID and the
// applied lease.
func (s *Service) Register(ctx context.Context, accountID, namespace, titleID, endpointRef string, lease time.Duration) (string, time.Duration, error) {
	if accountID == "" || namespace == "" || titleID == "" || endpointRef == "" {
		return "", 0, ErrInvalid
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	var active bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND status='active')`,
		accountID).Scan(&active); err != nil {
		return "", 0, err
	}
	if !active {
		return "", 0, ErrAccountUnknown
	}
	id := newUUID()
	// Replace semantics: one live session per (account, title).
	if _, err := s.pool.Exec(ctx, `DELETE FROM adapter_sessions
		WHERE account_id=$1 AND title_id=$2`, accountID, titleID); err != nil {
		return "", 0, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO adapter_sessions
		(session_id, account_id, namespace, title_id, endpoint_ref, expires_at)
		VALUES ($1,$2,$3,$4,$5, now() + make_interval(secs => $6))`,
		id, accountID, namespace, titleID, endpointRef, int(lease.Seconds())); err != nil {
		return "", 0, fmt.Errorf("sessions: register: %w", err)
	}
	return id, lease, nil
}

// Heartbeat renews the lease. Expired or unknown sessions are not revived.
// Sessions of inactive (banned/deleted/pending) accounts are rejected and
// invalidated on touch (review P1 #5): a ban must not leave a renewable
// lease behind.
func (s *Service) Heartbeat(ctx context.Context, sessionID string, lease time.Duration) (time.Duration, error) {
	if sessionID == "" {
		return 0, ErrInvalid
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	tag, err := s.pool.Exec(ctx, `UPDATE adapter_sessions s
		SET expires_at = now() + make_interval(secs => $2), updated_at = now()
		FROM accounts a
		WHERE s.session_id=$1 AND s.expires_at > now()
		  AND a.id = s.account_id AND a.status = 'active'`, sessionID, int(lease.Seconds()))
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 1 {
		return lease, nil
	}
	// Distinguish: unknown/expired vs. inactive owner (invalidate on touch).
	tag, err = s.pool.Exec(ctx, `DELETE FROM adapter_sessions s
		USING accounts a
		WHERE s.session_id=$1 AND a.id = s.account_id AND a.status <> 'active'`, sessionID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 1 {
		return 0, ErrAccountUnknown
	}
	return 0, ErrNotFound
}

// Expire removes a session (explicit teardown). Idempotent.
func (s *Service) Expire(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM adapter_sessions WHERE session_id=$1`, sessionID)
	return err
}

// Resolve returns the live session for (account, title), if any. Expired
// rows for the pair are reaped on read. Sessions of inactive accounts are
// invalidated and never resolved (review P1 #5).
func (s *Service) Resolve(ctx context.Context, accountID, titleID string) (*Session, error) {
	if accountID == "" || titleID == "" {
		return nil, ErrInvalid
	}
	sess := &Session{}
	err := s.pool.QueryRow(ctx, `SELECT s.session_id, s.account_id, s.namespace, s.title_id, s.endpoint_ref
		FROM adapter_sessions s JOIN accounts a ON a.id = s.account_id
		WHERE s.account_id=$1 AND s.title_id=$2 AND s.expires_at > now() AND a.status = 'active'`,
		accountID, titleID).Scan(&sess.ID, &sess.AccountID, &sess.Namespace, &sess.TitleID, &sess.EndpointRef)
	if errors.Is(err, pgx.ErrNoRows) {
		// Reap expired rows and invalidate rows of inactive owners.
		_, _ = s.pool.Exec(ctx, `DELETE FROM adapter_sessions s USING accounts a
			WHERE s.account_id=$1 AND s.title_id=$2
			  AND (s.expires_at <= now() OR a.id = s.account_id AND a.status <> 'active')`,
			accountID, titleID)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 36)
	pos := map[int]bool{8: true, 13: true, 18: true, 23: true}
	j := 0
	for i := 0; i < 36; i++ {
		if pos[i] {
			out[i] = '-'
			continue
		}
		out[i] = hexDigits[b[j>>1]>>(4-(j&1)*4)&0x0f]
		j++
	}
	return string(out)
}
