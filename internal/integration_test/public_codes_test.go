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

func activeLink(t *testing.T, ctx context.Context, lc accountv1.LinksClient, accountID, ns, subject string) string {
	t.Helper()
	res, err := lc.ReserveLink(ctx, &accountv1.ReserveLinkRequest{AccountId: accountID, Namespace: ns, SubjectId: subject, IdempotencyKey: subject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lc.ActivateLink(ctx, &accountv1.ActivateLinkRequest{LinkId: res.LinkId, IdempotencyKey: subject + "-a"}); err != nil {
		t.Fatal(err)
	}
	return res.LinkId
}

// CR-3: the adapter publishes the friend code it minted; every other surface
// reads and resolves it through the core.
func TestLinkPublicCodes(t *testing.T) {
	_, lc, _, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	a, _, _ := ident.Register(ctx, "code-a@example.com", "correct horse battery", "A")
	b, _, _ := ident.Register(ctx, "code-b@example.com", "correct horse battery", "B")
	la := activeLink(t, ctx, lc, a.ID, "switch", "0123456789abcdef")
	lb := activeLink(t, ctx, lc, b.ID, "switch", "fedcba9876543210")
	activeLink(t, ctx, lc, a.ID, "wiiu", "wiiu-subject-a")

	// Before publishing: links listed, no code.
	list, err := lc.ListAccountLinks(ctx, &accountv1.ListAccountLinksRequest{AccountId: a.ID})
	if err != nil || len(list.GetLinks()) != 2 || list.GetLinks()[0].GetPublicCode() != "" {
		t.Fatalf("list before: %+v %v", list, err)
	}

	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: la, PublicCode: "SW-1000-0000-0001", LookupKey: "100000000001"}); err != nil {
		t.Fatal(err)
	}
	got, err := lc.GetActiveLinkByAccount(ctx, &accountv1.GetActiveLinkByAccountRequest{Namespace: "switch", AccountId: a.ID})
	if err != nil || got.GetPublicCode() != "SW-1000-0000-0001" || got.GetSubjectId() != "0123456789abcdef" {
		t.Fatalf("active link carries the code: %+v %v", got, err)
	}
	sw, _ := lc.ListAccountLinks(ctx, &accountv1.ListAccountLinksRequest{AccountId: a.ID, Namespace: "switch"})
	if len(sw.GetLinks()) != 1 || sw.GetLinks()[0].GetPublicCode() != "SW-1000-0000-0001" {
		t.Fatalf("filtered list: %+v", sw.GetLinks())
	}
	// Every way a person types it resolves.
	for _, typed := range []string{"SW-1000-0000-0001", "sw100000000001", "1000 0000 0001", "100000000001"} {
		r, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "switch", PublicCode: typed})
		if err != nil || r.GetAccountId() != a.ID || r.GetLinkId() != la {
			t.Fatalf("resolve %q: %+v %v", typed, r, err)
		}
	}
	if _, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "wiiu", PublicCode: "100000000001"}); status.Code(err) != codes.NotFound {
		t.Fatalf("other namespace: %v", err)
	}
	// One code, one account.
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: lb, PublicCode: "SW-1000-0000-0001", LookupKey: "100000000001"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate code: %v", err)
	}
	// Regenerating replaces: the old code stops resolving.
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: la, PublicCode: "SW-1111-2222-3333", LookupKey: "111122223333"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "switch", PublicCode: "100000000001"}); status.Code(err) != codes.NotFound {
		t.Fatalf("retired code: %v", err)
	}
	// Now free for someone else.
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: lb, PublicCode: "SW-1000-0000-0001"}); err != nil {
		t.Fatal(err)
	}
	if r, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "switch", PublicCode: "sw-1000-0000-0001"}); err != nil || r.GetAccountId() != b.ID {
		t.Fatalf("default key: %+v %v", r, err)
	}
	// Unlinking takes the code with it, and a revoked link cannot publish.
	if _, err := lc.UnlinkLink(ctx, &accountv1.UnlinkLinkRequest{LinkId: la, IdempotencyKey: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "switch", PublicCode: "111122223333"}); status.Code(err) != codes.NotFound {
		t.Fatalf("code after unlink: %v", err)
	}
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: la, PublicCode: "SW-9"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("publish on revoked link: %v", err)
	}
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: "00000000-0000-4000-8000-000000000000", PublicCode: "x"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown link: %v", err)
	}
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: lb, PublicCode: "---"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code without letters or digits: %v", err)
	}
	// Unpublish.
	if _, err := lc.SetLinkPublicCode(ctx, &accountv1.SetLinkPublicCodeRequest{LinkId: lb}); err != nil {
		t.Fatal(err)
	}
	if _, err := lc.GetLinkByPublicCode(ctx, &accountv1.GetLinkByPublicCodeRequest{Namespace: "switch", PublicCode: "100000000001"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unpublished: %v", err)
	}
}
