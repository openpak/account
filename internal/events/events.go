// Package events implements the durable invalidation event log (PRD §7):
// "Versioned, durable events for bans, unlinking, deletion, and relevant
// credential/session changes". Consumers poll with deduplication, or
// subscribe (universal-social-prd §4a): the two share one stream, one
// version order and one dedup key.
package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// KnownTypes is the closed event set the database constraint admits. The
// core's own writers and EmitEvent callers are held to it, so a mistake
// surfaces as InvalidArgument here instead of as a failed commit.
var KnownTypes = map[string]bool{
	"account_banned":         true,
	"account_unbanned":       true,
	"link_unlinked":          true,
	"account_delete_started": true,
	"account_deleted":        true,
	"credential_revoked":     true,
	"friend_requested":       true,
	"friend_accepted":        true,
	"friend_removed":         true,
	"invitation_sent":        true,
	"message":                true,
}

type Service struct {
	pool *pgxpool.Pool
	hub  *hub
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, hub: newHub()} }

// Emit records one event on its own transaction.
func (s *Service) Emit(ctx context.Context, typ, accountID, namespace, subjectID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return s.EmitTx(ctx, tx, typ, accountID, namespace, subjectID)
	})
}

// EmitEvent records one event from an internal service (chat announces
// messages this way) and returns it with its version assigned.
func (s *Service) EmitEvent(ctx context.Context, typ, accountID, namespace, subjectID string) (*accountv1.InvalidationEvent, error) {
	if !KnownTypes[typ] {
		return nil, fmt.Errorf("events: unknown type %q", typ)
	}
	ev := &accountv1.InvalidationEvent{Type: typ, AccountId: accountID, Namespace: namespace, SubjectId: subjectID}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var version int64
		var eventID string
		if err := tx.QueryRow(ctx, `WITH v AS (
			UPDATE invalidation_event_counter SET n = n + 1 WHERE id = 1 RETURNING n
		), e AS (
			INSERT INTO invalidation_events (version, event_id, type, account_id, namespace, subject_id)
			SELECT v.n, gen_random_uuid(), $1, $2, NULLIF($3,''), NULLIF($4,'') FROM v
			RETURNING version, event_id
		) SELECT version, event_id FROM e`, typ, accountID, namespace, subjectID).Scan(&version, &eventID); err != nil {
			return fmt.Errorf("events: emit: %w", err)
		}
		ev.Version = uint64(version)
		ev.EventId = eventID
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.hub.wake()
	return ev, nil
}

// EmitTx records one event inside the caller's transaction, atomically with
// the state change it describes (review P1 #4). Version assignment takes a
// row lock on the counter that is held until commit, so version order equals
// commit order — a poller can never permanently skip an event (review P1 #3;
// a plain sequence can commit v5 after v6, letting the poller's high-water
// mark advance past it).
func (s *Service) EmitTx(ctx context.Context, tx pgx.Tx, typ, accountID, namespace, subjectID string) error {
	var version int64
	if err := tx.QueryRow(ctx,
		`UPDATE invalidation_event_counter SET n = n + 1 WHERE id = 1 RETURNING n`).Scan(&version); err != nil {
		return fmt.Errorf("events: version: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invalidation_events
		(version, event_id, type, account_id, namespace, subject_id)
		VALUES ($1, gen_random_uuid(), $2, $3, NULLIF($4,''), NULLIF($5,''))`,
		version, typ, accountID, namespace, subjectID); err != nil {
		return fmt.Errorf("events: emit: %w", err)
	}
	s.hub.wake()
	return nil
}

// Poll returns events after sinceVersion, oldest first, capped at 1000.
func (s *Service) Poll(ctx context.Context, sinceVersion uint64, limit int32) (*accountv1.PollEventsResponse, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `SELECT version, event_id, type, account_id, COALESCE(namespace,''), COALESCE(subject_id,''),
		EXTRACT(EPOCH FROM created_at)::bigint
		FROM invalidation_events WHERE version > $1 ORDER BY version LIMIT $2`, sinceVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &accountv1.PollEventsResponse{}
	for rows.Next() {
		var ev accountv1.InvalidationEvent
		var eventID string
		if err := rows.Scan(&ev.Version, &eventID, &ev.Type, &ev.AccountId, &ev.Namespace, &ev.SubjectId, &ev.CreatedAtUnix); err != nil {
			return nil, err
		}
		ev.EventId = eventID
		resp.Events = append(resp.Events, &ev)
		resp.MaxVersion = ev.Version
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if resp.MaxVersion < sinceVersion {
		resp.MaxVersion = sinceVersion
	}
	return resp, nil
}

// pollPage is the subscription's drain page.
const pollPage = 500

// Subscribe streams every event after sinceVersion and then stays live until
// ctx ends. Delivery is at-least-once: a wake can arrive while a commit is
// still in flight, so after every wake the subscriber re-polls from its
// high-water mark and finds nothing if the event is not visible yet — the
// safety tick below bounds how long that re-poll can be deferred, which is
// what makes the wake heuristic safe rather than load-bearing.
func (s *Service) Subscribe(ctx context.Context, sinceVersion uint64, emit func(*accountv1.InvalidationEvent) error) error {
	version := sinceVersion
	for {
		for {
			resp, err := s.Poll(ctx, version, pollPage)
			if err != nil {
				return err
			}
			for _, ev := range resp.GetEvents() {
				if err := emit(ev); err != nil {
					return err
				}
				version = ev.GetVersion()
			}
			if len(resp.GetEvents()) < pollPage {
				break
			}
		}
		wake := s.hub.wait()
		tick := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			tick.Stop()
			return ctx.Err()
		case <-wake:
			tick.Stop()
		case <-tick.C:
		}
	}
}

// hub is a broadcast wake-up for subscribers. wake() is best effort and may
// be called before the emitting transaction commits; correctness rests on the
// re-poll, never on the wake.
type hub struct {
	mu sync.Mutex
	ch chan struct{}
}

func newHub() *hub { return &hub{ch: make(chan struct{})} }

func (h *hub) wake() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.ch)
	h.ch = make(chan struct{})
}

func (h *hub) wait() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch
}
