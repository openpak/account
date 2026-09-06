// Package events implements the durable invalidation event log (PRD §7):
// "Versioned, durable events for bans, unlinking, deletion, and relevant
// credential/session changes". Consumers poll with deduplication.
package events

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Emit records one event on its own transaction.
func (s *Service) Emit(ctx context.Context, typ, accountID, namespace, subjectID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return s.EmitTx(ctx, tx, typ, accountID, namespace, subjectID)
	})
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
