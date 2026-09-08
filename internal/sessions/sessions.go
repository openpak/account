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
	// Atomic replacement: the unique (account_id, title_id) key backs an
	// upsert, so concurrent registrations converge on exactly one live row.
	if _, err := s.pool.Exec(ctx, `INSERT INTO adapter_sessions
		(session_id, account_id, namespace, title_id, endpoint_ref, expires_at)
		VALUES ($1,$2,$3,$4,$5, now() + make_interval(secs => $6))
		ON CONFLICT (account_id, title_id) DO UPDATE SET
			session_id = EXCLUDED.session_id,
			namespace = EXCLUDED.namespace,
			endpoint_ref = EXCLUDED.endpoint_ref,
			expires_at = EXCLUDED.expires_at,
			updated_at = now()`,
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

// Presence is what a companion surface may know about someone: online, and in
// which title. It is derived from the live session lease, so it needs no
// separate state to keep in step with reality.
type Presence struct {
	AccountID string
	Namespace string
	TitleID   string
	SinceUnix int64
}

// MaxPresenceAccounts caps one presence query. A friends list is the caller.
const MaxPresenceAccounts = 200

// GetPresence returns an entry for each of the given accounts that holds a live
// session. Accounts that are offline are simply absent: no row, no guess. A
// person in two titles at once yields the most recently registered session,
// because the phone shows one line per friend.
func (s *Service) GetPresence(ctx context.Context, accountIDs []string) ([]Presence, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	if len(accountIDs) > MaxPresenceAccounts {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (account_id)
			account_id, namespace, title_id, extract(epoch FROM created_at)::bigint
		FROM adapter_sessions
		WHERE account_id = ANY($1::uuid[]) AND expires_at > now()
		ORDER BY account_id, created_at DESC`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Presence
	for rows.Next() {
		var p Presence
		if err := rows.Scan(&p.AccountID, &p.Namespace, &p.TitleID, &p.SinceUnix); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TitlePlayers is how many people are in one title right now.
type TitlePlayers struct {
	TitleID   string
	Namespace string
	Players   int
}

// PlayerCounts reports who is playing what, from the same live leases presence
// reads. Accounts are counted, not sessions: one person with a console and an
// emulator open is one player, and a status page that said two would be
// wrong in the direction that flatters us.
func (s *Service) PlayerCounts(ctx context.Context) ([]TitlePlayers, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT title_id, namespace, count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now()
		GROUP BY title_id, namespace ORDER BY count(DISTINCT account_id) DESC, title_id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TitlePlayers
	for rows.Next() {
		var t TitlePlayers
		if err := rows.Scan(&t.TitleID, &t.Namespace, &t.Players); err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Someone in two titles is one person online, so the total is its own
	// query rather than the sum of the rows above.
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now()`).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
