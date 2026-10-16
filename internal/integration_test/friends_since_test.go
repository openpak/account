//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"openpak/account/internal/social"
	"openpak/account/internal/storage"
)

// A friend's entry carries when the friendship was accepted, which is what a console's friend page
// shows as "When You Became Friends": not when the request was made, and not zero.
func TestFriendsSinceIsTheAcceptTime(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	a, _, err := ident.Register(ctx, "since-a@example.org", "correct horse battery", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "since-b@example.org", "correct horse battery", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := social.New(pool, nil)
	if _, err = svc.RequestFriend(ctx, a.ID, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	// The request sits for a while before it is answered.
	if _, err = pool.Exec(ctx, `UPDATE friendships SET created_at = now() - interval '9 days', updated_at = now() - interval '9 days'`); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-5 * time.Second).Unix()
	if _, err = svc.AcceptFriend(ctx, b.ID, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{a.ID, b.ID} {
		friends, err := svc.ListFriends(ctx, who)
		if err != nil || len(friends) != 1 {
			t.Fatalf("friends of %s: %v %v", who, friends, err)
		}
		if got := friends[0].FriendsSinceUnix; got < before || got > time.Now().Add(5*time.Second).Unix() {
			t.Fatalf("friends since = %d, want the accept time (>= %d), not the request's", got, before)
		}
	}
}
