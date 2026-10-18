package identity

import (
	"context"
	"fmt"
)

// Directory returns active accounts in stable ID order for home-owned adapter
// backfills. Only minimal profile fields leave this path.
func (s *Service) Directory(ctx context.Context, after string, limit int) ([]Account, error) {
	if limit < 1 || limit > 501 {
		return nil, fmt.Errorf("invalid directory limit")
	}
	rows, e := s.pool.Query(ctx, `SELECT id::text, display_name, country, COALESCE(handle,'') FROM accounts WHERE status = 'active' AND ($1 = '' OR id::text > $1) ORDER BY id::text LIMIT $2`, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if e := rows.Scan(&a.ID, &a.DisplayName, &a.Country, &a.Handle); e != nil {
			return nil, e
		}
		a.Status = StatusActive
		out = append(out, a)
	}
	return out, rows.Err()
}
