//go:build integration

package integration_test

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	accountv1 "openpak/account/proto/openpak/account/v1"
	"testing"
)

func TestGRPCAccountDirectoryPaginationAndAuth(t *testing.T) {
	client, _, _, _, _, _, identity := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	for _, name := range []string{"Alice", "Bob", "Charlie"} {
		if _, _, e := identity.Register(ctx, name+"@example.org", "correct horse battery", name); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := client.ListAccounts(context.Background(), &accountv1.ListAccountsRequest{}); status.Code(e) != codes.Unauthenticated {
		t.Fatal("directory accepted anonymous caller", e)
	}
	seen := map[string]bool{}
	after := ""
	for {
		r, e := client.ListAccounts(ctx, &accountv1.ListAccountsRequest{AfterAccountId: after, Limit: 1})
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range r.Accounts {
			if seen[a.AccountId] || a.DisplayName == "" {
				t.Fatal("invalid page", r)
			}
			seen[a.AccountId] = true
		}
		if r.NextAfterAccountId == "" {
			break
		}
		if r.NextAfterAccountId <= after {
			t.Fatal("cursor did not advance")
		}
		after = r.NextAfterAccountId
	}
	if len(seen) != 3 {
		t.Fatalf("accounts %d", len(seen))
	}
	for _, n := range []int32{-1, 501} {
		if _, e := client.ListAccounts(ctx, &accountv1.ListAccountsRequest{Limit: n}); status.Code(e) != codes.InvalidArgument {
			t.Fatal("invalid limit accepted", n, e)
		}
	}
}
