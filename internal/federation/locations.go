package federation

import (
	"context"
	"regexp"
	"strings"
)

// Location is one entry of the coverage plan: a place OpenPak wants nodes.
// Approved is derived (approved nodes whose region is this code).
type Location struct {
	Code     string // canonical region, e.g. "sa-east"
	Country  string
	City     string
	Wanted   int
	Status   string // open | full | closed
	Approved int
}

// Open says whether applications are taken here now: the admin left it open
// and it still wants more nodes than it has.
func (l Location) Open() bool { return l.Status == "open" && l.Approved < l.Wanted }

var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+){0,3}$`)

// ValidRegion is the shape of a region code.
func ValidRegion(code string) bool { return len(code) <= 32 && regionRe.MatchString(code) }

// ListLocations is the whole coverage plan, with approved counts.
func (s *Service) ListLocations(ctx context.Context) ([]Location, error) {
	rows, err := s.pool.Query(ctx, `SELECT l.code, l.country, l.city, l.wanted, l.status,
		(SELECT count(*) FROM federation_nodes n WHERE n.region = l.code AND n.approved)
		FROM federation_locations l ORDER BY l.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Location
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.Code, &l.Country, &l.City, &l.Wanted, &l.Status, &l.Approved); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLocation is one entry, or ErrNotFound.
func (s *Service) GetLocation(ctx context.Context, code string) (Location, error) {
	all, err := s.ListLocations(ctx)
	if err != nil {
		return Location{}, err
	}
	for _, l := range all {
		if l.Code == code {
			return l, nil
		}
	}
	return Location{}, ErrNotFound
}

// PutLocation creates or replaces an entry.
func (s *Service) PutLocation(ctx context.Context, l Location) error {
	l.Code = strings.ToLower(strings.TrimSpace(l.Code))
	l.Country = strings.ToUpper(strings.TrimSpace(l.Country))
	l.City = strings.TrimSpace(l.City)
	if !ValidRegion(l.Code) || len(l.Country) != 2 || len(l.City) > 64 || l.Wanted < 0 || l.Wanted > 1000 {
		return ErrInvalid
	}
	switch l.Status {
	case "open", "full", "closed":
	default:
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO federation_locations (code, country, city, wanted, status) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (code) DO UPDATE SET country=EXCLUDED.country, city=EXCLUDED.city, wanted=EXCLUDED.wanted, status=EXCLUDED.status`,
		l.Code, l.Country, l.City, l.Wanted, l.Status)
	return err
}

// DeleteLocation drops an entry. Nodes keep their region code.
func (s *Service) DeleteLocation(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM federation_locations WHERE code=$1`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
