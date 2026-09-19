// Package links implements the console-neutral adapter link lifecycle
// (PRD §7): pending → active → revoking → revoked, with idempotency keys.
package links

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound       = errors.New("links: not found")
	ErrConflict       = errors.New("links: conflicting claim or state")
	ErrNotPending     = errors.New("links: reservation not pending")
	ErrAccountUnknown = errors.New("links: account unknown")
	ErrNotActive      = errors.New("links: link not active")
	ErrInvalid        = errors.New("links: invalid public code")
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
	// PublicCode is the adapter-published human code (e.g. the Switch friend
	// code), display form; empty until the adapter publishes one.
	PublicCode string
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

	l, err := queryOne(ctx, tx, `SELECT id, account_id, namespace, subject_id, state, public_code
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
			// The published code goes with the link: a relink later must not
			// resurrect a code the adapter may have handed to someone else.
			if _, err := tx.Exec(ctx, `UPDATE adapter_links SET state='revoked', public_code='', public_code_key='', updated_at=now()
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
	return queryOne(ctx, s.pool, `SELECT id, account_id, namespace, subject_id, state, public_code
		FROM adapter_links WHERE namespace=$1 AND subject_id=$2`, namespace, subjectID)
}

// ListByAccount returns all links for an account.
func (s *Service) ListByAccount(ctx context.Context, accountID string) ([]*Link, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, account_id, namespace, subject_id, state, public_code
		FROM adapter_links WHERE account_id=$1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.AccountID, &l.Namespace, &l.SubjectID, &l.State, &l.PublicCode); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

// MaxPublicCode bounds a published code's display form.
const MaxPublicCode = 64

// CodeKey is the form public codes are compared on when the publisher did not
// declare one: letters and digits only, lower-cased. "SW-1234-5678-9012",
// "sw 1234 5678 9012" and "sw123456789012" share a key.
func CodeKey(code string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(code) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SetPublicCode publishes the adapter's human code on an active link, and
// replaces whatever was published before (a regenerated friend code retires
// the old one in the same statement). key is the adapter's canonical lookup
// form; empty means CodeKey(code). An empty code unpublishes. A code another
// active link in the namespace already holds is ErrConflict.
func (s *Service) SetPublicCode(ctx context.Context, linkID, code, key string) error {
	code = strings.TrimSpace(code)
	if len(code) > MaxPublicCode || strings.ContainsFunc(code, unicode.IsControl) {
		return ErrInvalid
	}
	if key = CodeKey(key); key == "" {
		key = CodeKey(code)
	}
	if code != "" && key == "" {
		return ErrInvalid
	}
	if code == "" {
		key = ""
	}
	tag, err := s.pool.Exec(ctx, `UPDATE adapter_links SET public_code=$2, public_code_key=$3, updated_at=now()
		WHERE id=$1 AND state='active'`, linkID, code, key)
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			return ErrConflict
		}
		if strings.Contains(err.Error(), "SQLSTATE 22P02") { // not a uuid
			return ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var state State
	err = s.pool.QueryRow(ctx, `SELECT state FROM adapter_links WHERE id=$1`, linkID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrNotActive
}

// GetByPublicCode resolves a code, as a person typed it, to the active link
// that published it. The typed form is compared by CodeKey against both the
// declared key and the display form, so "SW-1234-5678-9012" and, where the
// adapter declared the digits as its key, "123456789012" both resolve.
func (s *Service) GetByPublicCode(ctx context.Context, namespace, code string) (*Link, error) {
	k := CodeKey(code)
	if namespace == "" || k == "" {
		return nil, ErrNotFound
	}
	return queryOne(ctx, s.pool, `SELECT id, account_id, namespace, subject_id, state, public_code
		FROM adapter_links WHERE namespace=$1 AND state='active' AND public_code <> ''
		AND (public_code_key=$2 OR lower(regexp_replace(public_code, '[^0-9A-Za-z]', '', 'g'))=$2)
		ORDER BY (public_code_key=$2) DESC LIMIT 1`, namespace, k)
}

func queryOne(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, sql string, args ...any) (*Link, error) {
	var l Link
	err := q.QueryRow(ctx, sql, args...).Scan(&l.ID, &l.AccountID, &l.Namespace, &l.SubjectID, &l.State, &l.PublicCode)
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
