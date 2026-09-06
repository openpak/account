// Package events implements the durable invalidation event log (PRD §7):
// "Versioned, durable events for bans, unlinking, deletion, and relevant
// credential/session changes". Consumers poll with deduplication.
package events

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Emit records one event. Callers inside an active transaction should use
// EmitTx instead so events commit atomically with the state change.
func (s *Service) Emit(ctx context.Context, typ, accountID, namespace, subjectID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO invalidation_events (event_id, type, account_id, namespace, subject_id)
		VALUES (gen_random_uuid(), $1, $2, NULLIF($3,''), NULLIF($4,''))`, typ, accountID, namespace, subjectID)
	if err != nil {
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
