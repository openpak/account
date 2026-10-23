package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"openpak/account/pkg/routing"
)

type fakeSource struct {
	nodes    []Node
	accounts map[string]routing.Account // by account id or "ns/subject"
	fail     bool
}

func (f fakeSource) RoutingTable(context.Context) (*routing.Table, error) {
	if f.fail {
		return nil, errors.New("db down")
	}
	return TableFrom(f.nodes), nil
}

func (f fakeSource) AccountRouting(_ context.Context, r routing.AccountRef) (routing.Account, error) {
	if r.AccountID != "" {
		return f.accounts[r.AccountID], nil
	}
	return f.accounts[r.Namespace+"/"+r.Subject], nil
}

func get(t *testing.T, h http.Handler, key, url string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set(routing.KeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code
}

func TestRoutingHandler(t *testing.T) {
	now := time.Now()
	src := fakeSource{
		nodes: []Node{
			{ID: "br-gru-1", Region: "sa-east", Country: "BR", InRotation: true, LastSeen: now,
				Services: map[string]string{"photon": "198.51.100.10:5056", "turn": "198.51.100.10:3478"}},
			{ID: "jp-test", Region: "ap-northeast", Country: "JP", InRotation: true, Test: true,
				Services: map[string]string{"photon": "198.51.100.30:5056"}},
			{ID: "us-down", Region: "us-east", Country: "US", InRotation: false,
				Services: map[string]string{"photon": "198.51.100.20:5056"}},
		},
		accounts: map[string]routing.Account{
			"switch/owner-baas": {Country: "BR", Tester: true},
			"acc-1":             {Country: "JP"},
		},
	}
	h := RoutingHandler(src, "rk")

	if c := get(t, h, "", routing.PathTable, nil); c != http.StatusUnauthorized {
		t.Fatalf("no key: %d", c)
	}
	if c := get(t, h, "wrong", routing.PathTable, nil); c != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", c)
	}
	if c := get(t, RoutingHandler(src), "", routing.PathTable, nil); c != http.StatusUnauthorized {
		t.Fatalf("unconfigured handler must refuse: %d", c)
	}

	var tbl routing.Table
	if c := get(t, h, "rk", routing.PathTable, &tbl); c != 200 {
		t.Fatalf("table: %d", c)
	}
	if len(tbl.Nodes) != 2 || tbl.Home != routing.HomeRegion || len(tbl.Regions) == 0 {
		t.Fatalf("table = %+v", tbl)
	}

	var acc routing.Account
	get(t, h, "rk", routing.PathAccount+"?namespace=switch&subject=owner-baas", &acc)
	if !acc.Tester || acc.Country != "BR" {
		t.Fatalf("account = %+v", acc)
	}

	var res struct {
		routing.Result
		Tester bool `json:"tester"`
	}
	get(t, h, "rk", routing.PathResolve+"?stack=photon&token=jp/*&account_id=acc-1", &res)
	if !res.IsHome() || res.Via != routing.ViaHome {
		t.Fatalf("non-tester explicit jp = %+v", res)
	}
	get(t, h, "rk", routing.PathResolve+"?stack=photon&token=jp/*&namespace=switch&subject=owner-baas", &res)
	if res.Node != "jp-test" || !res.Tester {
		t.Fatalf("tester explicit jp = %+v", res)
	}
	get(t, h, "rk", routing.PathResolve+"?stack=photon&namespace=switch&subject=owner-baas", &res)
	if res.Node != "br-gru-1" || res.Via != routing.ViaCountry {
		t.Fatalf("tester by country = %+v", res)
	}

	// A core that cannot read its tables still answers, with home.
	bad := RoutingHandler(fakeSource{fail: true}, "rk")
	if c := get(t, bad, "rk", routing.PathTable, nil); c != http.StatusBadGateway {
		t.Fatalf("table on failure: %d", c)
	}
	var home routing.Result
	if c := get(t, bad, "rk", routing.PathResolve+"?stack=photon&country=BR", &home); c != 200 || !home.IsHome() || home.Via != routing.ViaHome {
		t.Fatalf("resolve on failure: %d %+v", c, home)
	}
}

// Nodes out of rotation never reach the table; with none in rotation every
// stack is served by home.
func TestTableFromZeroNodes(t *testing.T) {
	tbl := TableFrom([]Node{{ID: "x", Region: "sa-east", InRotation: false, Services: map[string]string{"photon": "a:1"}}})
	if len(tbl.Nodes) != 0 {
		t.Fatal("node out of rotation in the table")
	}
	for _, st := range []string{"photon", "amongus", "playfab", "npln", "gcp"} {
		if r := tbl.Resolve(routing.Request{Stack: st, Country: "BR", Token: "sa", Tester: true}); !r.IsHome() {
			t.Errorf("%s: %+v", st, r)
		}
	}
}
