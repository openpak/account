//go:build integration

package integration_test

import (
	"testing"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// Cross-network presence: the client label rides from RegisterSession to
// GetPresence, a titled session beats a title-less one whatever the order, and
// being signed in on two networks at once is two sessions rather than one row
// two adapters take from each other.
func TestPresenceClientAndPreference(t *testing.T) {
	_, _, _, sc, _, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "presence-client@example.com", "correct horse battery", "Label")
	if err != nil {
		t.Fatal(err)
	}
	presence := func() *accountv1.Presence {
		t.Helper()
		pr, err := sc.GetPresence(ctx, &accountv1.GetPresenceRequest{AccountIds: []string{a.ID}})
		if err != nil || len(pr.GetPresence()) != 1 {
			t.Fatalf("presence: %v %v", pr, err)
		}
		return pr.GetPresence()[0]
	}

	// A Wii U, signed in, nothing running.
	wiiu, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "wiiu", TitleId: "", EndpointRef: "pid:1", Client: "cemu"})
	if err != nil {
		t.Fatal(err)
	}
	if p := presence(); p.GetClient() != "cemu" || p.GetNamespace() != "wiiu" {
		t.Fatalf("client round trip: %+v", p)
	}

	// Then Ryujinx in a game: the titled session wins.
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: "01004D300C5AE000", EndpointRef: "dev:1", Client: "ryujinx"}); err != nil {
		t.Fatal(err)
	}
	// And afterwards a title-less Switch session (a login): newer, but title-less,
	// so it must not hide the game.
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: "", EndpointRef: "dev:1", Client: "ryujinx"}); err != nil {
		t.Fatal(err)
	}
	if p := presence(); p.GetTitleId() != "01004D300C5AE000" || p.GetClient() != "ryujinx" || p.GetNamespace() != "switch" {
		t.Fatalf("titled session should win: %+v", p)
	}

	// The Switch title-less registration did not take the Wii U's row.
	if _, err := sc.Heartbeat(ctx, &accountv1.HeartbeatRequest{SessionId: wiiu.GetSessionId()}); err != nil {
		t.Fatalf("wiiu session replaced by another namespace: %v", err)
	}

	// Re-registering the same key updates the client.
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: "01004D300C5AE000", EndpointRef: "dev:2", Client: "switch"}); err != nil {
		t.Fatal(err)
	}
	if p := presence(); p.GetClient() != "switch" {
		t.Fatalf("client not updated on replace: %+v", p)
	}

	// An absurd client is refused rather than stored.
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: "x", EndpointRef: "e", Client: string(long)}); err == nil {
		t.Fatal("65-byte client accepted")
	}
}
