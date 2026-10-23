// Package routing decides which region node serves a new room, and is the
// one copy of that rule every OpenPak game server uses.
//
// The decision (federation-worldwide-plan.md §1.1, owner decisions 2026-10-23):
//
//  1. An explicit in-game region always wins, wherever the player is: the
//     stack's token (Photon "jp", Among Us "na", PlayFab "BrazilSouth") names
//     a region. A token shared by several regions (Among Us "na") is refined
//     by the account country inside that set.
//  2. With no choice, the account country picks the region.
//  3. A region with no node walks its fallback chain, and every chain ends at
//     home. An unknown token, no country, no table or a failed lookup all
//     mean home. Nothing here ever returns an error to a console.
//
// Test nodes are invisible to everyone but allow-listed accounts (Tester).
// Joiners never come through here: they go to the room's node.
//
// The package is standard library only, so a game server can vendor or copy
// it without pulling in the account core.
package routing

import (
	"hash/fnv"
	"sort"
	"strings"
)

// Region is one entry of the registry.
type Region struct {
	Code      string              `json:"code"`
	Name      string              `json:"name"`
	Countries []string            `json:"countries"`
	Fallback  []string            `json:"fallback,omitempty"` // ordered; home is implied at the end
	Aliases   map[string][]string `json:"aliases"`            // stack -> its names for this region, canonical first
}

// Node is a node in rotation as the core hands it to game servers. Test
// nodes are included here (the routing table is internal) and filtered by
// Resolve; they never reach the public /api/v1/regions.
type Node struct {
	ID       string            `json:"id"`
	Region   string            `json:"region"`
	Country  string            `json:"country"`
	Services map[string]string `json:"services"`
	Test     bool              `json:"test,omitempty"`
}

// Table is everything a game server needs to place rooms: the registry and
// the nodes in rotation right now.
type Table struct {
	Home          string              `json:"home"` // the region home serves itself
	Regions       []Region            `json:"regions"`
	CountryRoutes map[string][]string `json:"country_routes,omitempty"`
	Nodes         []Node              `json:"nodes"`
}

// NewTable is the built-in registry with the given nodes.
func NewTable(nodes []Node) *Table {
	return &Table{Home: HomeRegion, Regions: Builtin(), CountryRoutes: BuiltinCountryRoutes(), Nodes: nodes}
}

// Request is one placement question.
type Request struct {
	Stack   string // StackPhoton, StackAmongUs, ...
	Token   string // the explicit in-game choice in the stack's own names; "" = none
	Country string // the account country (ISO 3166-1 alpha-2); "" = unknown
	Tester  bool   // the account is on the test-node allow-list
	Service string // node role the room needs; "" = DefaultService[Stack]
	Key     string // spreads rooms over a region's nodes (room code, session id); "" = first node
}

// How a result was reached.
const (
	ViaExplicit = "explicit" // the in-game choice's region had a node
	ViaCountry  = "country"  // the account country's region had a node
	ViaFallback = "fallback" // a region further down the chain had one
	ViaHome     = "home"     // nothing did, or there was nothing to go on
)

// Result is where the room goes. Node "" is home.
type Result struct {
	Region   string            `json:"region"`
	Node     string            `json:"node,omitempty"`
	Address  string            `json:"address,omitempty"` // the node's address for the requested service
	Services map[string]string `json:"services,omitempty"`
	Via      string            `json:"via"`
}

// IsHome reports whether home serves the room.
func (r Result) IsHome() bool { return r.Node == "" }

func (t *Table) home() Result {
	code := HomeRegion
	if t != nil && t.Home != "" {
		code = t.Home
	}
	return Result{Region: code, Via: ViaHome}
}

// NormalizeToken turns a client's region string into a table token: Photon
// sends "sa/*" or "us/ussc", hosts send "na.mm.among.us".
func NormalizeToken(stack, token string) string {
	token = strings.TrimSpace(token)
	if i := strings.IndexByte(token, '/'); i >= 0 {
		token = token[:i]
	}
	if stack != StackPlayFab { // Azure names are compared case-insensitively below anyway
		token = strings.ToLower(token)
	}
	return token
}

// RegionsForToken lists the regions a stack's token names, primary first.
func (t *Table) RegionsForToken(stack, token string) []string {
	if t == nil {
		return nil
	}
	token = NormalizeToken(stack, token)
	if token == "" {
		return nil
	}
	var out []string
	for _, r := range t.Regions {
		for _, a := range r.Aliases[stack] {
			if strings.EqualFold(a, token) {
				out = append(out, r.Code)
				break
			}
		}
	}
	return out
}

// RegionOfCountry is the region a country belongs to, or "".
func (t *Table) RegionOfCountry(country string) string {
	if t == nil {
		return ""
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		return ""
	}
	for _, r := range t.Regions {
		for _, c := range r.Countries {
			if c == country {
				return r.Code
			}
		}
	}
	return ""
}

// CountryRoute is the ordered regions an account country starts from.
func (t *Table) CountryRoute(country string) []string {
	if t == nil {
		return nil
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if r, ok := t.CountryRoutes[country]; ok {
		return append([]string(nil), r...)
	}
	if c := t.RegionOfCountry(country); c != "" {
		return []string{c}
	}
	return nil
}

func (t *Table) region(code string) *Region {
	for i := range t.Regions {
		if t.Regions[i].Code == code {
			return &t.Regions[i]
		}
	}
	return nil
}

// Chain expands starting regions with their fallbacks, transitively, in
// order and without repeats. Home is not listed; it is always the last stop.
func (t *Table) Chain(start []string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(start); i++ {
		c := start[i]
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if r := t.region(c); r != nil {
			start = append(start, r.Fallback...)
		}
	}
	return out
}

// NodeRegion is the region a node serves: its own code, or its country's
// for a node registered before regions existed.
func (t *Table) NodeRegion(n Node) string {
	if n.Region != "" {
		return n.Region
	}
	return t.RegionOfCountry(n.Country)
}

// Eligible lists the nodes of a region that run service and that this
// caller may be sent to, in a stable order. A tester gets the test nodes
// first when the region has any: that is what they are there to try.
func (t *Table) Eligible(region, service string, tester bool) []Node {
	if t == nil {
		return nil
	}
	var public, test []Node
	for _, n := range t.Nodes {
		if t.NodeRegion(n) != region || n.Services[service] == "" {
			continue
		}
		if n.Test {
			if tester {
				test = append(test, n)
			}
			continue
		}
		public = append(public, n)
	}
	out := public
	if len(test) > 0 {
		out = test
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resolve places a new room. It never fails: anything it cannot use ends at home.
func (t *Table) Resolve(req Request) Result {
	if t == nil {
		return t.home()
	}
	service := req.Service
	if service == "" {
		service = DefaultService[req.Stack]
	}
	if service == "" {
		return t.home()
	}
	route := t.CountryRoute(req.Country)

	var start []string
	via := ViaCountry
	if NormalizeToken(req.Stack, req.Token) != "" {
		cands := t.RegionsForToken(req.Stack, req.Token)
		if len(cands) == 0 {
			return t.home() // a name we do not know: never guess
		}
		via = ViaExplicit
		pick := cands[0]
	refine:
		for _, c := range route {
			for _, k := range cands {
				if c == k {
					pick = k
					break refine
				}
			}
		}
		start = []string{pick}
	} else {
		start = route
	}

	for i, code := range t.Chain(start) {
		if code == t.home().Region {
			return t.home() // home serves its own region
		}
		nodes := t.Eligible(code, service, req.Tester)
		if len(nodes) == 0 {
			continue
		}
		n := nodes[0]
		if req.Key != "" {
			h := fnv.New32a()
			h.Write([]byte(req.Key))
			n = nodes[h.Sum32()%uint32(len(nodes))]
		}
		v := via
		if i >= len(start) {
			v = ViaFallback
		}
		return Result{Region: code, Node: n.ID, Address: n.Services[service], Services: n.Services, Via: v}
	}
	return t.home()
}

// Advert is one region a stack may list to a client (Photon GetRegions,
// PlayFab QoS servers, Splatoon 3 latency candidates).
type Advert struct {
	Token   string // the stack's canonical name for the region
	Region  string
	Node    string // "" = home
	Address string // the node's address for the requested service
}

// Advertise lists, for each region that has a node this caller may use
// running service, the stack's canonical token and that node's address. Home
// is not included: each stack lists home its own way (Photon always lists
// "eu" first). A token already taken by an earlier region is skipped, so a
// client never sees one name twice.
func (t *Table) Advertise(stack, service string, tester bool) []Advert {
	if t == nil {
		return nil
	}
	taken := map[string]bool{}
	if h := t.region(t.home().Region); h != nil {
		for _, a := range h.Aliases[stack] {
			taken[strings.ToLower(a)] = true
		}
	}
	var out []Advert
	for _, r := range t.Regions {
		if r.Code == t.home().Region {
			continue
		}
		names := r.Aliases[stack]
		if len(names) == 0 || taken[strings.ToLower(names[0])] {
			continue
		}
		nodes := t.Eligible(r.Code, service, tester)
		if len(nodes) == 0 {
			continue
		}
		taken[strings.ToLower(names[0])] = true
		out = append(out, Advert{Token: names[0], Region: r.Code, Node: nodes[0].ID, Address: nodes[0].Services[service]})
	}
	return out
}

// Node is the node with this id, if the table still has it in rotation.
func (t *Table) Node(id string) (Node, bool) {
	if t == nil {
		return Node{}, false
	}
	for _, n := range t.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}
