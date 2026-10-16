// Package sessions implements the live-adapter-session registry (PRD SW-07):
// short core-owned leases give stale-session cleanup a shared clock. One
// live session per (account, title): a new registration replaces the old.
package sessions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	NodeID      string
	Client      string
}

type Service struct {
	pool *pgxpool.Pool
	// OnChange, when set, is told that an account's presence moved: a session began or ended.
	// It must not block; the fan-out to friends belongs to the caller.
	OnChange func(accountID string)
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// OSes are the operating systems a session may name; "" is a console, or an
// emulator that did not say.
var OSes = []string{"", "windows", "macos", "linux", "android", "ios"}

// MaxClientLen caps the client label an adapter may attach to a session.
const MaxClientLen = 64

// Register inserts a live session, replacing any previous session for the
// same (account, namespace, title). Returns the server-assigned session ID and
// the applied lease. client names what the person is on inside the namespace
// ("ryujinx", "switch", "cemu", ...); empty means the adapter could not tell.
func (s *Service) Register(ctx context.Context, accountID, namespace, titleID, endpointRef, nodeID string, lease time.Duration, client, os string) (string, time.Duration, error) {
	// titleID may be empty, and that is a fact rather than a gap: it means signed in to this
	// network and not in any title. Being online and playing something are different things and
	// the status page reports them separately, so the distinction has to survive down here.
	if accountID == "" || namespace == "" || endpointRef == "" || nodeID == "" || len(client) > MaxClientLen {
		return "", 0, ErrInvalid
	}
	if !slices.Contains(OSes, os) {
		os = "" // a label, not worth refusing a session over
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
	var wasLive bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM adapter_sessions
		WHERE account_id=$1 AND namespace=$2 AND title_id=$3 AND expires_at > now())`,
		accountID, namespace, titleID).Scan(&wasLive); err != nil {
		return "", 0, err
	}
	id := newUUID()
	// Atomic replacement: the unique (account_id, namespace, title_id) key
	// backs an upsert, so concurrent registrations converge on exactly one
	// live row. The namespace is part of the key so that being signed in on a
	// Switch and on a Wii U at once (both title-less) are two rows, not one
	// row the two adapters keep taking from each other.
	if _, err := s.pool.Exec(ctx, `INSERT INTO adapter_sessions
		(session_id, account_id, namespace, title_id, endpoint_ref, node_id, client, os, expires_at)
		VALUES ($1,$2,$3,$4,$5,$7,$8,$9, now() + make_interval(secs => $6))
		ON CONFLICT (account_id, namespace, title_id) DO UPDATE SET
			session_id = EXCLUDED.session_id,
			endpoint_ref = EXCLUDED.endpoint_ref,
			node_id = EXCLUDED.node_id,
			client = EXCLUDED.client,
			os = EXCLUDED.os,
			expires_at = EXCLUDED.expires_at,
			updated_at = now()`,
		id, accountID, namespace, titleID, endpointRef, int(lease.Seconds()), nodeID, client, os); err != nil {
		return "", 0, fmt.Errorf("sessions: register: %w", err)
	}
	if !wasLive && s.OnChange != nil { // a renewal changes nothing a friend can see
		s.OnChange(accountID)
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
	var accountID string
	err := s.pool.QueryRow(ctx, `DELETE FROM adapter_sessions WHERE session_id=$1 RETURNING account_id`, sessionID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil && s.OnChange != nil {
		s.OnChange(accountID)
	}
	return err
}

// LiveAccounts lists the accounts with at least one unexpired session. The caller diffs two
// readings to find who lapsed: a lease running out is not an event anywhere else.
func (s *Service) LiveAccounts(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT account_id::text FROM adapter_sessions WHERE expires_at > now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
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
		WHERE s.account_id=$1 AND s.title_id=$2 AND s.expires_at > now() AND a.status = 'active'
		ORDER BY s.updated_at DESC LIMIT 1`,
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
	NodeID    string
	Client    string
	OS        string
}

// MaxPresenceAccounts caps one presence query. A friends list is the caller.
const MaxPresenceAccounts = 200

// GetPresence returns an entry for each of the given accounts that holds a live
// session. Accounts that are offline are simply absent: no row, no guess. A
// person with several live sessions yields one, because the phone shows one
// line per friend: a session in a title beats a title-less one (signed in on a
// second device must not hide the game being played), then the most recently
// registered wins.
func (s *Service) GetPresence(ctx context.Context, accountIDs []string) ([]Presence, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	if len(accountIDs) > MaxPresenceAccounts {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (account_id)
			account_id, namespace, title_id, extract(epoch FROM created_at)::bigint, node_id, client, os
		FROM adapter_sessions
		WHERE account_id = ANY($1::uuid[]) AND expires_at > now()
		ORDER BY account_id, (title_id <> '') DESC, created_at DESC`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Presence
	for rows.Next() {
		var p Presence
		if err := rows.Scan(&p.AccountID, &p.Namespace, &p.TitleID, &p.SinceUnix, &p.NodeID, &p.Client, &p.OS); err != nil {
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

// PlayerCounts reports two different facts and keeps them apart.
//
// Titles are who is *playing* what. Networks and the total are who is *online*
// on each network — a person signed in with nothing running is online and is
// playing nothing, and reporting them as a player would be a lie in the
// direction that flatters us.
//
// Accounts are counted, not sessions, in both: one person with a console and an
// emulator open is one person.
func (s *Service) PlayerCounts(ctx context.Context) ([]TitlePlayers, []NetworkPlayers, []NodePlayers, int, error) {
	// Titles are who is playing what, so the title-less sessions — signed in, in no game — are
	// left out here. They are counted per network and in the total below, which are about being
	// online rather than about playing.
	rows, err := s.pool.Query(ctx, `SELECT title_id, namespace, count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now() AND title_id <> ''
		GROUP BY title_id, namespace ORDER BY count(DISTINCT account_id) DESC, title_id`)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	defer rows.Close()
	var out []TitlePlayers
	for rows.Next() {
		var t TitlePlayers
		if err := rows.Scan(&t.TitleID, &t.Namespace, &t.Players); err != nil {
			return nil, nil, nil, 0, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, 0, err
	}

	// Per network, for the same reason the total is its own query: two titles
	// on one console network is still one person on that network.
	nrows, err := s.pool.Query(ctx, `SELECT namespace, count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now()
		GROUP BY namespace ORDER BY namespace`)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	defer nrows.Close()
	var nets []NetworkPlayers
	for nrows.Next() {
		var n NetworkPlayers
		if err := nrows.Scan(&n.Namespace, &n.Players); err != nil {
			return nil, nil, nil, 0, err
		}
		nets = append(nets, n)
	}
	if err := nrows.Err(); err != nil {
		return nil, nil, nil, 0, err
	}

	// Per federation node: where people are.
	drows, err := s.pool.Query(ctx, `SELECT node_id, count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now()
		GROUP BY node_id ORDER BY node_id`)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	defer drows.Close()
	var nodes []NodePlayers
	for drows.Next() {
		var n NodePlayers
		if err := drows.Scan(&n.NodeID, &n.Players); err != nil {
			return nil, nil, nil, 0, err
		}
		nodes = append(nodes, n)
	}
	if err := drows.Err(); err != nil {
		return nil, nil, nil, 0, err
	}

	// Someone in two titles is one person online, so the total is its own
	// query rather than the sum of the rows above.
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT account_id)
		FROM adapter_sessions WHERE expires_at > now()`).Scan(&total); err != nil {
		return nil, nil, nil, 0, err
	}
	return out, nets, nodes, total, nil
}

// NodePlayers is how many people are on one federation node.
type NodePlayers struct {
	NodeID  string
	Players int
}

// NetworkPlayers is how many people are on one console network.
type NetworkPlayers struct {
	Namespace string
	Players   int
}

// PlayTally is how long one title has been played on one client and OS.
type PlayTally struct {
	Namespace, TitleID, Client, OS string
	TotalSeconds, LongestSeconds   int64
	Sessions                       int
}

// Playtime adds up, per title, client and OS, the time from a player's first
// game-server login in a session (MarkOnline) to the session's last renewal:
// every finished session (play_sessions, written by the trigger in 0021) and
// every session still in adapter_sessions. A session that never reached a game
// server adds nothing, however long the title was open.
func (s *Service) Playtime(ctx context.Context) ([]PlayTally, error) {
	rows, err := s.pool.Query(ctx, `WITH d AS (
			SELECT namespace, title_id, client, os, online_seconds AS secs FROM play_sessions
			UNION ALL
			SELECT namespace, title_id, client, os,
				GREATEST(0, extract(epoch FROM updated_at - online_since))::int
			FROM adapter_sessions WHERE title_id <> '' AND online_since IS NOT NULL)
		SELECT namespace, title_id, client, os, sum(secs)::bigint, max(secs)::bigint, count(*)
		FROM d WHERE secs > 0 GROUP BY namespace, title_id, client, os
		ORDER BY namespace, title_id, client, os`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayTally
	for rows.Next() {
		var t PlayTally
		if err := rows.Scan(&t.Namespace, &t.TitleID, &t.Client, &t.OS, &t.TotalSeconds, &t.LongestSeconds, &t.Sessions); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PlayHistoryEntry is one title in an account's history.
type PlayHistoryEntry struct {
	TitleID                 string
	Sessions                int
	TotalSeconds            int64
	FirstPlayed, LastPlayed int64
}

// MaxPlayHistory is how many titles a console's friend page keeps.
const MaxPlayHistory = 20

// PlayHistory is what an account played in a namespace, newest title first: every finished session
// (play_sessions) and the one still running (adapter_sessions), per title. Unlike Playtime it counts a
// session whether or not it reached a game server, since a play log is about the title having run.
func (s *Service) PlayHistory(ctx context.Context, accountID, namespace string, limit int) ([]PlayHistoryEntry, error) {
	if !uuidRe.MatchString(accountID) || namespace == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > MaxPlayHistory {
		limit = MaxPlayHistory
	}
	rows, err := s.pool.Query(ctx, `WITH d AS (
			SELECT title_id, started_at, ended_at FROM play_sessions
			WHERE account_id = $1::uuid AND namespace = $2 AND title_id <> ''
			UNION ALL
			SELECT title_id, created_at, updated_at FROM adapter_sessions
			WHERE account_id = $1::uuid AND namespace = $2 AND title_id <> '' AND expires_at > now())
		SELECT title_id, count(*)::int,
			GREATEST(0, sum(extract(epoch FROM ended_at - started_at)))::bigint,
			extract(epoch FROM min(started_at))::bigint, extract(epoch FROM max(ended_at))::bigint
		FROM d GROUP BY title_id ORDER BY max(ended_at) DESC LIMIT $3`, accountID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayHistoryEntry
	for rows.Next() {
		var e PlayHistoryEntry
		if err := rows.Scan(&e.TitleID, &e.Sessions, &e.TotalSeconds, &e.FirstPlayed, &e.LastPlayed); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OnlinePlayer is one player a game server just let in.
type OnlinePlayer struct{ AccountID, Namespace, TitleID string }

// MaxOnlineMarks caps one MarkOnline call.
const MaxOnlineMarks = 1000

// MarkOnline records that each player reached a game server: their live titled
// presence session in that namespace starts counting playtime now, unless it
// already was. Players with no such session, or a malformed account id, are
// skipped and not counted in the result.
func (s *Service) MarkOnline(ctx context.Context, players []OnlinePlayer) (int, error) {
	if len(players) > MaxOnlineMarks {
		return 0, ErrInvalid
	}
	marked := 0
	for _, p := range players {
		if !uuidRe.MatchString(p.AccountID) || p.Namespace == "" {
			continue
		}
		tag, err := s.pool.Exec(ctx, `UPDATE adapter_sessions SET online_since = COALESCE(online_since, now())
			WHERE account_id = $1::uuid AND namespace = $2 AND title_id <> ''
			  AND ($3 = '' OR title_id = $3) AND expires_at > now()`,
			p.AccountID, p.Namespace, p.TitleID)
		if err != nil {
			return marked, err
		}
		if tag.RowsAffected() > 0 {
			marked++
		}
	}
	return marked, nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
