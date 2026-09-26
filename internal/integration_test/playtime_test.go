//go:build integration

package integration_test

import (
	"context"
	"testing"

	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// Playtime is time a game server confirmed, not time a title sat open: presence
// alone counts nothing, each MarkOnline adds the time since the previous one (a
// long gap adds nothing), the OS rides with the session, a reconnect after a dead
// lease is a new session, and an expiry carries the online time into the record.
func TestPlaytimeFromGameServerMarks(t *testing.T) {
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
	mark := func(want int32) {
		t.Helper()
		r, err := sc.MarkOnline(ctx, &accountv1.MarkOnlineRequest{Players: []*accountv1.OnlinePlayer{
			{AccountId: a.ID, Namespace: "switch"},
			{AccountId: "not-a-uuid", Namespace: "switch"},
		}})
		if err != nil || r.GetMarked() != want {
			t.Fatalf("mark: %v %v, want %d", r, err, want)
		}
	}
	// ago pretends the previous mark was some seconds back.
	ago := func(secs int) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET online_seen_at = now() - make_interval(secs => $2)
			WHERE account_id = $1 AND expires_at > now()`, a.ID, secs); err != nil {
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

	mark(0) // no presence yet: nothing to mark
	register()
	if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET created_at = now() - interval '2 hours' WHERE account_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := tallies(); len(got) != 0 {
		t.Fatalf("two hours on the title screen counted: %+v", got)
	}

	mark(1) // starts the clock
	ago(60)
	mark(1) // +60
	ago(600)
	mark(1) // a ten-minute gap is not one stretch online: +0
	ago(30)
	mark(1) // +30
	got := tallies()
	if len(got) != 1 || got[0].GetOs() != "macos" || got[0].GetClient() != "ryujinx" || got[0].GetTotalSeconds() < 90 || got[0].GetTotalSeconds() > 95 {
		t.Fatalf("after marks: %+v", got)
	}

	// The lease runs out and the game reconnects: a new session, the old one's
	// online time is kept.
	if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	id := register()
	mark(1)
	ago(45)
	mark(1)
	got = tallies()
	if len(got) != 1 || got[0].GetSessions() != 2 || got[0].GetTotalSeconds() < 135 || got[0].GetLongestSeconds() > 95 {
		t.Fatalf("after reconnect: %+v", got)
	}

	// Expiry writes the live one out; totals stay.
	if _, err := sc.ExpireSession(ctx, &accountv1.ExpireSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	var n, secs int
	_ = pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(online_seconds), 0) FROM play_sessions`).Scan(&n, &secs)
	if got = tallies(); n != 2 || secs < 135 || len(got) != 1 || got[0].GetTotalSeconds() != int64(secs) {
		t.Fatalf("after expiry: %d rows %d s, %+v", n, secs, got)
	}
}
