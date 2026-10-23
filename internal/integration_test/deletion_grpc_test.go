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

// The finaliser's and the admin's view over gRPC: a closed account reports
// its erasure date, is not due before it, restores, and once due is listed
// and refuses a restore.
func TestDeletionGraceOverGRPC(t *testing.T) {
	ic, _, _, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	rc := accountv1.NewRegistrationClient(lastConn)
	created, err := rc.RegisterAccount(ctx, &accountv1.RegisterAccountRequest{CallerNamespace: "adapter", Purpose: "registration",
		Email: "closing@example.org", Password: "correct horse battery", Handle: "ClosingOne"})
	if err != nil {
		t.Fatal(err)
	}
	id := &accountv1.RequestAccountDeletionRequest{AccountId: created.AccountId}
	if _, err := ic.RestoreAccountDeletion(ctx, id); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("restore of an open account = %v", err)
	}
	if _, err := ic.RequestAccountDeletion(ctx, id); err != nil {
		t.Fatal(err)
	}
	acct, err := ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: created.AccountId})
	if err != nil || acct.Status != accountv1.AccountStatus_ACCOUNT_STATUS_DELETION_PENDING || acct.EraseAfterUnix == 0 {
		t.Fatalf("closed account: %+v %v", acct, err)
	}
	due, err := ic.ListDueAccountDeletions(ctx, &accountv1.ListDueAccountDeletionsRequest{})
	if err != nil || len(due.AccountIds) != 0 {
		t.Fatalf("due before the grace: %v %v", due, err)
	}
	if _, err := ic.RestoreAccountDeletion(ctx, id); err != nil {
		t.Fatalf("admin restore: %v", err)
	}
	acct, _ = ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: created.AccountId})
	if acct.Status != accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE || acct.EraseAfterUnix != 0 {
		t.Fatalf("restored account: %+v", acct)
	}
	if _, err := ic.RequestAccountDeletion(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ident.Pool().Exec(context.Background(), `UPDATE accounts SET erase_after = now() - interval '1 second' WHERE id=$1`, created.AccountId); err != nil {
		t.Fatal(err)
	}
	due, err = ic.ListDueAccountDeletions(ctx, &accountv1.ListDueAccountDeletionsRequest{Limit: 5})
	if err != nil || len(due.AccountIds) != 1 || due.AccountIds[0] != created.AccountId {
		t.Fatalf("due = %v %v", due, err)
	}
	if _, err := ic.RestoreAccountDeletion(ctx, id); status.Code(err) != codes.OutOfRange {
		t.Fatalf("restore after the grace = %v, want OutOfRange", err)
	}
	if _, err := ic.FinalizeAccountDeletion(ctx, id); err != nil {
		t.Fatal(err)
	}
	if due, _ := ic.ListDueAccountDeletions(ctx, &accountv1.ListDueAccountDeletionsRequest{}); len(due.AccountIds) != 0 {
		t.Fatalf("erased account still due: %v", due)
	}
}
