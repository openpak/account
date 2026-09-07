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

// RedeliveryAfter is the visibility window for at-least-once delivery: a
// polled-but-unacknowledged invitation becomes deliverable again after this
// long. Var (not const) so integration tests can shorten it.
var RedeliveryAfter = 60 * time.Second

// Checker is the social enforcement surface (implemented by social.Service).
// The Tx variant runs the same policy inside a caller-provided transaction so
// Poll never checks out a second pool connection while holding one.
type Checker interface {
	CheckInteraction(ctx context.Context, actorID, targetID string) (allowed bool, reason string, err error)
	CheckInteractionTx(ctx context.Context, tx pgx.Tx, actorID, targetID string) (allowed bool, reason string, err error)
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
	ID              string
	FromAccountID   string
	FromDisplayName string
	TitleID         string
	Payload         []byte
	Nonce           string
	ExpiresAtUnix   int64
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
	allowed, reason, err := s.check.CheckInteraction(ctx, fromID, toID)
	if err != nil {
		if reason == "self" {
			return nil, ErrSelfInvite
		}
		return nil, err
	} else if !allowed {
		return nil, ErrDenied
	}
	sess, err := s.resolve.Resolve(ctx, toID, titleID)
	if err != nil {
		// Any resolver failure means there is no deliverable live session.
		return nil, ErrNoLiveSession
	}
	in := &Invitation{
		FromAccountID: fromID,
		TitleID:       titleID,
		Payload:       payload,
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO invitations
			(id, from_id, to_id, namespace, title_id, session_id, payload, nonce, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, gen_random_uuid(), now() + make_interval(secs => $7))
		RETURNING id, nonce::text, extract(epoch FROM expires_at)::bigint`,
		fromID, toID, sess.Namespace, titleID, sess.ID, payload, int(ttl.Seconds())).
		Scan(&in.ID, &in.Nonce, &in.ExpiresAtUnix)
	if err != nil {
		return nil, fmt.Errorf("invitations: send: %w", err)
	}
	return in, nil
}

// Poll returns pending invitations for (namespace, account) with
// at-least-once semantics: eligibility and session liveness are revalidated
// at delivery time; returned rows stay pending inside a redelivery window
// until AckInvitations confirms receipt.
func (s *Service) Poll(ctx context.Context, namespace, accountID string, limit int32) ([]*Invitation, error) {
	if namespace == "" || accountID == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	// Reap by TTL...
	if _, err := s.pool.Exec(ctx, `UPDATE invitations SET state='expired'
		WHERE to_id=$1 AND namespace=$2 AND state='pending' AND expires_at <= now()`,
		accountID, namespace); err != nil {
		return nil, err
	}
	// ...and by loss of the bound live session.
	if _, err := s.pool.Exec(ctx, `UPDATE invitations i SET state='expired'
		WHERE i.to_id=$1 AND i.namespace=$2 AND i.state='pending'
		AND NOT EXISTS (SELECT 1 FROM adapter_sessions s
			WHERE s.session_id = i.session_id AND s.expires_at > now())`,
		accountID, namespace); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT i.id, i.from_id, acc.display_name, i.title_id,
			i.payload, i.nonce::text, extract(epoch FROM i.expires_at)::bigint
		FROM invitations i
		JOIN accounts acc ON acc.id = i.from_id
		WHERE i.to_id=$1 AND i.namespace=$2 AND i.state='pending'
			AND (i.delivery_started_at IS NULL
				OR i.delivery_started_at < now() - make_interval(secs => $3))
		ORDER BY i.created_at LIMIT $4
		FOR UPDATE OF i SKIP LOCKED`,
		accountID, namespace, int(RedeliveryAfter.Seconds()), limit)
	if err != nil {
		return nil, err
	}
	var candidates []*Invitation
	for rows.Next() {
		in := &Invitation{}
		if err := rows.Scan(&in.ID, &in.FromAccountID, &in.FromDisplayName, &in.TitleID,
			&in.Payload, &in.Nonce, &in.ExpiresAtUnix); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, in)
	}
	// Fully drain the SELECT before issuing further statements on the same
	// transaction connection (pgx forbids interleaved queries).
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var deliver []*Invitation
	var reject []string
	for _, in := range candidates {
		// Delivery-time policy revalidation: a queued invitation must not
		// survive a block, unfriend, or account deactivation. Runs through
		// the held transaction, not the pool.
		allowed, _, err := s.check.CheckInteractionTx(ctx, tx, in.FromAccountID, accountID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			reject = append(reject, in.ID)
			continue
		}
		deliver = append(deliver, in)
	}
	if len(reject) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE invitations SET state='rejected'
			WHERE id = ANY($1) AND state='pending'`, reject); err != nil {
			return nil, err
		}
	}
	// Mark the redelivery window; state stays pending until acknowledged.
	if len(deliver) > 0 {
		ids := make([]string, len(deliver))
		for i, in := range deliver {
			ids[i] = in.ID
		}
		if _, err := tx.Exec(ctx, `UPDATE invitations SET delivery_started_at=now()
			WHERE id = ANY($1) AND state='pending'`, ids); err != nil {
			return nil, err
		}
	}
	return deliver, tx.Commit(ctx)
}

// Ack marks (invitation, nonce) pairs delivered. Only pending rows bound to
// the given namespace/account/nonce are accepted; everything else is ignored.
func (s *Service) Ack(ctx context.Context, namespace, accountID string, ids, nonces []string) (int, error) {
	if namespace == "" || accountID == "" || len(ids) != len(nonces) {
		return 0, ErrInvalid
	}
	acked := 0
	xb, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer xb.Rollback(ctx)
	for i, id := range ids {
		tag, err := xb.Exec(ctx, `UPDATE invitations SET state='delivered', delivered_at=now()
			WHERE id=$1 AND nonce=$2::uuid AND namespace=$3 AND to_id=$4 AND state='pending'`,
			id, nonces[i], namespace, accountID)
		if err != nil {
			return 0, err
		}
		acked += int(tag.RowsAffected())
	}
	return acked, xb.Commit(ctx)
}

// ErrNotFound is returned by Send when callers need to probe session state
// indirectly; the resolver returns its own sentinel otherwise.
var ErrNotFound = errors.New("invitations: not found")
