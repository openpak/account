// Package federation keeps the register of region nodes: who they are, whether
// a person has approved them, and what addresses they announced last. A node
// authenticates with its own key, so one region can be cut off without
// touching another.
package federation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid  = errors.New("federation: invalid request")
	ErrNotFound = errors.New("federation: node not found")
	ErrExists   = errors.New("federation: node exists")
)

// Home is the built-in node: whatever calls with ACCOUNT_INTERNAL_KEY.
const Home = "home"

// Stale is how long after its last announce a node stops being listed. Nodes
// announce every minute; three misses is a node that is gone, not slow.
const Stale = 3 * time.Minute

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

type Node struct {
	ID       string
	Name     string
	Country  string
	Services map[string]string
	LastSeen time.Time
	Approved bool
}

type Service struct {
	pool    *pgxpool.Pool
	homeKey string
}

func New(pool *pgxpool.Pool, homeKey string) *Service { return &Service{pool: pool, homeKey: homeKey} }

func hash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// Authenticate resolves a presented key to a node id. The home key is compared
// in constant time; node keys are found by hash, which is constant time for
// free because the key is random.
func (s *Service) Authenticate(ctx context.Context, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.homeKey)) == 1 {
		return Home, true
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM federation_nodes WHERE key_hash=$1`, hash(key)).Scan(&id)
	return id, err == nil
}

// Create registers a node and returns its key, the only time it exists in
// the clear.
func (s *Service) Create(ctx context.Context, id, name, country string) (string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	name = strings.TrimSpace(name)
	if !slugRe.MatchString(id) || id == Home || name == "" || len(country) != 2 {
		return "", ErrInvalid
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := "opnode_" + hex.EncodeToString(raw)
	_, err := s.pool.Exec(ctx, `INSERT INTO federation_nodes (id, name, country, key_hash) VALUES ($1,$2,$3,$4)`,
		id, name, country, hash(key))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return "", ErrExists
		}
		return "", err
	}
	return key, nil
}

// Announce replaces what a node advertises and marks it seen.
func (s *Service) Announce(ctx context.Context, id string, services map[string]string) (bool, error) {
	if id == Home {
		return true, nil // home has nothing to announce; its addresses are configuration
	}
	if services == nil {
		services = map[string]string{}
	}
	for k, v := range services {
		if len(k) > 32 || len(v) > 256 {
			return false, ErrInvalid
		}
	}
	var approved bool
	err := s.pool.QueryRow(ctx, `UPDATE federation_nodes SET services=$2, last_seen_at=now() WHERE id=$1 RETURNING approved`,
		id, services).Scan(&approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return approved, err
}

func (s *Service) SetApproved(ctx context.Context, id string, approved bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE federation_nodes SET approved=$2 WHERE id=$1`, id, approved)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns the nodes that may be handed out: approved and recently seen.
func (s *Service) List(ctx context.Context) ([]Node, error) {
	return s.query(ctx, `WHERE approved AND last_seen_at > now() - $1::interval`, Stale.String())
}

// ListAll returns every node for the admin view.
func (s *Service) ListAll(ctx context.Context) ([]Node, error) {
	return s.query(ctx, ``)
}

func (s *Service) query(ctx context.Context, where string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, country, services, coalesce(last_seen_at, 'epoch'::timestamptz), approved
		FROM federation_nodes `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.Country, &n.Services, &n.LastSeen, &n.Approved); err != nil {
			return nil, err
		}
		if n.LastSeen.Unix() <= 0 {
			n.LastSeen = time.Time{}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
