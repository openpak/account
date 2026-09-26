//go:build integration

package integration_test

import (
	"context"
	"testing"

	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// Playtime: the OS rides with the session, a live session counts from its
// start to its last renewal, a reconnect after the lease ran out is a second
// session rather than one long one, and an explicit expiry writes it out.
func TestPlaytimeFromSessions(t *testing.T) {
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
	register := func(os string) string {
		t.Helper()
		r, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
			AccountId: a.ID, Namespace: "switch", TitleId: title, EndpointRef: "dev:1", Client: "ryujinx", Os: os})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSessionId()
	}
	// played pretends the live session started minutes ago and was renewed just now.
	played := func(minutes int) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions
			SET created_at = now() - make_interval(mins => $2), updated_at = now()
			WHERE account_id = $1 AND expires_at > now()`, a.ID, minutes); err != nil {
			t.Fatal(err)
		}
	}
	tally := func() *accountv1.PlaytimeTally {
		t.Helper()
		r, err := sc.GetPlaytime(ctx, &accountv1.GetPlaytimeRequest{})
		if err != nil || len(r.GetTallies()) != 1 {
			t.Fatalf("playtime: %v %v", r, err)
		}
		return r.GetTallies()[0]
	}

	register("macos")
	played(20)
	if tl := tally(); tl.GetOs() != "macos" || tl.GetClient() != "ryujinx" || tl.GetSessions() != 1 || tl.GetLongestSeconds() < 20*60 {
		t.Fatalf("live session: %+v", tl)
	}
	pr, _ := sc.GetPresence(ctx, &accountv1.GetPresenceRequest{AccountIds: []string{a.ID}})
	if pr.GetPresence()[0].GetOs() != "macos" {
		t.Fatalf("presence os: %+v", pr.GetPresence())
	}

	// The lease runs out, then the game reconnects: a new session starts.
	if _, err := pool.Exec(context.Background(), `UPDATE adapter_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	id := register("macos")
	played(10)
	if tl := tally(); tl.GetSessions() != 2 || tl.GetLongestSeconds() >= 21*60 || tl.GetTotalSeconds() < 30*60 {
		t.Fatalf("after reconnect: %+v", tl)
	}

	// Expiry moves the live one into play_sessions; the totals stay.
	if _, err := sc.ExpireSession(ctx, &accountv1.ExpireSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM play_sessions`).Scan(&n)
	if tl := tally(); n != 2 || tl.GetSessions() != 2 || tl.GetTotalSeconds() < 30*60 {
		t.Fatalf("after expiry: %d rows, %+v", n, tl)
	}

	// An OS nobody ships is stored as none rather than refused.
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: "", EndpointRef: "dev:1", Client: "ryujinx", Os: "beos"}); err != nil {
		t.Fatalf("odd os refused: %v", err)
	}
}
