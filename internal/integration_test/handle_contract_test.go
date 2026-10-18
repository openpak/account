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

func TestPermanentHandleContracts(t *testing.T) {
	ic, _, _, _, _, _, _ := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	rc := accountv1.NewRegistrationClient(lastConn)
	if _, err := ic.CheckHandle(context.Background(), &accountv1.CheckHandleRequest{Handle: "AuroraOne"}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous check", err)
	}
	req := &accountv1.RegisterAccountRequest{CallerNamespace: "adapter", Purpose: "registration", Email: "aurora@example.org", Password: "correct horse battery"}
	if _, err := rc.RegisterAccount(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("missing handle accepted", err)
	}
	req.Handle = "AuroraOne"
	created, err := rc.RegisterAccount(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := created.AccountId
	account, err := ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: id})
	if err != nil || account.Handle != "AuroraOne" || account.DisplayName != "AuroraOne" {
		t.Fatalf("account: %+v %v", account, err)
	}
	directory, err := ic.ListAccounts(ctx, &accountv1.ListAccountsRequest{})
	if err != nil || len(directory.Accounts) != 1 || directory.Accounts[0].Handle != "AuroraOne" {
		t.Fatal(directory, err)
	}
	check, err := ic.CheckHandle(ctx, &accountv1.CheckHandleRequest{Handle: "auroraone"})
	if err != nil || !check.Valid || check.Available {
		t.Fatal(check, err)
	}
	resolved, err := ic.ResolveHandle(ctx, &accountv1.ResolveHandleRequest{Handle: "AURORAONE"})
	if err != nil || resolved.AccountId != id {
		t.Fatal(resolved, err)
	}
	if _, err := ic.ClaimHandle(ctx, &accountv1.ClaimHandleRequest{AccountId: id, Handle: "AuroraTwo"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("rename accepted", err)
	}
	if _, err := rc.SetDisplayName(ctx, &accountv1.SetDisplayNameRequest{CallerNamespace: "adapter", AccountId: id, DisplayName: "Aurora Player"}); err != nil {
		t.Fatal(err)
	}
	account, err = ic.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: id})
	if err != nil || account.Handle != "AuroraOne" || account.DisplayName != "Aurora Player" {
		t.Fatal(account, err)
	}
	deletion := &accountv1.RequestAccountDeletionRequest{AccountId: id}
	if _, err := ic.RequestAccountDeletion(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.FinalizeAccountDeletion(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.FinalizeAccountDeletion(ctx, deletion); err != nil {
		t.Fatal("finalization retry", err)
	}
	check, err = ic.CheckHandle(ctx, &accountv1.CheckHandleRequest{Handle: "AuroraOne"})
	if err != nil || check.Available {
		t.Fatal("deleted handle released early", check, err)
	}
	if _, err := ic.ResolveHandle(ctx, &accountv1.ResolveHandleRequest{Handle: "AuroraOne"}); status.Code(err) != codes.NotFound {
		t.Fatal("deleted owner resolved", err)
	}
}
