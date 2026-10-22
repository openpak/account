//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"openpak/account/internal/identity"
)

func TestHandleOwnership(t *testing.T) {
	ident, lnks := newEnv(t)
	ctx := context.Background()
	a, _, err := ident.RegisterWithHandle(ctx, "handle@example.com", "correct horse battery", "Aurora Player", "AuroraOne", identity.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ident.GetByID(ctx, a.ID); err != nil || got.Handle != "AuroraOne" || got.DisplayName != "Aurora Player" {
		t.Fatalf("account: %+v %v", got, err)
	}
	check, err := ident.CheckHandle(ctx, "auroraone")
	if err != nil || !check.Valid || check.Available {
		t.Fatalf("check: %+v %v", check, err)
	}
	if id, err := ident.ResolveHandle(ctx, "AURORAONE"); err != nil || id != a.ID {
		t.Fatalf("resolve: %s %v", id, err)
	}
	if err := ident.ClaimHandle(ctx, a.ID, "AuroraOne"); err != nil {
		t.Fatal(err)
	}
	if err := ident.ClaimHandle(ctx, a.ID, "auroraone"); !errors.Is(err, identity.ErrHandleImmutable) {
		t.Fatalf("case rename: %v", err)
	}
	if err := ident.SetDisplayName(ctx, a.ID, "Aurora Name"); err != nil {
		t.Fatal(err)
	}
	if id, err := ident.ResolveHandle(ctx, "auroraone"); err != nil || id != a.ID {
		t.Fatal(id, err)
	}
	b, _, err := ident.Register(ctx, "player@example.com", "correct horse battery", "Player")
	if err != nil {
		t.Fatal(err)
	}
	l, err := lnks.Reserve(ctx, b.ID, "xbox", "adapter-subject", "player")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lnks.Activate(ctx, l.ID, "activate"); err != nil {
		t.Fatal(err)
	}
	if err := lnks.SetPublicCode(ctx, l.ID, "AuroraOne", "auroraone"); err == nil {
		t.Fatal("link adapter stole claimed handle")
	}
	if _, _, err := ident.RegisterWithHandle(ctx, "other@example.com", "correct horse battery", "", "auroraone", identity.Profile{}); !errors.Is(err, identity.ErrHandleTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	np, err := lnks.Reserve(ctx, a.ID, "np", "AuroraOne", "np-reserve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lnks.Activate(ctx, np.ID, "np-active"); err != nil {
		t.Fatal(err)
	}
	if err = lnks.SetPublicCode(ctx, np.ID, "AuroraOne", "auroraone"); err != nil {
		t.Fatal(err)
	}
	if err := ident.FinalizeDeletion(ctx, a.ID); !errors.Is(err, identity.ErrDeletionNotPending) {
		t.Fatalf("active finalization: %v", err)
	}
	if err := ident.RequestDeletion(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	check, err = ident.CheckHandle(ctx, "auroraone")
	if err != nil || check.Available {
		t.Fatal("pending deletion released handle", err)
	}
	if err := ident.FinalizeDeletion(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	check, err = ident.CheckHandle(ctx, "auroraone")
	if err != nil || check.Available {
		t.Fatal("deleted handle escaped hold", err)
	}
	if _, err := ident.ResolveHandle(ctx, "auroraone"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted lookup: %v", err)
	}
	if _, err := ident.Pool().Exec(ctx, `UPDATE account_handle_claims SET release_at=now()-interval '1 second' WHERE account_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	replacement, _, err := ident.RegisterWithHandle(ctx, "replacement@example.com", "correct horse battery", "", "AuroraOne", identity.Profile{})
	if err != nil {
		t.Fatal("expired hold blocked reuse", err)
	}
	if _, err := lnks.Reserve(ctx, replacement.ID, "np", "AuroraOne", "replacement-np"); err != nil {
		t.Fatal("retired PSN subject blocked reuse", err)
	}
}

func TestConcurrentHandleRegistration(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := ident.RegisterWithHandle(ctx, fmt.Sprintf("race%d@example.com", i), "correct horse battery", "", "RaceHandle", identity.Profile{})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	won, lost := 0, 0
	for err := range errs {
		if err == nil {
			won++
		} else if errors.Is(err, identity.ErrHandleTaken) {
			lost++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || lost != 3 {
		t.Fatal(won, lost)
	}
}
