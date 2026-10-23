package federation

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"openpak/account/pkg/routing"
)

// RoutingSource is what the routing endpoints read; *Service is one.
type RoutingSource interface {
	RoutingTable(ctx context.Context) (*routing.Table, error)
	AccountRouting(ctx context.Context, ref routing.AccountRef) (routing.Account, error)
}

// RoutingHandler serves the routing table to game servers at home:
//
//	GET /internal/v1/routing                 the table (registry + nodes in rotation, test nodes flagged)
//	GET /internal/v1/routing/account?...     {country, tester} for account_id=, or namespace=&subject=
//	GET /internal/v1/routing/resolve?...     one decision: stack, token, country, service, key,
//	                                         plus the account parameters above
//
// Every call needs one of keys in X-Routing-Key. The routing key reads this
// and nothing else, so a game server never holds the home key. With no keys
// configured the handler refuses everything.
func RoutingHandler(src RoutingSource, keys ...string) http.Handler {
	var allowed [][]byte
	for _, k := range keys {
		if k != "" {
			allowed = append(allowed, []byte(k))
		}
	}
	authed := func(r *http.Request) bool {
		got := []byte(r.Header.Get(routing.KeyHeader))
		ok := false
		for _, k := range allowed {
			if len(got) > 0 && subtle.ConstantTimeCompare(got, k) == 1 {
				ok = true
			}
		}
		return ok
	}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(v)
	}
	ref := func(r *http.Request) routing.AccountRef {
		q := r.URL.Query()
		return routing.AccountRef{AccountID: q.Get("account_id"), Namespace: q.Get("namespace"), Subject: q.Get("subject")}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routing.PathTable, func(w http.ResponseWriter, r *http.Request) {
		t, err := src.RoutingTable(r.Context())
		if err != nil {
			log.Printf("routing table: %v", err)
			http.Error(w, "routing unavailable", http.StatusBadGateway)
			return
		}
		write(w, t)
	})
	mux.HandleFunc("GET "+routing.PathAccount, func(w http.ResponseWriter, r *http.Request) {
		a, err := src.AccountRouting(r.Context(), ref(r))
		if err != nil {
			log.Printf("routing account: %v", err)
			http.Error(w, "routing unavailable", http.StatusBadGateway)
			return
		}
		write(w, a)
	})
	mux.HandleFunc("GET "+routing.PathResolve, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		req := routing.Request{Stack: q.Get("stack"), Token: q.Get("token"), Country: q.Get("country"),
			Service: q.Get("service"), Key: q.Get("key")}
		// Never an error: a core that cannot read its own tables answers home.
		t, err := src.RoutingTable(r.Context())
		if err != nil {
			log.Printf("routing resolve: %v", err)
			t = nil
		}
		var tester bool
		if rf := ref(r); rf.AccountID != "" || rf.Subject != "" {
			if a, err := src.AccountRouting(r.Context(), rf); err == nil {
				tester = a.Tester
				if req.Country == "" {
					req.Country = a.Country
				}
			}
		}
		req.Tester = tester
		res := t.Resolve(req)
		write(w, struct {
			routing.Result
			Country string `json:"country,omitempty"`
			Tester  bool   `json:"tester,omitempty"`
		}{res, strings.ToUpper(req.Country), tester})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "routing key required", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
