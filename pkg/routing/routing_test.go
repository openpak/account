package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type fixture struct {
	Nodes []Node `json:"nodes"`
	Cases []struct {
		Name       string `json:"name"`
		Stack      string `json:"stack"`
		Token      string `json:"token"`
		Country    string `json:"country"`
		Tester     bool   `json:"tester"`
		WantNode   string `json:"want_node"`
		WantRegion string `json:"want_region"`
		WantVia    string `json:"want_via"`
	} `json:"cases"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFixtureCases(t *testing.T) {
	f := loadFixture(t)
	tbl := NewTable(f.Nodes)
	for _, c := range f.Cases {
		got := tbl.Resolve(Request{Stack: c.Stack, Token: c.Token, Country: c.Country, Tester: c.Tester})
		if got.Node != c.WantNode || got.Region != c.WantRegion || got.Via != c.WantVia {
			t.Errorf("%s: got node=%q region=%q via=%q, want node=%q region=%q via=%q",
				c.Name, got.Node, got.Region, got.Via, c.WantNode, c.WantRegion, c.WantVia)
		}
	}
}

// Home-only operation: with no nodes, and with no table at all, every
// question from every stack is answered by home.
func TestZeroNodesIsHome(t *testing.T) {
	f := loadFixture(t)
	for name, tbl := range map[string]*Table{"empty": NewTable(nil), "nil": nil} {
		for _, c := range f.Cases {
			got := tbl.Resolve(Request{Stack: c.Stack, Token: c.Token, Country: c.Country, Tester: true})
			if !got.IsHome() || got.Region != HomeRegion || got.Via != ViaHome {
				t.Errorf("%s table, %s: got %+v, want home", name, c.Name, got)
			}
		}
		if ads := tbl.Advertise(StackPhoton, "photon", true); len(ads) != 0 {
			t.Errorf("%s table advertises %v", name, ads)
		}
	}
	var c *Client
	if r := c.Resolve(Request{Stack: StackPhoton, Country: "BR"}); !r.IsHome() {
		t.Errorf("nil client: %+v", r)
	}
	if r := (&Client{}).Resolve(Request{Stack: StackPhoton, Country: "BR"}); !r.IsHome() {
		t.Errorf("unconfigured client: %+v", r)
	}
}

func TestAdvertise(t *testing.T) {
	tbl := NewTable(loadFixture(t).Nodes)
	ads := tbl.Advertise(StackPhoton, "photon_ping", false)
	if len(ads) != 1 || ads[0].Token != "sa" || ads[0].Address != "198.51.100.10:5055" {
		t.Fatalf("public photon adverts = %+v", ads)
	}
	ads = tbl.Advertise(StackPhoton, "photon", true)
	var toks []string
	for _, a := range ads {
		toks = append(toks, a.Token)
	}
	if strings.Join(toks, ",") != "us,sa,jp" {
		t.Fatalf("tester photon adverts = %v", toks)
	}
}

// Every alias the table can send is a name the vendor really uses: one
// canonical token per region per stack, never a home token re-listed.
func TestTokensAreKnown(t *testing.T) {
	photon := map[string]bool{"asia": true, "au": true, "cae": true, "eu": true, "hk": true, "in": true, "jp": true, "kr": true,
		"sa": true, "tr": true, "uae": true, "us": true, "usw": true, "ussc": true, "za": true}
	amongus := map[string]bool{"na": true, "eu": true, "as": true}
	seen := map[string]bool{}
	for _, r := range Builtin() {
		if seen[r.Code] {
			t.Errorf("duplicate region %s", r.Code)
		}
		seen[r.Code] = true
		for _, tok := range r.Aliases[StackPhoton] {
			if !photon[tok] {
				t.Errorf("%s: Photon token %q is not a Photon region", r.Code, tok)
			}
		}
		for _, tok := range r.Aliases[StackAmongUs] {
			if !amongus[tok] {
				t.Errorf("%s: Among Us region %q", r.Code, tok)
			}
		}
		for _, tok := range r.Aliases[StackPlayFab] {
			if len(tok) > 20 {
				t.Errorf("%s: PlayFab region %q longer than 20", r.Code, tok)
			}
		}
		for _, f := range r.Fallback {
			if f == "" {
				t.Errorf("%s: empty fallback", r.Code)
			}
		}
	}
	for _, r := range Builtin() {
		for _, f := range r.Fallback {
			if !seen[f] {
				t.Errorf("%s falls back to unknown %s", r.Code, f)
			}
		}
	}
	for c, route := range BuiltinCountryRoutes() {
		for _, code := range route {
			if !seen[code] {
				t.Errorf("country %s routes to unknown %s", c, code)
			}
		}
	}
	// No country belongs to two regions; China and Russia only through their routes.
	owner := map[string]string{}
	for _, r := range Builtin() {
		for _, c := range r.Countries {
			if o, ok := owner[c]; ok {
				t.Errorf("country %s in %s and %s", c, o, r.Code)
			}
			owner[c] = r.Code
		}
	}
	if owner["CN"] != "" || owner["RU"] != "" {
		t.Errorf("CN/RU must route through CountryRoutes, not own a region")
	}
}

func TestKeySpreadsWithinRegion(t *testing.T) {
	tbl := NewTable([]Node{
		{ID: "a", Region: "sa-east", Services: map[string]string{"photon": "a:1"}},
		{ID: "b", Region: "sa-east", Services: map[string]string{"photon": "b:1"}},
	})
	got := map[string]bool{}
	for _, k := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		got[tbl.Resolve(Request{Stack: StackPhoton, Country: "BR", Key: k}).Node] = true
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("keys did not spread: %v", got)
	}
	if tbl.Resolve(Request{Stack: StackPhoton, Country: "BR"}).Node != "a" {
		t.Fatal("no key should pick the first node")
	}
}

func TestClientCachesAndKeepsLastGood(t *testing.T) {
	fail := false
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(KeyHeader) != "k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		calls++
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case PathTable:
			_ = json.NewEncoder(w).Encode(NewTable([]Node{{ID: "br", Region: "sa-east", Services: map[string]string{"photon": "x:1"}}}))
		case PathAccount:
			_ = json.NewEncoder(w).Encode(Account{Country: "br", Tester: r.URL.Query().Get("subject") == "tester"})
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k"}
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if r := c.Resolve(Request{Stack: StackPhoton, Country: "BR"}); r.Node != "br" {
		t.Fatalf("resolve = %+v", r)
	}
	a := c.Account(ctx, AccountRef{Namespace: "switch", Subject: "tester"})
	if !a.Tester || a.Country != "BR" {
		t.Fatalf("account = %+v", a)
	}
	n := calls
	_ = c.Account(ctx, AccountRef{Namespace: "switch", Subject: "tester"})
	if calls != n {
		t.Fatal("account answer not cached")
	}
	fail = true
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("refresh against a failing core should error")
	}
	if r := c.Resolve(Request{Stack: StackPhoton, Country: "BR"}); r.Node != "br" {
		t.Fatalf("last good table lost: %+v", r)
	}
	if a := c.Account(ctx, AccountRef{AccountID: "other"}); a != (Account{}) {
		t.Fatalf("failed lookup = %+v, want zero", a)
	}
}
