//go:build integration

package integration_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// The website could block and unblock but never show the list it built.
func TestListBlocks(t *testing.T) {
	_, _, sc, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	me, _, _ := ident.Register(ctx, "me@example.com", "correct horse battery", "Me")
	a, _, _ := ident.Register(ctx, "a@example.com", "correct horse battery", "Alpha")
	b, _, _ := ident.Register(ctx, "b@example.com", "correct horse battery", "Bravo")

	empty, err := sc.ListBlocks(ctx, &accountv1.ListBlocksRequest{AccountId: me.ID})
	if err != nil || len(empty.GetBlocked()) != 0 {
		t.Fatalf("empty list: %+v %v", empty, err)
	}
	for _, other := range []string{a.ID, b.ID} {
		if _, err := sc.Block(ctx, &accountv1.BlockRequest{BlockerId: me.ID, BlockedId: other}); err != nil {
			t.Fatal(err)
		}
	}
	// Someone blocking me does not show on my list.
	if _, err := sc.Block(ctx, &accountv1.BlockRequest{BlockerId: a.ID, BlockedId: me.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := sc.ListBlocks(ctx, &accountv1.ListBlocksRequest{AccountId: me.ID})
	if err != nil || len(got.GetBlocked()) != 2 {
		t.Fatalf("list: %+v %v", got, err)
	}
	names := map[string]string{}
	for _, e := range got.GetBlocked() {
		names[e.GetAccountId()] = e.GetDisplayName()
		if e.GetBlockedAtUnix() == 0 {
			t.Fatalf("blocked_at missing: %+v", e)
		}
	}
	if names[a.ID] != "Alpha" || names[b.ID] != "Bravo" {
		t.Fatalf("names: %v", names)
	}
	if _, err := sc.Unblock(ctx, &accountv1.UnblockRequest{BlockerId: me.ID, BlockedId: a.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ = sc.ListBlocks(ctx, &accountv1.ListBlocksRequest{AccountId: me.ID})
	if len(got.GetBlocked()) != 1 || got.GetBlocked()[0].GetAccountId() != b.ID {
		t.Fatalf("after unblock: %+v", got.GetBlocked())
	}
	if _, err := sc.ListBlocks(ctx, &accountv1.ListBlocksRequest{AccountId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
}
