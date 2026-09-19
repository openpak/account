// Package moderation holds what moderators read and act on (website contract
// requests CR-5 and CR-6): account search across every identifier the core
// knows, and the reports queue. Banning itself lives in identity, because it
// is a change of account status with sessions and events attached.
package moderation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid     = errors.New("moderation: invalid request")
	ErrNotFound    = errors.New("moderation: not found")
	ErrNotOpen     = errors.New("moderation: report is not open")
	ErrSelfReport  = errors.New("moderation: an account cannot report itself")
	ErrUnknownUser = errors.New("moderation: account unknown")
)

const (
	DefaultLimit   = 20
	MaxSearchLimit = 100
	MaxListLimit   = 500
	MaxDetail      = 2000
	MaxResolution  = 2000
)

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	kindRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// IsUUID reports whether s is a canonical UUID string, so callers can answer
// InvalidArgument/NotFound instead of letting Postgres reject the cast.
func IsUUID(s string) bool { return uuidRe.MatchString(s) }

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Account is the moderator's view of an account. It carries the email, which
// no other projection does: moderators need it to find people.
type Account struct {
	ID            string
	Email         string
	DisplayName   string
	Status        string
	EmailVerified bool
	CreatedAt     time.Time
	// BanReason is the reason recorded with the latest ban; empty unless banned.
	BanReason string
	// MatchedSubjects lists "namespace:subject" for the links the query hit.
	MatchedSubjects []string
}

func clamp(limit, max int32) int32 {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > max {
		return max
	}
	return limit
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Search finds accounts whose id equals the query, whose email or display
// name contains it (case-insensitive), or that hold a link whose subject
// equals it in any namespace. Deleted accounts are never returned. Exact
// matches (id, email, subject) sort before substring ones.
func (s *Service) Search(ctx context.Context, query string, limit int32) ([]Account, error) {
	q := strings.TrimSpace(query)
	if q == "" || len(q) > 254 {
		return nil, ErrInvalid
	}
	limit = clamp(limit, MaxSearchLimit)
	id := ""
	if IsUUID(q) {
		id = strings.ToLower(q)
	}
	pattern := "%" + likeEscape(strings.ToLower(q)) + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.email, a.display_name, a.status, a.email_verified_at IS NOT NULL, a.created_at,
			COALESCE((SELECT m.reason FROM moderation_actions m
				WHERE m.account_id = a.id AND m.action = 'ban' AND a.status = 'banned'
				ORDER BY m.created_at DESC LIMIT 1), ''),
			COALESCE((SELECT array_agg(l.namespace || ':' || l.subject_id ORDER BY l.namespace)
				FROM adapter_links l WHERE l.account_id = a.id AND l.subject_id = $2), '{}')
		FROM accounts a
		WHERE a.status <> 'deleted' AND (
			($1 <> '' AND a.id::text = $1)
			OR a.email_normalized LIKE $3
			OR lower(a.display_name) LIKE $3
			OR EXISTS (SELECT 1 FROM adapter_links l WHERE l.account_id = a.id AND l.subject_id = $2))
		ORDER BY (a.id::text = $1 OR a.email_normalized = lower($2)
			OR EXISTS (SELECT 1 FROM adapter_links l WHERE l.account_id = a.id AND l.subject_id = $2)) DESC,
			a.created_at DESC
		LIMIT $4`, id, q, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("moderation: search: %w", err)
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.DisplayName, &a.Status, &a.EmailVerified, &a.CreatedAt,
			&a.BanReason, &a.MatchedSubjects); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type Report struct {
	ID               string
	ReporterID       string
	ReporterName     string
	SubjectAccountID string
	SubjectName      string
	Kind             string
	Detail           string
	Status           string
	Resolution       string
	ResolvedBy       string
	CreatedAt        time.Time
	ResolvedAt       time.Time
}

const reportCols = `r.id, r.reporter_id, ra.display_name, r.subject_account_id, sa.display_name,
	r.kind, r.detail, r.status, COALESCE(r.resolution,''), COALESCE(r.resolved_by,''),
	r.created_at, COALESCE(r.resolved_at, 'epoch'::timestamptz)`

const reportFrom = ` FROM reports r
	JOIN accounts ra ON ra.id = r.reporter_id
	JOIN accounts sa ON sa.id = r.subject_account_id`

func scanReport(row pgx.Row) (*Report, error) {
	var r Report
	if err := row.Scan(&r.ID, &r.ReporterID, &r.ReporterName, &r.SubjectAccountID, &r.SubjectName,
		&r.Kind, &r.Detail, &r.Status, &r.Resolution, &r.ResolvedBy, &r.CreatedAt, &r.ResolvedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// File records a report. Filing the same (reporter, subject, kind) while an
// earlier one is still open returns that report instead of queueing a second:
// a player mashing the button costs the moderators nothing.
func (s *Service) File(ctx context.Context, reporterID, subjectID, kind, detail string) (*Report, error) {
	kind = strings.TrimSpace(kind)
	detail = strings.TrimSpace(detail)
	if !IsUUID(reporterID) || !IsUUID(subjectID) || !kindRe.MatchString(kind) || len(detail) > MaxDetail {
		return nil, ErrInvalid
	}
	if strings.EqualFold(reporterID, subjectID) {
		return nil, ErrSelfReport
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id IN ($1,$2) AND status <> 'deleted'`,
			reporterID, subjectID).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return ErrUnknownUser
		}
		err := tx.QueryRow(ctx, `INSERT INTO reports (reporter_id, subject_account_id, kind, detail)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (reporter_id, subject_account_id, kind) WHERE status = 'open' DO NOTHING
			RETURNING id`, reporterID, subjectID, kind, detail).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT id FROM reports WHERE reporter_id=$1 AND subject_account_id=$2
				AND kind=$3 AND status='open'`, reporterID, subjectID, kind).Scan(&id)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Get returns one report.
func (s *Service) Get(ctx context.Context, id string) (*Report, error) {
	if !IsUUID(id) {
		return nil, ErrNotFound
	}
	r, err := scanReport(s.pool.QueryRow(ctx, `SELECT `+reportCols+reportFrom+` WHERE r.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// List returns reports in one status ("" for all): open ones oldest first,
// because the queue is worked from the front; otherwise newest first.
func (s *Service) List(ctx context.Context, status string, limit int32) ([]Report, error) {
	if status != "" && status != "open" && status != "resolved" {
		return nil, ErrInvalid
	}
	limit = clamp(limit, MaxListLimit)
	order := "r.created_at DESC"
	if status == "open" {
		order = "r.created_at ASC"
	}
	rows, err := s.pool.Query(ctx, `SELECT `+reportCols+reportFrom+`
		WHERE ($1 = '' OR r.status = $1) ORDER BY `+order+` LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Resolve closes an open report with the moderator's resolution and name.
// A report is resolved once; resolving it again is ErrNotOpen.
func (s *Service) Resolve(ctx context.Context, id, resolution, actor string) (*Report, error) {
	resolution = strings.TrimSpace(resolution)
	actor = strings.TrimSpace(actor)
	if resolution == "" || len(resolution) > MaxResolution || actor == "" || len(actor) > 128 {
		return nil, ErrInvalid
	}
	if !IsUUID(id) {
		return nil, ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE reports SET status='resolved', resolution=$2, resolved_by=$3, resolved_at=now()
		WHERE id=$1 AND status='open'`, id, resolution, actor)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return nil, err
		}
		return nil, ErrNotOpen
	}
	return s.Get(ctx, id)
}
