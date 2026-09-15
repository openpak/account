//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"openpak/account/internal/events"
	"openpak/account/internal/identity"
	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// readOneVia waits for one event on ch, failing the test with what on timeout.
func readOneVia(t *testing.T, ch chan *accountv1.InvalidationEvent, what string) *accountv1.InvalidationEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// TestSubscribeAccountEvents covers universal-social-prd US-1: the stream is
// the same events the poll returns, in version order, with catch-up for
// events emitted before the subscription started and liveness for events
// emitted after it. The poll stays untouched; a subscriber and a poller see
// the same facts.
func TestSubscribeAccountEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	evSvc := events.New(pool)
	ident := identity.New(pool, "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, nil)
	acctA, _, err := ident.Register(ctx, "sub-a@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	acctB, _, err := ident.Register(ctx, "sub-b@example.com", "correct horse battery", "B")
	if err != nil {
		t.Fatal(err)
	}
	a, b := acctA.ID, acctB.ID

	// Two events before anyone subscribes: a subscriber starting at zero
	// must catch up on them.
	if err := evSvc.Emit(ctx, "friend_requested", a, "", b); err != nil {
		t.Fatal(err)
	}
	if err := evSvc.Emit(ctx, "friend_accepted", b, "", a); err != nil {
		t.Fatal(err)
	}

	streamCtx, stop := context.WithCancel(context.Background())
	got := make(chan *accountv1.InvalidationEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- evSvc.Subscribe(streamCtx, 0, func(ev *accountv1.InvalidationEvent) error {
			got <- ev
			return nil
		})
	}()

	readOne := func(what string) *accountv1.InvalidationEvent {
		t.Helper()
		select {
		case ev := <-got:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatalf("subscribe: timed out waiting for %s", what)
			return nil
		}
	}

	first := readOne("first catch-up event")
	if first.GetType() != "friend_requested" || first.GetAccountId() != a || first.GetSubjectId() != b {
		t.Fatalf("first event: %+v", first)
	}
	if first.GetEventId() == "" || first.GetVersion() == 0 {
		t.Fatalf("event id/version must be set: %+v", first)
	}
	second := readOne("second catch-up event")
	if second.GetType() != "friend_accepted" || second.GetVersion() <= first.GetVersion() {
		t.Fatalf("second event: %+v", second)
	}

	// A live event, emitted after the subscriber is parked in its wait, must
	// arrive without anyone re-polling on its behalf. The 250 ms safety tick
	// is the upper bound; assert well above it so the test proves the wake,
	// not the tick.
	time.Sleep(400 * time.Millisecond)
	before := time.Now()
	if err := evSvc.Emit(ctx, "friend_removed", a, "", b); err != nil {
		t.Fatal(err)
	}
	third := readOne("live event")
	if third.GetType() != "friend_removed" {
		t.Fatalf("third event: %+v", third)
	}
	if d := time.Since(before); d > 200*time.Millisecond {
		t.Fatalf("live delivery took %v; the wake path is broken", d)
	}

	// A mid-stream subscriber starts at its own version, not at zero.
	_, err = evSvc.EmitEvent(ctx, "message", a, "", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	sub2Ctx, stop2 := context.WithCancel(context.Background())
	got2 := make(chan *accountv1.InvalidationEvent, 16)
	done2 := make(chan error, 1)
	go func() {
		done2 <- evSvc.Subscribe(sub2Ctx, third.GetVersion(), func(ev *accountv1.InvalidationEvent) error {
			got2 <- ev
			return nil
		})
	}()
	ev := readOneVia(t, got2, "mid-stream subscriber")
	if ev.GetType() != "message" || ev.GetSubjectId() != "msg-1" {
		t.Fatalf("mid-stream first event: %+v", ev)
	}
	stop2()
	sub2CtxDone := <-done2
	if sub2CtxDone != nil && sub2CtxDone.Error() != "context canceled" {
		t.Fatalf("second subscriber stop: %v", sub2CtxDone)
	}

	stop()
	if err := <-done; err != nil && err.Error() != "context canceled" {
		t.Fatalf("subscriber stop: %v", err)
	}
}

// TestEmitEventRejectsUnknownType pins the closed set: an internal caller
// cannot invent an event type the database constraint would refuse anyway.
func TestEmitEventRejectsUnknownType(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	evSvc := events.New(pool)
	if _, err := evSvc.EmitEvent(ctx, "made_up_kind", "00000000-0000-0000-0000-000000000001", "", ""); err == nil {
		t.Fatal("expected unknown type to be rejected")
	}
	ev, err := evSvc.EmitEvent(ctx, "message", "00000000-0000-0000-0000-000000000002", "", "msg-9")
	if err != nil {
		t.Fatal(err)
	}
	if ev.GetType() != "message" || ev.GetVersion() == 0 || ev.GetEventId() == "" {
		t.Fatalf("emitted event: %+v", ev)
	}
}
