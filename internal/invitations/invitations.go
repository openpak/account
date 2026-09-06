// Package invitations implements core-routed invitations (PRD SW-06):
// eligibility enforcement, expiry, nonce-based replay rejection, and
// delivery through a live adapter session.
package invitations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid       = errors.New("invitations: invalid request")
	ErrSelfInvite    = errors.New("invitations: self-invite not permitted")
	ErrDenied        = errors.New("invitations: interaction denied by policy")
	ErrNoLiveSession = errors.New("invitations: no live session for target account and title")
)

const (
	MaxPayload = 4096
	MaxTTL     = 24 * time.Hour
)

// Checker is the social enforcement surface (implemented by social.Service).
type Checker interface {
	CheckInteraction(ctx context.Context, actorID, targetID string) (allowed bool, reason string, err error)
}

// Resolver is the live-session lookup surface (implemented by sessions.Service).
type Resolver interface {
	Resolve(ctx context.Context, accountID, titleID string) (*SessionRef, error)
}

// SessionRef is the minimal session projection invitations need.
type SessionRef struct {
	ID        string
	Namespace string
}

type Invitation struct {
	ID             string
	FromAccountID  string
	FromDisplayName string
	TitleID        string
	Payload        []byte
	Nonce          string
	ExpiresAtUnix  int64
}

type Service struct {
	pool    *pgxpool.Pool
	check   Checker
	resolve Resolver
}

func New(pool *pgxpool.Pool, check Checker, resolve Resolver) *Service {
	return &Service{pool: pool, check: check, resolve: resolve}
}

// Send validates eligibility, requires a live session for the target, and
// stores the invitation for the session's adapter namespace.
func (s *Service) Send(ctx context.Context, fromID, toID, titleID string, payload []byte, ttl time.Duration) (*Invitation, error) {
	if fromID == "" || toID == "" || titleID == "" {
		return nil, ErrInvalid
	}
	if fromID == toID {
		return nil, ErrSelfInvite
	}
	if len(payload) == 0 || len(payload) > MaxPayload {
		return nil, ErrInvalid
	}
	if ttl <= 0 || ttl > MaxTTL {
		return nil, ErrInvalid
	}
	if allowed, _, err := s.check.CheckInteraction(ctx, fromID, toID); err != nil {
		if errors.Is(err, ErrSelfInvite) {
			return nil, ErrSelfInvite
		}
		return nil, err
	} else if !allowed {
		return nil, ErrDenied
	}
	sess, err := s.resolve.Resolve(ctx, toID, titleID)
	if err != nil {
		if errors.Is(err, ErrNoLiveSession) || errors.Is(err, ErrNotFound) {
			return nil, ErrNoLiveSession
		}
		return nil, err
	}
	in := &Invitation{
		FromAccountID: fromID,
		TitleID:       titleID,
		Payload:       payload,
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO invitations
			(id, from_id, to_id, namespace, title_id, payload, nonce, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, gen_random_uuid(), now() + make_interval(secs => $6))
		RETURNING id, nonce::text, extract(epoch FROM expires_at)::bigint`,
		fromID, toID, sess.Namespace, titleID, payload, int(ttl.Seconds())).
		Scan(&in.ID, &in.Nonce, &in.ExpiresAtUnix)
	if err != nil {
		return nil, fmt.Errorf("invitations: send: %w", err)
	}
	return in, nil
}

// Poll returns and marks delivered the pending, unexpired invitations for
// (namespace, account). Expired rows are reaped. At-least-once semantics
// apply only before marking; callers apply idempotently.
func (s *Service) Poll(ctx context.Context, namespace, accountID string, limit int32) ([]*Invitation, error) {
	if namespace == "" || accountID == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	_, err := s.pool.Exec(ctx, `UPDATE invitations SET state='expired'
		WHERE to_id=$1 AND namespace=$2 AND state='pending' AND expires_at <= now()`,
		accountID, namespace)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT i.id, i.from_id, COALESCE(acc.display_name,''), i.title_id,
			i.payload, i.nonce::text, extract(epoch FROM i.expires_at)::bigint
		FROM invitations i JOIN accounts acc ON acc.id = i.from_id
		WHERE i.to_id=$1 AND i.namespace=$2 AND i.state='pending'
		ORDER BY i.created_at LIMIT $3 FOR UPDATE OF i SKIP LOCKED`,
		accountID, namespace, limit)
	if err != nil {
		return nil, err
	}
	var picked []string
	var out []*Invitation
	for rows.Next() {
		in := &Invitation{}
		if err := rows.Scan(&in.ID, &in.FromAccountID, &in.FromDisplayName, &in.TitleID,
			&in.Payload, &in.Nonce, &in.ExpiresAtUnix); err != nil {
			rows.Close()
			return nil, err
		}
		picked = append(picked, in.ID)
		out = append(out, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(picked) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE invitations SET state='delivered', delivered_at=now()
			WHERE id = ANY($1)`, picked); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit(ctx)
}

var ErrNotFound = errors.New("invitations: not found")
var _ = pgx.ErrNoRows // keep pgx import stable across builds
