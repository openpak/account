//go:build integration

package integration_test

import (
	"context"
	"testing"

	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// Playtime runs from a session's first game-server login (MarkOnline) to its end:
// presence alone counts nothing however long the title is open, a second login
// in the same session does not restart the clock, a reconnect after a dead lease
// is a new session that needs its own login, and an expiry carries the time into
// the record.
func TestPlaytimeFromGameServerLogin(t *testing.T) {
	_, _, _, sc, _, _, ident := startGRPC(t)
	ctx := authCtx()
	pool, err := storage.Connect(context.Background(), testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a, _, err := ident.Register(ctx, "playtime@example.com", "correct horse battery", "Player")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100C2500FC20000"
	register := func() string {
		t.Helper()
		r, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
			AccountId: a.ID, Namespace: "switch", TitleId: title, EndpointRef: "dev:1", Client: "ryujinx", Os: "macos"})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSessionId()
	}
	login := func(want int32) {
		t.Helper()
		r, err := sc.MarkOnline(ctx, &accountv1.MarkOnlineRequest{Players: []*accountv1.OnlinePlayer{
			{AccountId: a.ID, Namespace: "switch"},
			{AccountId: "not-a-uuid", Namespace: "switch"},
		}})
		if err != nil || r.GetMarked() != want {
			t.Fatalf("mark: %v %v, want %d", r, err, want)
		}
	}
	// minutes pretends the live session reached the game server that long ago and was renewed just now.
	minutes := func(m int) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET online_since = now() - make_interval(mins => $2), updated_at = now()
			WHERE account_id = $1 AND expires_at > now()`, a.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	tallies := func() []*accountv1.PlaytimeTally {
		t.Helper()
		r, err := sc.GetPlaytime(ctx, &accountv1.GetPlaytimeRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetTallies()
	}

	login(0) // no presence yet: nothing to mark
	register()
	if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET created_at = now() - interval '2 hours' WHERE account_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := tallies(); len(got) != 0 {
		t.Fatalf("two hours on the title screen counted: %+v", got)
	}

	login(1)
	minutes(20)
	login(1) // a second login does not restart the clock
	got := tallies()
	if len(got) != 1 || got[0].GetOs() != "macos" || got[0].GetClient() != "ryujinx" || got[0].GetLongestSeconds() < 20*60 {
		t.Fatalf("after login: %+v", got)
	}

	// The lease runs out and the game reconnects: a new session, not counting
	// until it reaches a game server again; the old one's time is kept.
	if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	id := register()
	if got = tallies(); len(got) != 1 || got[0].GetSessions() != 1 {
		t.Fatalf("reconnect counted before a login: %+v", got)
	}
	login(1)
	minutes(10)
	if got = tallies(); len(got) != 1 || got[0].GetSessions() != 2 || got[0].GetTotalSeconds() < 30*60 || got[0].GetLongestSeconds() >= 21*60 {
		t.Fatalf("after reconnect: %+v", got)
	}

	// Expiry writes the live one out; totals stay.
	if _, err := sc.ExpireSession(ctx, &accountv1.ExpireSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	var n, secs int
	_ = pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(online_seconds), 0) FROM play_sessions`).Scan(&n, &secs)
	if got = tallies(); n != 2 || secs < 30*60 || len(got) != 1 || got[0].GetTotalSeconds() != int64(secs) {
		t.Fatalf("after expiry: %d rows %d s, %+v", n, secs, got)
	}
}
