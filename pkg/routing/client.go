package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Paths the account core serves (internal network only, routing key).
const (
	PathTable   = "/internal/v1/routing"
	PathAccount = "/internal/v1/routing/account"
	PathResolve = "/internal/v1/routing/resolve"
)

// KeyHeader carries the routing key. It reads the routing table and nothing else.
const KeyHeader = "X-Routing-Key"

// Account is what routing needs to know about a player.
type Account struct {
	Country string `json:"country"`
	Tester  bool   `json:"tester"`
}

// AccountRef names a player: the core account id, or an adapter link
// (namespace "switch", subject the BAAS user id).
type AccountRef struct {
	AccountID string
	Namespace string
	Subject   string
}

func (r AccountRef) empty() bool { return r.AccountID == "" && (r.Namespace == "" || r.Subject == "") }

func (r AccountRef) query() url.Values {
	q := url.Values{}
	if r.AccountID != "" {
		q.Set("account_id", r.AccountID)
	} else {
		q.Set("namespace", r.Namespace)
		q.Set("subject", r.Subject)
	}
	return q
}

// Client reads the routing table from the account core and caches it. A
// zero BaseURL is a server with federation off: Table is nil and every
// Resolve is home, exactly as before federation.
type Client struct {
	BaseURL    string        // e.g. http://openpak-account:8000
	Key        string        // ACCOUNT_ROUTING_KEY
	HTTP       *http.Client  // default: 3 s timeout
	TTL        time.Duration // table refresh; default 30 s
	AccountTTL time.Duration // per-account answer; default 5 min

	mu       sync.Mutex
	table    *Table
	fetched  time.Time
	inflight bool
	accounts map[string]cachedAccount
}

type cachedAccount struct {
	a  Account
	at time.Time
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 3 * time.Second}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.Key != "" {
		req.Header.Set(KeyHeader, c.Key)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("routing: %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(out)
}

// Refresh fetches the table now. On failure the last good table is kept: a
// home that briefly cannot reach the core must not forget its nodes.
func (c *Client) Refresh(ctx context.Context) error {
	if c == nil || c.BaseURL == "" {
		return nil
	}
	var t Table
	err := c.get(ctx, PathTable, nil, &t)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = false
	if err != nil {
		return err
	}
	c.table, c.fetched = &t, time.Now()
	return nil
}

// Table is the cached table, refreshed in the background when older than
// TTL. Nil until the first fetch succeeds (and always nil when BaseURL is
// unset); a nil table resolves everything to home.
func (c *Client) Table() *Table {
	if c == nil || c.BaseURL == "" {
		return nil
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	c.mu.Lock()
	t := c.table
	stale := time.Since(c.fetched) > ttl && !c.inflight
	if stale {
		c.inflight = true
	}
	c.mu.Unlock()
	if stale {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.Refresh(ctx)
		}()
	}
	return t
}

// Account asks the core for a player's country and test allow-list status,
// cached per player. Errors and unknown players are the zero Account: no
// country, not a tester, so home.
func (c *Client) Account(ctx context.Context, ref AccountRef) Account {
	if c == nil || c.BaseURL == "" || ref.empty() {
		return Account{}
	}
	k := ref.AccountID + "|" + ref.Namespace + "|" + ref.Subject
	ttl := c.AccountTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	c.mu.Lock()
	if v, ok := c.accounts[k]; ok && time.Since(v.at) < ttl {
		c.mu.Unlock()
		return v.a
	}
	c.mu.Unlock()
	var a Account
	if err := c.get(ctx, PathAccount, ref.query(), &a); err != nil {
		return Account{}
	}
	a.Country = strings.ToUpper(a.Country)
	c.mu.Lock()
	if c.accounts == nil {
		c.accounts = map[string]cachedAccount{}
	}
	if len(c.accounts) > 100000 {
		c.accounts = map[string]cachedAccount{}
	}
	c.accounts[k] = cachedAccount{a: a, at: time.Now()}
	c.mu.Unlock()
	return a
}

// Resolve is Table().Resolve: home when there is no table.
func (c *Client) Resolve(req Request) Result { return c.Table().Resolve(req) }
