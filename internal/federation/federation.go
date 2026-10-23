// Package federation keeps the register of region nodes: who they are, whether
// a person has approved them, which servers they run on and what those servers
// last reported. A node is one region, one operator and one approval; each of
// its servers has its own key, so one leaked box is revoked without touching
// the others or the region.
//
// Two kinds of key exist besides home's. The node key ("opnode_") is the
// operator's enrolment credential: its only power is to join a server. A
// server key ("opsrv_") announces and asks for self-tests, and nothing else.
// Neither can read an account, a friend list or a link.
package federation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid  = errors.New("federation: invalid request")
	ErrNotFound = errors.New("federation: node not found")
	ErrExists   = errors.New("federation: node exists")
	// ErrNotCompliant refuses an approval; the wrapped message says what is short.
	ErrNotCompliant = errors.New("federation: node is below the minimum")
	// ErrTooSoon refuses a self-test inside the interval.
	ErrTooSoon = errors.New("federation: self-test already run in this interval")
	// ErrWrongKey is a node key used where a server key is needed, or the reverse.
	ErrWrongKey = errors.New("federation: wrong kind of key")
)

// Home is the built-in node: whatever calls with ACCOUNT_INTERNAL_KEY.
const Home = "home"

// Stale is the default for how long after its last announce a server counts
// as not reporting. Servers announce every minute; three misses is a server
// that is gone, not slow.
const Stale = 3 * time.Minute

var (
	slugRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)
	serverNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// Roles a server may announce. They are the transports a node can carry (see
// research/federation.md): Photon's Game role and its region ping responder
// (UDP 5055), STUN/TURN for NPLN, Among Us's Impostor, and PlayFab Party's
// relay and QoS beacon.
var Roles = []string{"photon", "photon_ping", "turn", "stun", "npln_relay", "impostor", "playfab_party", "playfab_qos"}

// Node is one region. Services, LastSeen, the totals, Problems and InRotation
// are derived from its servers by evaluate; they are not stored.
type Node struct {
	ID       string
	Name     string
	Country  string
	Region   string // coverage-plan code ("sa-east"); "" for a node created outside the plan
	Services map[string]string
	LastSeen time.Time
	Approved bool
	// Test: an OpenPak test node. Never in the public list; routed only to
	// accounts on the test allow-list (routing.go).
	Test bool

	WaiverReason string
	WaiverUntil  time.Time // zero: no expiry

	Servers     []Server
	TotalCores  int
	TotalRAMMB  int64
	TotalDiskGB int64 // NVMe/SSD only
	Problems    []string
	InRotation  bool
}

// Server is one box of a node and the last thing it reported. Every figure
// here except the measured ones is self-declared.
type Server struct {
	ID       string // "<node>/<name>"
	NodeID   string
	Name     string
	Services map[string]string
	Report
	ObservedIP string
	LastSeen   time.Time

	DownMbps float64 // measured by home
	DownAt   time.Time
	UpMbps   float64 // measured by home
	UpAt     time.Time
	RTTMs    int // reported by the server

	Problems []string
}

// Report is what a server says about itself on every announce.
type Report struct {
	Cores    int
	RAMMB    int64
	DiskGB   int64
	DiskSSD  bool
	IPv4     string
	IPv6     string
	PortMbps int
}

// Caller is who presented a key: home, a node (enrolment key) or one server.
type Caller struct {
	Node   string
	Server string // "" for home and for a node key
}

type Service struct {
	pool    *pgxpool.Pool
	homeKey string
	Req     Requirements
	now     func() time.Time

	routingMu sync.Mutex
	routing   routingCache
}

func New(pool *pgxpool.Pool, homeKey string) *Service {
	return &Service{pool: pool, homeKey: homeKey, Req: DefaultRequirements(), now: time.Now}
}

// WithRequirements replaces the admission rules (from the environment at boot).
func (s *Service) WithRequirements(r Requirements) *Service { s.Req = r; return s }

func hash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func newKey(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

// Authenticate resolves a presented key to its caller. The home key is
// compared in constant time; node and server keys are found by hash, which is
// constant time for free because the key is random.
func (s *Service) Authenticate(ctx context.Context, key string) (Caller, bool) {
	if key == "" {
		return Caller{}, false
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.homeKey)) == 1 {
		return Caller{Node: Home}, true
	}
	var c Caller
	if strings.HasPrefix(key, "opsrv_") {
		err := s.pool.QueryRow(ctx, `SELECT node_id, id FROM federation_servers WHERE key_hash=$1`, hash(key)).Scan(&c.Node, &c.Server)
		return c, err == nil
	}
	err := s.pool.QueryRow(ctx, `SELECT id FROM federation_nodes WHERE key_hash=$1`, hash(key)).Scan(&c.Node)
	return c, err == nil
}

// Admitted says whether a node may do anything beyond announcing: approved and
// in rotation right now. Home always is.
func (s *Service) Admitted(ctx context.Context, node string) bool {
	if node == Home {
		return true
	}
	n, err := s.Get(ctx, node)
	return err == nil && n.InRotation
}

// Create registers a node and returns its key, the only time it exists in
// the clear. The key only enrols servers. region is the coverage-plan code
// and may be empty; country stays required, because routing reads it.
func (s *Service) Create(ctx context.Context, id, name, country, region string) (string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	name = strings.TrimSpace(name)
	region = strings.ToLower(strings.TrimSpace(region))
	if !slugRe.MatchString(id) || id == Home || name == "" || len(country) != 2 || (region != "" && !ValidRegion(region)) {
		return "", ErrInvalid
	}
	key, err := newKey("opnode_")
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO federation_nodes (id, name, country, region, key_hash) VALUES ($1,$2,$3,$4,$5)`,
		id, name, country, region, hash(key))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return "", ErrExists
		}
		return "", err
	}
	return key, nil
}

// RotateNodeKey replaces a node's enrolment key. Servers already joined keep
// their own keys.
func (s *Service) RotateNodeKey(ctx context.Context, id string) (string, error) {
	key, err := newKey("opnode_")
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE federation_nodes SET key_hash=$2 WHERE id=$1`, id, hash(key))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// JoinServer enrols a server under the calling node and returns its own key,
// once. Joining a name again replaces that server's key: a reinstalled box
// rejoins without an admin, and the old key stops working.
func (s *Service) JoinServer(ctx context.Context, c Caller, name string) (id, key string, err error) {
	if c.Node == Home || c.Server != "" {
		return "", "", ErrWrongKey
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !serverNameRe.MatchString(name) {
		return "", "", ErrInvalid
	}
	key, err = newKey("opsrv_")
	if err != nil {
		return "", "", err
	}
	id = c.Node + "/" + name
	_, err = s.pool.Exec(ctx, `INSERT INTO federation_servers (id, node_id, name, key_hash) VALUES ($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET key_hash=EXCLUDED.key_hash`, id, c.Node, name, hash(key))
	if err != nil {
		return "", "", err
	}
	return id, key, nil
}

// RevokeServer removes one server; its key stops working at once.
func (s *Service) RevokeServer(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM federation_servers WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateRouting()
	return nil
}

// AnnounceResult is what a server learns from its announce.
type AnnounceResult struct {
	Node        Node
	Server      Server
	SelfTestDue bool
}

// Announce stores what a server runs and reports, and marks it seen.
// observedIP is the source address home saw, set by the relay, never by the
// server.
func (s *Service) Announce(ctx context.Context, c Caller, services map[string]string, rep Report, observedIP string) (AnnounceResult, error) {
	if c.Node == Home {
		return AnnounceResult{Node: Node{ID: Home, Approved: true, InRotation: true}}, nil // home's addresses are configuration
	}
	if c.Server == "" {
		return AnnounceResult{}, fmt.Errorf("%w: announce with a server key; join the server with the node key first", ErrWrongKey)
	}
	if services == nil {
		services = map[string]string{}
	}
	for k, v := range services {
		if !validRole(k) || len(v) > 256 {
			return AnnounceResult{}, fmt.Errorf("%w: unknown role %q (known: %s)", ErrInvalid, k, strings.Join(Roles, ", "))
		}
	}
	if rep.Cores < 0 || rep.Cores > 4096 || rep.RAMMB < 0 || rep.DiskGB < 0 || rep.PortMbps < 0 || len(rep.IPv4) > 64 || len(rep.IPv6) > 64 {
		return AnnounceResult{}, ErrInvalid
	}
	if rep.IPv6 != "" {
		if a, err := netip.ParseAddr(rep.IPv6); err != nil || !a.Is6() {
			rep.IPv6 = ""
		}
	}
	if len(observedIP) > 64 {
		observedIP = ""
	}
	tag, err := s.pool.Exec(ctx, `UPDATE federation_servers SET services=$2, cores=$3, ram_mb=$4, disk_gb=$5, disk_ssd=$6,
		ipv4=$7, ipv6=$8, port_mbps=$9, observed_ip=$10, last_seen_at=now() WHERE id=$1`,
		c.Server, services, rep.Cores, rep.RAMMB, rep.DiskGB, rep.DiskSSD, rep.IPv4, rep.IPv6, rep.PortMbps, observedIP)
	if err != nil {
		return AnnounceResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return AnnounceResult{}, ErrNotFound
	}
	s.invalidateRouting() // a new address or a server back in rotation shows at once
	n, err := s.Get(ctx, c.Node)
	if err != nil {
		return AnnounceResult{}, err
	}
	res := AnnounceResult{Node: n}
	for _, sv := range n.Servers {
		if sv.ID == c.Server {
			res.Server = sv
			res.SelfTestDue = s.Req.SelfTestDue(sv, s.now())
		}
	}
	return res, nil
}

func validRole(k string) bool {
	for _, r := range Roles {
		if k == r {
			return true
		}
	}
	return false
}

// StartSelfTest reserves one timed transfer for the calling server in one
// direction ("down": home sends, "up": the server sends) and returns how many
// bytes to move. At most one per direction per SelfTestInterval: the test
// costs real bandwidth on both ends.
func (s *Service) StartSelfTest(ctx context.Context, c Caller, direction string) (serverID string, bytes int64, err error) {
	if c.Server == "" {
		return "", 0, ErrWrongKey
	}
	col := map[string]string{"down": "down_started_at", "up": "up_started_at"}[direction]
	if col == "" {
		return "", 0, ErrInvalid
	}
	var next time.Time
	err = s.pool.QueryRow(ctx, `UPDATE federation_servers SET `+col+`=now()
		WHERE id=$1 AND (`+col+` IS NULL OR `+col+` <= now() - $2::interval) RETURNING id`,
		c.Server, s.Req.SelfTestInterval.String()).Scan(&serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		if qerr := s.pool.QueryRow(ctx, `SELECT `+col+` + $2::interval FROM federation_servers WHERE id=$1`,
			c.Server, s.Req.SelfTestInterval.String()).Scan(&next); qerr != nil {
			return "", 0, ErrNotFound
		}
		return "", 0, fmt.Errorf("%w; next one after %s", ErrTooSoon, next.UTC().Format(time.RFC3339))
	}
	if err != nil {
		return "", 0, err
	}
	return serverID, s.Req.SelfTestBytes, nil
}

// RecordSelfTest stores what home measured. Only home calls it: a server can
// start a test but never write its own result.
func (s *Service) RecordSelfTest(ctx context.Context, serverID, direction string, mbps float64, rttMs int) error {
	if mbps < 0 || rttMs < 0 {
		return ErrInvalid
	}
	var q string
	args := []any{serverID, mbps}
	switch direction {
	case "down":
		q = `UPDATE federation_servers SET down_mbps=$2, down_at=now() WHERE id=$1`
	case "up":
		q = `UPDATE federation_servers SET up_mbps=$2, up_at=now(), rtt_ms=NULLIF($3::int, 0) WHERE id=$1`
		args = append(args, rttMs)
	default:
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetApproved flips the switch a person owns. Approving is refused while the
// node is below the minimum (or its waiver), naming every shortfall; removing
// approval always works.
func (s *Service) SetApproved(ctx context.Context, id string, approved bool) error {
	if approved {
		n, err := s.Get(ctx, id)
		if err != nil {
			return err
		}
		if len(n.Problems) > 0 {
			return fmt.Errorf("%w: %s", ErrNotCompliant, strings.Join(n.Problems, "; "))
		}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE federation_nodes SET approved=$2 WHERE id=$1`, id, approved)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateRouting()
	return nil
}

// SetWaiver waives the hardware figures for one node, with a reason and an
// optional expiry. An empty reason clears it. Approval, a fresh report, a
// public IPv4 and liveness still apply to a waived node.
func (s *Service) SetWaiver(ctx context.Context, id, reason string, until time.Time) error {
	reason = strings.TrimSpace(reason)
	if len(reason) > 200 || (reason == "" && !until.IsZero()) || (!until.IsZero() && !until.After(s.now())) {
		return ErrInvalid
	}
	var u any
	if !until.IsZero() {
		u = until
	}
	tag, err := s.pool.Exec(ctx, `UPDATE federation_nodes SET waiver_reason=$2, waiver_until=$3 WHERE id=$1`, id, reason, u)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateRouting()
	return nil
}

// List returns the nodes that may be handed out: approved and in rotation,
// test nodes included. Public answers use PublicList.
func (s *Service) List(ctx context.Context) ([]Node, error) {
	all, err := s.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, n := range all {
		if n.InRotation {
			out = append(out, n)
		}
	}
	return out, nil
}

// PublicList is List without test nodes: what /api/v1/regions shows.
func (s *Service) PublicList(ctx context.Context) ([]Node, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, n := range all {
		if !n.Test {
			out = append(out, n)
		}
	}
	return out, nil
}

// SetTest flags or unflags a test node.
func (s *Service) SetTest(ctx context.Context, id string, test bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE federation_nodes SET test=$2 WHERE id=$1`, id, test)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateRouting()
	return nil
}

// Get is one node, evaluated.
func (s *Service) Get(ctx context.Context, id string) (Node, error) {
	nodes, err := s.query(ctx, `WHERE id=$1`, id)
	if err != nil {
		return Node{}, err
	}
	if len(nodes) == 0 {
		return Node{}, ErrNotFound
	}
	return nodes[0], nil
}

// ListAll returns every node for the admin view, evaluated.
func (s *Service) ListAll(ctx context.Context) ([]Node, error) {
	return s.query(ctx, ``)
}

func (s *Service) query(ctx context.Context, where string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, country, region, approved, test, waiver_reason, coalesce(waiver_until, 'epoch'::timestamptz)
		FROM federation_nodes `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Node
	idx := map[string]int{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.Country, &n.Region, &n.Approved, &n.Test, &n.WaiverReason, &n.WaiverUntil); err != nil {
			rows.Close()
			return nil, err
		}
		n.WaiverUntil = unzero(n.WaiverUntil)
		idx[n.ID] = len(out)
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(out))
	for _, n := range out {
		ids = append(ids, n.ID)
	}
	srows, err := s.pool.Query(ctx, `SELECT id, node_id, name, services, cores, ram_mb, disk_gb, disk_ssd, ipv4, ipv6, port_mbps,
		observed_ip, coalesce(last_seen_at, 'epoch'::timestamptz), coalesce(down_mbps, 0), coalesce(down_at, 'epoch'::timestamptz),
		coalesce(up_mbps, 0), coalesce(up_at, 'epoch'::timestamptz), coalesce(rtt_ms, 0)
		FROM federation_servers WHERE node_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var v Server
		if err := srows.Scan(&v.ID, &v.NodeID, &v.Name, &v.Services, &v.Cores, &v.RAMMB, &v.DiskGB, &v.DiskSSD, &v.IPv4, &v.IPv6, &v.PortMbps,
			&v.ObservedIP, &v.LastSeen, &v.DownMbps, &v.DownAt, &v.UpMbps, &v.UpAt, &v.RTTMs); err != nil {
			return nil, err
		}
		v.LastSeen, v.DownAt, v.UpAt = unzero(v.LastSeen), unzero(v.DownAt), unzero(v.UpAt)
		i := idx[v.NodeID]
		out[i].Servers = append(out[i].Servers, v)
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}
	now := s.now()
	for i := range out {
		s.Req.evaluate(&out[i], now)
	}
	return out, nil
}

func unzero(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}
