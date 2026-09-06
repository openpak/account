// Package social implements the canonical friend graph and interaction
// enforcement (PRD SW-05): the core owns relationships and blocks; adapters
// receive projections and answers, never another authoritative store.
package social

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid        = errors.New("social: invalid request")
	ErrAccountUnknown = errors.New("social: account unknown or inactive")
	ErrSelfRequest    = errors.New("social: self-interaction not permitted")
	ErrBlocked        = errors.New("social: blocked")
	ErrNotPending     = errors.New("social: request not pending")
	ErrReversePending = errors.New("social: addressee already requested")
)

type State string

const (
	Pending  State = "pending"
	Accepted State = "accepted"
	Removed  State = "removed"
)

type Friendship struct {
	ID     string
	State  State
	Other  string // the other account's ID
	WithID string
}

type Friend struct {
	AccountID   string
	DisplayName string
}

// Relationship is the viewpoint of account on other.
type Relationship int

const (
	RelNone Relationship = iota
	RelFriends
	RelPendingIncoming
	RelPendingOutgoing
	RelBlockedBySelf
	RelBlockedByOther
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// RequestFriend creates a pending friendship request. Idempotent while
// pending or accepted for the same pair and direction.
func (s *Service) RequestFriend(ctx context.Context, requesterID, addresseeID, idempotencyKey string) (*Friendship, error) {
	if requesterID == "" || addresseeID == "" {
		return nil, ErrInvalid
	}
	if requesterID == addresseeID {
		return nil, ErrSelfRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := requireActivePair(ctx, tx, requesterID, addresseeID); err != nil {
		return nil, err
	}
	if err := requireNoBlock(ctx, tx, requesterID, addresseeID); err != nil {
		return nil, err
	}

	// Same-direction row wins; a reverse pending request is surfaced so the
	// adapter can offer accept instead of a duplicate request.
	var id string
	var state State
	err = tx.QueryRow(ctx, `SELECT id, state FROM friendships
		WHERE requester_id=$1 AND addressee_id=$2 AND state IN ('pending','accepted')
		FOR UPDATE`, requesterID, addresseeID).Scan(&id, &state)
	switch {
	case err == nil:
		return &Friendship{ID: id, State: state, WithID: addresseeID}, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT true FROM friendships
		WHERE requester_id=$1 AND addressee_id=$2 AND state='pending'`, addresseeID, requesterID).Scan(new(bool))
	if err == nil {
		return nil, ErrReversePending
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	f := &Friendship{ID: newUUID(), State: Pending, WithID: addresseeID}
	tag, err := tx.Exec(ctx, `INSERT INTO friendships (id, requester_id, addressee_id, state, idempotency_key)
		VALUES ($1,$2,$3,'pending',$4)
		ON CONFLICT (requester_id, addressee_id) DO UPDATE
		SET state='pending', idempotency_key=EXCLUDED.idempotency_key, updated_at=now()
		WHERE friendships.state='removed'`, f.ID, requesterID, addresseeID, idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("social: request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrNotPending // a removed row could not be re-opened
	}
	return f, tx.Commit(ctx)
}

// AcceptFriend accepts a pending incoming request. Idempotent when already
// accepted.
func (s *Service) AcceptFriend(ctx context.Context, addresseeID, requesterID, _ string) (State, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE friendships SET state='accepted', updated_at=now()
		WHERE requester_id=$1 AND addressee_id=$2 AND state='pending'`, requesterID, addresseeID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return Accepted, nil
	}
	var state State
	err = s.pool.QueryRow(ctx, `SELECT state FROM friendships
		WHERE requester_id=$1 AND addressee_id=$2`, requesterID, addresseeID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state == Accepted {
		return Accepted, nil
	}
	return "", ErrNotPending
}

var ErrNotFound = errors.New("social: not found")

// RemoveFriend ends a pending or accepted friendship from either side.
// Idempotent when absent or already removed.
func (s *Service) RemoveFriend(ctx context.Context, accountID, otherID string) error {
	if accountID == "" || otherID == "" {
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `UPDATE friendships SET state='removed', updated_at=now()
		WHERE state IN ('pending','accepted')
		AND ((requester_id=$1 AND addressee_id=$2) OR (requester_id=$2 AND addressee_id=$1))`,
		accountID, otherID)
	return err
}

// ListFriends returns accepted friends with minimal profile projection.
func (s *Service) ListFriends(ctx context.Context, accountID string) ([]Friend, error) {
	rows, err := s.pool.Query(ctx, `SELECT other, acc.display_name FROM (
		SELECT CASE WHEN requester_id=$1 THEN addressee_id ELSE requester_id END AS other
		FROM friendships WHERE state='accepted' AND (requester_id=$1 OR addressee_id=$1)
	) fr JOIN accounts acc ON acc.id = fr.other ORDER BY acc.display_name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Friend
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.AccountID, &f.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListIncomingRequests returns accounts with a pending request to accountID.
func (s *Service) ListIncomingRequests(ctx context.Context, accountID string) ([]Friend, error) {
	rows, err := s.pool.Query(ctx, `SELECT f.requester_id, acc.display_name
		FROM friendships f JOIN accounts acc ON acc.id = f.requester_id
		WHERE f.addressee_id=$1 AND f.state='pending' ORDER BY f.created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Friend
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.AccountID, &f.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetRelationship returns account's relationship viewpoint on other. Blocks
// take precedence over friendship rows.
func (s *Service) GetRelationship(ctx context.Context, accountID, otherID string) (Relationship, error) {
	var bSelf, bOther bool
	if err := s.pool.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM account_blocks WHERE blocker_id=$1 AND blocked_id=$2),
			EXISTS(SELECT 1 FROM account_blocks WHERE blocker_id=$2 AND blocked_id=$1)`,
		accountID, otherID).Scan(&bSelf, &bOther); err != nil {
		return RelNone, err
	}
	if bSelf {
		return RelBlockedBySelf, nil
	}
	if bOther {
		return RelBlockedByOther, nil
	}
	var state State
	err := s.pool.QueryRow(ctx, `SELECT state FROM friendships
		WHERE (requester_id=$1 AND addressee_id=$2) OR (requester_id=$2 AND addressee_id=$1)`,
		accountID, otherID).Scan(&state)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return RelNone, nil
	case err != nil:
		return RelNone, err
	case state == Accepted:
		return RelFriends, nil
	case state == Pending:
		// Direction: pending row with requester=account is outgoing.
		var outgoing bool
		if err := s.pool.QueryRow(ctx, `SELECT requester_id=$1 FROM friendships
			WHERE (requester_id=$1 AND addressee_id=$2) OR (requester_id=$2 AND addressee_id=$1)`,
			accountID, otherID).Scan(&outgoing); err != nil {
			return RelNone, err
		}
		if outgoing {
			return RelPendingOutgoing, nil
		}
		return RelPendingIncoming, nil
	}
	return RelNone, nil
}

// CheckInteraction answers whether actor may interact with target. v1 rule:
// every kind requires an accepted friendship, no block in either direction,
// and both accounts active (friends-only discovery and invitations match the
// first-wave titles; privacy broadening is future work — SP-5).
func (s *Service) CheckInteraction(ctx context.Context, actorID, targetID string) (bool, string, error) {
	if actorID == "" || targetID == "" {
		return false, "invalid", ErrInvalid
	}
	if actorID == targetID {
		return false, "self", ErrSelfRequest
	}
	actorOK, err := accountActive(ctx, s.pool, actorID)
	if err != nil {
		return false, "", err
	}
	targetOK, err := accountActive(ctx, s.pool, targetID)
	if err != nil {
		return false, "", err
	}
	if !actorOK || !targetOK {
		return false, "account_inactive", nil
	}
	var blocked bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_blocks
			WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`,
		actorID, targetID).Scan(&blocked); err != nil {
		return false, "", err
	}
	if blocked {
		return false, "blocked", nil
	}
	var friends bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM friendships
			WHERE state='accepted'
			AND ((requester_id=$1 AND addressee_id=$2) OR (requester_id=$2 AND addressee_id=$1)))`,
		actorID, targetID).Scan(&friends); err != nil {
		return false, "", err
	}
	if !friends {
		return false, "not_friends", nil
	}
	return true, "", nil
}

// Block records a directional block. Idempotent.
func (s *Service) Block(ctx context.Context, blockerID, blockedID string) error {
	if blockerID == "" || blockedID == "" {
		return ErrInvalid
	}
	if blockerID == blockedID {
		return ErrSelfRequest
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO account_blocks (blocker_id, blocked_id)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`, blockerID, blockedID); err != nil {
		return err
	}
	return nil
}

// Unblock removes a directional block. Idempotent.
func (s *Service) Unblock(ctx context.Context, blockerID, blockedID string) error {
	if blockerID == "" || blockedID == "" {
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM account_blocks WHERE blocker_id=$1 AND blocked_id=$2`,
		blockerID, blockedID)
	return err
}

func requireActivePair(ctx context.Context, tx pgx.Tx, a, b string) error {
	for _, id := range []string{a, b} {
		ok, err := accountActive(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAccountUnknown
		}
	}
	return nil
}

func requireNoBlock(ctx context.Context, tx pgx.Tx, a, b string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_blocks
			WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`,
		a, b).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrBlocked
	}
	return nil
}

func accountActive(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND status='active')`, id).Scan(&ok)
	return ok, err
}
