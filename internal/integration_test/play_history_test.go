//go:build integration

package integration_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// A friend's play history is per title: how many sessions, how long, first and last played, newest
// title first, finished sessions plus the one running now, only the namespace asked for.
func TestPlayHistory(t *testing.T) {
	_, _, _, sc, _, _, ident := startGRPC(t)
	ctx := authCtx()
	pool, err := storage.Connect(context.Background(), testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a, _, err := ident.Register(ctx, "history@example.com", "correct horse battery", "Player")
	if err != nil {
		t.Fatal(err)
	}
	const older, newer = "0100c2500fc20000", "0100a5a020d5e000"
	insert := func(ns, title, started, ended string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `INSERT INTO play_sessions
			(account_id, namespace, title_id, client, os, started_at, ended_at)
			VALUES ($1, $2, $3, 'harbor', 'linux', now() - $4::interval, now() - $5::interval)`,
			a.ID, ns, title, started, ended); err != nil {
			t.Fatal(err)
		}
	}
	insert("switch", older, "10 days", "10 days" /* zero length */)
	insert("switch", older, "9 days 2 hours", "9 days") // 2 h
	insert("switch", newer, "3 hours", "2 hours")       // 1 h
	insert("switch", newer, "30 minutes", "0 minutes")  // 30 min
	insert("np", "NPUB00000", "1 day", "23 hours")      // another namespace: not in the answer

	r, err := sc.GetPlayHistory(ctx, &accountv1.GetPlayHistoryRequest{AccountId: a.ID, Namespace: "switch"})
	if err != nil || len(r.GetEntries()) != 2 {
		t.Fatalf("history: %v %v", r, err)
	}
	first, second := r.GetEntries()[0], r.GetEntries()[1]
	if first.GetTitleId() != newer || second.GetTitleId() != older {
		t.Fatalf("order: %s then %s, want the most recently played first", first.GetTitleId(), second.GetTitleId())
	}
	if first.GetSessions() != 2 || first.GetTotalSeconds() < 5390 || first.GetTotalSeconds() > 5410 {
		t.Fatalf("newer: %+v, want 2 sessions and about 5400 s", first)
	}
	if second.GetSessions() != 2 || second.GetTotalSeconds() < 7190 || second.GetTotalSeconds() > 7210 {
		t.Fatalf("older: %+v, want 2 sessions and about 7200 s", second)
	}
	if first.GetFirstPlayedUnix() >= first.GetLastPlayedUnix() || second.GetFirstPlayedUnix() >= second.GetLastPlayedUnix() {
		t.Fatalf("first must precede last: %+v %+v", first, second)
	}
	if one, err := sc.GetPlayHistory(ctx, &accountv1.GetPlayHistoryRequest{AccountId: a.ID, Namespace: "switch", Limit: 1}); err != nil || len(one.GetEntries()) != 1 {
		t.Fatalf("limit: %v %v", one, err)
	}
	if _, err := sc.GetPlayHistory(ctx, &accountv1.GetPlayHistoryRequest{AccountId: "not-a-uuid", Namespace: "switch"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad account id: %v", err)
	}
	if none, err := sc.GetPlayHistory(ctx, &accountv1.GetPlayHistoryRequest{AccountId: "00000000-0000-4000-8000-000000000000", Namespace: "switch"}); err != nil || len(none.GetEntries()) != 0 {
		t.Fatalf("unknown account is an empty history: %v %v", none, err)
	}
}
