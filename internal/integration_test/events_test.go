//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"openpak/account/internal/events"
	"openpak/account/internal/identity"
	"openpak/account/internal/links"
	"openpak/account/internal/storage"
)

func TestInvalidationEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS sessions, lifecycle_tokens, adapter_links, accounts, schema_migrations, invalidation_events CASCADE; DROP SEQUENCE IF EXISTS invalidation_events_version_seq CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	evSvc := events.New(pool)
	ident := identity.New(pool, "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, evSvc)
	lnks := links.New(pool, evSvc)

	acct, _, err := ident.Register(ctx, "events@example.com", "correct horse battery", "E")
	if err != nil {
		t.Fatal(err)
	}
	// Link first while active, then ban, unlink, unban.
	l, err := lnks.Reserve(ctx, acct.ID, "synthetic", "subj-ev", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lnks.Activate(ctx, l.ID, "k2"); err != nil {
		t.Fatal(err)
	}
	// Ban emits event; sessions revoked.
	if err := ident.SetBanned(ctx, acct.ID, true); err != nil {
		t.Fatal(err)
	}
	// Unlink emits scoped event.
	if _, err := lnks.Unlink(ctx, l.ID, "k3"); err != nil {
		t.Fatal(err)
	}
	if err := ident.SetBanned(ctx, acct.ID, false); err != nil {
		t.Fatal(err)
	}

	page, err := evSvc.Poll(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, ev := range page.Events {
		types = append(types, ev.Type)
	}
	want := []string{"account_banned", "link_unlinked", "account_unbanned"}
	if len(types) != 3 {
		t.Fatalf("expected 3 events, got %v", types)
	}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("event %d: expected %s, got %s", i, w, types[i])
		}
	}
	if page.Events[1].Namespace != "synthetic" || page.Events[1].SubjectId != "subj-ev" {
		t.Fatalf("scoped event fields wrong: %+v", page.Events[1])
	}
	// Poll after max version is empty; versions increase monotonically.
	after, err := evSvc.Poll(ctx, page.MaxVersion, 100)
	if err != nil || len(after.Events) != 0 {
		t.Fatalf("expected empty page, got %v %v", after.Events, err)
	}
}
