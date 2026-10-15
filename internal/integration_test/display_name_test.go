//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// A console renames its user: the name becomes the account's, and a name the
// registration rule would refuse is refused here too.
func TestSetDisplayName(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	acct, _, err := ident.Register(ctx, "rename@example.com", "correct horse battery", "Before")
	if err != nil {
		t.Fatal(err)
	}
	rc := accountv1.NewRegistrationClient(lastConn)
	ic := accountv1.NewIdentityClient(lastConn)

	if _, err := rc.SetDisplayName(ctx, &accountv1.SetDisplayNameRequest{
		CallerNamespace: "switch", AccountId: acct.ID, DisplayName: "  After  "}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: acct.ID})
	if err != nil || got.GetDisplayName() != "After" {
		t.Fatalf("name after rename: %q %v", got.GetDisplayName(), err)
	}

	for _, bad := range []string{"", "   ", strings.Repeat("x", 33)} {
		_, err := rc.SetDisplayName(ctx, &accountv1.SetDisplayNameRequest{
			CallerNamespace: "switch", AccountId: acct.ID, DisplayName: bad})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if _, err := rc.SetDisplayName(ctx, &accountv1.SetDisplayNameRequest{
		CallerNamespace: "switch", AccountId: "00000000-0000-4000-8000-000000000000", DisplayName: "Ghost"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown account: %v", err)
	}
	if got, _ := ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: acct.ID}); got.GetDisplayName() != "After" {
		t.Fatalf("refused renames changed the name to %q", got.GetDisplayName())
	}
}
