//go:build integration

package integration_test

import (
	"context"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"
	"testing"
	"time"
)

func TestFriendActivitySurvivesOffline(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	a, _, err := ident.Register(ctx, "a@example.org", "correct horse battery", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "b@example.org", "correct horse battery", "Bob")
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
	if _, err = svc.AcceptFriend(ctx, b.ID, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Simulate retained history with no live adapter lease, later than auth history.
	stamp := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	_, err = pool.Exec(ctx, `INSERT INTO play_sessions(account_id,namespace,title_id,client,os,started_at,ended_at) VALUES($1,'np','','pier','linux',$2,$3)`, b.ID, stamp.Add(-time.Minute), stamp)
	if err != nil {
		t.Fatal(err)
	}
	friends, err := svc.ListFriends(ctx, a.ID)
	if err != nil || len(friends) != 1 {
		t.Fatalf("friends: %v %v", friends, err)
	}
	if friends[0].LastSeenUnix != stamp.Unix() {
		t.Fatalf("offline history lost: %+v", friends[0])
	}
	if _, err = pool.Exec(ctx, `DELETE FROM play_sessions WHERE account_id=$1;`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM sessions WHERE account_id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	friends, err = svc.ListFriends(ctx, a.ID)
	if err != nil || friends[0].LastSeenUnix != 0 {
		t.Fatalf("unknown history fabricated: %v %v", friends, err)
	}
}
