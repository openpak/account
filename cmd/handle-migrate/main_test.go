//go:build integration

package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openpak/account/internal/events"
	"openpak/account/internal/identity"
	"openpak/account/internal/links"
	"openpak/account/internal/storage"
)

func TestReviewedMigrationIsAtomicAndPreservesLinkIDs(t *testing.T) {
	url := os.Getenv("ACCOUNT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("isolated ACCOUNT_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := storage.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNT_DATABASE_URL", url)
	ident := identity.New(pool, "fixture-pepper", time.Hour, events.New(pool))
	ls := links.New(pool, nil)
	a, _, err := ident.Register(ctx, "aurora@example.org", "correct horse battery", "Aurora Player")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "borealis@example.org", "correct horse battery", "Borealis Player")
	if err != nil {
		t.Fatal(err)
	}
	var oldLinks []link
	for _, ns := range []string{"np", "xbox"} {
		subject := "OldAurora"
		if ns == "xbox" {
			subject = "0009000000000001"
		}
		l, err := ls.Reserve(ctx, a.ID, ns, subject, "reserve-"+ns)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ls.Activate(ctx, l.ID, "activate-"+ns); err != nil {
			t.Fatal(err)
		}
		if err = ls.SetPublicCode(ctx, l.ID, "OldAurora", "oldaurora"); err != nil {
			t.Fatal(err)
		}
		oldLinks = append(oldLinks, link{ID: l.ID, Namespace: ns, Name: "OldAurora", Subject: subject})
	}
	choices := map[string]choice{a.ID: {Handle: "AuroraOne", DisplayName: "Aurora Account"}, b.ID: {Handle: "BorealisTwo", DisplayName: "Borealis Account"}}
	snapshots := []snapshot{{ID: a.ID, DisplayName: "Aurora Player", Links: oldLinks}, {ID: b.ID, DisplayName: "Borealis Player"}}
	dir := t.TempDir()
	plan := filepath.Join(dir, "choices.json")
	snap := filepath.Join(dir, "snapshot.json")
	write := func(path string, data any) {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(plan, choices)
	write(snap, snapshots)
	invoke := func(apply bool) error {
		t.Helper()
		previousFlags, previousArgs := flag.CommandLine, os.Args
		defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
		flag.CommandLine = flag.NewFlagSet("handle-migrate", flag.ContinueOnError)
		os.Args = []string{"handle-migrate", "-choices", plan, "-snapshot", snap}
		if apply {
			os.Args = append(os.Args, "-apply")
		}
		return run()
	}
	if err = invoke(false); err != nil {
		t.Fatal(err)
	}
	if got, err := ident.GetByID(ctx, a.ID); err != nil || got.Handle != "" {
		t.Fatal("dry run changed account", got, err)
	}
	if err = invoke(true); err != nil {
		t.Fatal(err)
	}
	for id, c := range choices {
		got, err := ident.GetByID(ctx, id)
		if err != nil || got.Handle != c.Handle || got.DisplayName != c.DisplayName {
			t.Fatal(got, err)
		}
	}
	for _, old := range oldLinks {
		subject := old.Subject
		if old.Namespace == "np" {
			subject = "AuroraOne"
		}
		current, err := ls.GetBySubject(ctx, old.Namespace, subject)
		if err != nil || current.ID != old.ID || current.PublicCode != "AuroraOne" {
			t.Fatalf("link id changed: %+v %v", current, err)
		}
	}
	if err = invoke(true); err != nil {
		t.Fatal("idempotent retry", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM invalidation_events WHERE type='handle_assigned'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("replayed migration emitted duplicate assignments", count, err)
	}
	// A stale name aborts the whole batch, even if an earlier account was valid.
	if err = ident.SetDisplayName(ctx, b.ID, "Changed since review"); err != nil {
		t.Fatal(err)
	}
	if err = invoke(true); err == nil {
		t.Fatal("stale review was applied")
	}
	if got, err := ident.GetByID(ctx, b.ID); err != nil || got.DisplayName != "Changed since review" {
		t.Fatal("stale review overwrote a name", got, err)
	}
}
