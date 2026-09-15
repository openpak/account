// Package links implements the console-neutral adapter link lifecycle
// (PRD §7): pending → active → revoking → revoked, with idempotency keys.
package links

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound       = errors.New("links: not found")
	ErrConflict       = errors.New("links: conflicting claim or state")
	ErrNotPending     = errors.New("links: reservation not pending")
	ErrAccountUnknown = errors.New("links: account unknown")
)

type State string

const (
	Pending  State = "pending"
	Active   State = "active"
	Revoking State = "revoking"
	Revoked  State = "revoked"
)

type Link struct {
	ID        string
	AccountID string
	Namespace string
	SubjectID string
	State     State
}

// Emitter records durable invalidation events inside the caller's
// transaction; implemented by events.Service. State changes and their
// events commit atomically (review P1 #4).
type Emitter interface {
	EmitTx(ctx context.Context, tx pgx.Tx, typ, accountID, namespace, subjectID string) error
}

type Service struct {
	pool    *pgxpool.Pool
	emitter Emitter
}

func New(pool *pgxpool.Pool, emitter Emitter) *Service { return &Service{pool: pool, emitter: emitter} }

// Reserve claims a subject for an account. Idempotent by
// (account, namespace, subject, key); a claim by a different account or an
// already-active link conflicts.
func (s *Service) Reserve(ctx context.Context, accountID, namespace, subjectID, idempotencyKey string) (*Link, error) {
	if accountID == "" || namespace == "" || subjectID == "" {
		return nil, errors.New("links: account, namespace, and subject are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var accountExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND status='active')`,
		accountID).Scan(&accountExists); err != nil {
		return nil, err
	}
	if !accountExists {
		return nil, ErrAccountUnknown
	}

	l, err := queryOne(ctx, tx, `SELECT id, account_id, namespace, subject_id, state
		FROM adapter_links WHERE namespace=$1 AND subject_id=$2 FOR UPDATE`, namespace, subjectID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound):
		// No claim yet — insert. (queryOne translates pgx.ErrNoRows to
		// ErrNotFound for callers that probe; this is the caller that creates.)
		l = &Link{ID: newUUID(), AccountID: accountID, Namespace: namespace, SubjectID: subjectID, State: Pending}
		if _, err := tx.Exec(ctx, `INSERT INTO adapter_links (id, account_id, namespace, subject_id, state, idempotency_key)
			VALUES ($1,$2,$3,$4,$5,$6)`, l.ID, l.AccountID, l.Namespace, l.SubjectID, string(Pending), idempotencyKey); err != nil {
			return nil, fmt.Errorf("links: reserve: %w", err)
		}
	case err != nil:
		return nil, err
	case l.AccountID != accountID:
		// A revoked subject may not silently migrate to another account
		// (PRD FR-2: relinking/transfer policy — transfer is explicit, not
		// a side effect of a second reserve).
		return nil, ErrConflict
	case l.State == Revoking:
		return nil, ErrConflict
	case l.State == Revoked:
		// Same-account relink: return the link to pending (explicit claim by
		// the same owner only).
		tag, err := tx.Exec(ctx, `UPDATE adapter_links SET state='pending', idempotency_key=$3, updated_at=now()
			WHERE id=$1 AND state='revoked' AND account_id=$2`, l.ID, accountID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrConflict
		}
		l.State = Pending
		return l, tx.Commit(ctx)
	default:
		// pending or active for the same account: idempotent no-op
		return l, tx.Commit(ctx)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// Activate promotes a pending reservation to active. Idempotent when already
// active for the same link.
func (s *Service) Activate(ctx context.Context, linkID, idempotencyKey string) (State, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE adapter_links SET state='active', idempotency_key=$2, updated_at=now()
		WHERE id=$1 AND state='pending'`, linkID, idempotencyKey)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return Active, nil
	}
	// Either already active (idempotent) or in a non-pending state.
	var state State
	err = s.pool.QueryRow(ctx, `SELECT state FROM adapter_links WHERE id=$1`, linkID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state == Active {
		return Active, nil
	}
	return "", ErrNotPending
}

// Unlink moves an active/pending link to revoked. Idempotent when already
// revoked. The state change and its link_unlinked event commit in one
// transaction: an emitter failure rolls the whole unlink back, so a retry
// re-attempts both (review P1 #4). The adapter is responsible for revoking
// its own credentials before/after calling this (see PRD §7).
func (s *Service) Unlink(ctx context.Context, linkID, idempotencyKey string) (State, error) {
	var result State
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE adapter_links SET state='revoking', updated_at=now(), idempotency_key=$2
			WHERE id=$1 AND state IN ('pending','active')`, linkID, idempotencyKey)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			var accountID, namespace, subjectID string
			if err := tx.QueryRow(ctx, `SELECT account_id, namespace, subject_id FROM adapter_links WHERE id=$1`, linkID).
				Scan(&accountID, &namespace, &subjectID); err != nil {
				return err
			}
			if s.emitter != nil {
				if err := s.emitter.EmitTx(ctx, tx, "link_unlinked", accountID, namespace, subjectID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE adapter_links SET state='revoked', updated_at=now()
				WHERE id=$1 AND state='revoking'`, linkID); err != nil {
				return err
			}
			result = Revoked
			return nil
		}
		// Already transitioned: distinguish revoked (idempotent) from conflict.
		var state State
		err = tx.QueryRow(ctx, `SELECT state FROM adapter_links WHERE id=$1`, linkID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == Revoked {
			result = Revoked
			return nil
		}
		return ErrConflict
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// GetBySubject returns the link for a namespace/subject, regardless of state.
func (s *Service) GetBySubject(ctx context.Context, namespace, subjectID string) (*Link, error) {
	return queryOne(ctx, s.pool, `SELECT id, account_id, namespace, subject_id, state
		FROM adapter_links WHERE namespace=$1 AND subject_id=$2`, namespace, subjectID)
}

// ListByAccount returns all links for an account.
func (s *Service) ListByAccount(ctx context.Context, accountID string) ([]*Link, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, account_id, namespace, subject_id, state
		FROM adapter_links WHERE account_id=$1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.AccountID, &l.Namespace, &l.SubjectID, &l.State); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func queryOne(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, sql string, args ...any) (*Link, error) {
	var l Link
	err := q.QueryRow(ctx, sql, args...).Scan(&l.ID, &l.AccountID, &l.Namespace, &l.SubjectID, &l.State)
	if err != nil {
		// A subject that simply has no link yet is a lookup miss, not an
		// internal error: adapters probe for subjects that may not exist (a
		// battlenet link before first login, a friend whose account went
		// away), and leaking pgx.ErrNoRows mapped those to 500s.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &l, nil
}
