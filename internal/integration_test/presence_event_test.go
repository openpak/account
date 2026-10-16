//go:build integration

package integration_test

import (
	"context"
	"openpak/account/internal/events"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"
	"testing"
	"time"
)

// A session beginning or ending tells the networks where the person's friends are online, and only those.
func TestPresenceChangedGoesToFriendsOnlineElsewhere(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	reg := func(email, name string) string {
		a, _, err := ident.Register(ctx, email, "correct horse battery", name)
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	me, leia, offline := reg("me@example.org", "Me"), reg("leia@example.org", "Leia"), reg("off@example.org", "Off")
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	soc, evs, sess := social.New(pool, nil), events.New(pool), sessions.New(pool)
	for _, f := range []string{leia, offline} {
		if _, err := soc.RequestFriend(ctx, me, f, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := soc.AcceptFriend(ctx, f, me, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Leia is on the Switch; the other friend is online nowhere.
	if _, _, err := sess.Register(ctx, leia, "switch", "", "ref", "home", time.Minute, "switch", ""); err != nil {
		t.Fatal(err)
	}
	sess.OnChange = func(id string) {
		if err := evs.PresenceChanged(ctx, id); err != nil {
			t.Error(err)
		}
	}
	// I come online on the PS3.
	if _, _, err := sess.Register(ctx, me, "np", "", "ref", "home", time.Minute, "pier", ""); err != nil {
		t.Fatal(err)
	}
	// A renewal is not news.
	sid, _, err := sess.Register(ctx, me, "np", "", "ref", "home", time.Minute, "pier", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Expire(ctx, sid); err != nil {
		t.Fatal(err)
	}
	page, err := evs.Poll(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range page.GetEvents() {
		if e.GetType() == "presence_changed" {
			if e.GetAccountId() != leia || e.GetNamespace() != "switch" || e.GetSubjectId() != me {
				t.Fatalf("presence_changed went to the wrong place: %+v", e)
			}
			got = append(got, e.GetType())
		}
	}
	if len(got) != 2 { // online, then offline; not the renewal, not the friend who is online nowhere
		t.Fatalf("want 2 presence_changed events, got %d", len(got))
	}
}
