package federation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openpak/account/pkg/routing"
)

// RoutingTTL is how long the core reuses one routing table. Game servers
// cache it again on their side, so the database sees a handful of reads a
// minute however many rooms are placed.
const RoutingTTL = 10 * time.Second

type routingCache struct {
	table *routing.Table
	at    time.Time
}

func (s *Service) invalidateRouting() {
	s.routingMu.Lock()
	s.routing = routingCache{}
	s.routingMu.Unlock()
}

// RoutingTable is the region registry plus every node in rotation, test
// nodes flagged. It is for game servers at home, never for the public: the
// public list is PublicList.
func (s *Service) RoutingTable(ctx context.Context) (*routing.Table, error) {
	s.routingMu.Lock()
	c := s.routing
	s.routingMu.Unlock()
	if c.table != nil && s.now().Sub(c.at) < RoutingTTL {
		return c.table, nil
	}
	nodes, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	t := TableFrom(nodes)
	s.routingMu.Lock()
	s.routing = routingCache{table: t, at: s.now()}
	s.routingMu.Unlock()
	return t, nil
}

// TableFrom turns nodes in rotation into a routing table.
func TableFrom(nodes []Node) *routing.Table {
	rn := make([]routing.Node, 0, len(nodes))
	for _, n := range nodes {
		if !n.InRotation {
			continue
		}
		rn = append(rn, routing.Node{ID: n.ID, Region: n.Region, Country: n.Country, Services: n.Services, Test: n.Test})
	}
	return routing.NewTable(rn)
}

// Tester is one account on the test-node allow-list.
type Tester struct {
	AccountID string
	Email     string // for the admin view only
	Note      string
	Added     time.Time
}

// ListTesters is the allow-list, for the admin page.
func (s *Service) ListTesters(ctx context.Context) ([]Tester, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.account_id::text, a.email, t.note, t.created_at
		FROM federation_testers t JOIN accounts a ON a.id = t.account_id ORDER BY t.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tester
	for rows.Next() {
		var t Tester
		if err := rows.Scan(&t.AccountID, &t.Email, &t.Note, &t.Added); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutTester adds an account to the allow-list, by account id or by sign-in
// email (the admin types an address). Returns the account id.
func (s *Service) PutTester(ctx context.Context, accountOrEmail, note string) (string, error) {
	accountOrEmail = strings.TrimSpace(accountOrEmail)
	note = strings.TrimSpace(note)
	if accountOrEmail == "" || len(note) > 200 {
		return "", ErrInvalid
	}
	var id string
	var err error
	if strings.Contains(accountOrEmail, "@") {
		err = s.pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE email_normalized=$1`, strings.ToLower(accountOrEmail)).Scan(&id)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE id::text=$1`, strings.ToLower(accountOrEmail)).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO federation_testers (account_id, note) VALUES ($1::uuid, $2)
		ON CONFLICT (account_id) DO UPDATE SET note=EXCLUDED.note`, id, note); err != nil {
		return "", err
	}
	return id, nil
}

// DeleteTester removes an account from the allow-list.
func (s *Service) DeleteTester(ctx context.Context, accountID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM federation_testers WHERE account_id::text=$1`, strings.ToLower(strings.TrimSpace(accountID)))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AccountRouting is what routing needs about one player: their account
// country and whether they may reach test nodes. A player is named by core
// account id or by an active adapter link (namespace + subject). An unknown
// player is the zero answer, never an error: routing then sends them home.
func (s *Service) AccountRouting(ctx context.Context, ref routing.AccountRef) (routing.Account, error) {
	var q string
	var args []any
	switch {
	case ref.AccountID != "":
		q, args = `SELECT a.country, EXISTS (SELECT 1 FROM federation_testers t WHERE t.account_id = a.id)
			FROM accounts a WHERE a.id::text = $1`, []any{strings.ToLower(ref.AccountID)}
	case ref.Namespace != "" && ref.Subject != "":
		q, args = `SELECT a.country, EXISTS (SELECT 1 FROM federation_testers t WHERE t.account_id = a.id)
			FROM adapter_links l JOIN accounts a ON a.id = l.account_id
			WHERE l.namespace = $1 AND l.subject_id = $2 AND l.state = 'active'`, []any{ref.Namespace, ref.Subject}
	default:
		return routing.Account{}, nil
	}
	var a routing.Account
	err := s.pool.QueryRow(ctx, q, args...).Scan(&a.Country, &a.Tester)
	if errors.Is(err, pgx.ErrNoRows) {
		return routing.Account{}, nil
	}
	if err != nil {
		return routing.Account{}, err
	}
	a.Country = strings.ToUpper(a.Country)
	return a, nil
}
