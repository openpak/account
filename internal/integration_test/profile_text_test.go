//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"openpak/account/internal/identity"
	"openpak/account/internal/web"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func TestPublicProfileSharedBetweenWebAndAdapters(t *testing.T) {
	client, _, _, _, _, _, ident := startGRPC(t)
	ctx := authCtx()
	a, _, err := ident.RegisterWithProfile(ctx, "profile@example.com", "correct horse battery", "Profile User", identity.Profile{Country: "IE", Birthday: "1990-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	bio, motto := "Hello\nfrom OpenPak", "Ready to play"
	req := &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Bio: &bio, Motto: &motto}
	if _, err := client.UpdatePublicProfile(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated update: %v", err)
	}
	got, err := client.UpdatePublicProfile(ctx, req)
	if err != nil || got.GetBio() != bio || got.GetMotto() != motto || got.GetCountry() != "IE" || got.GetBirthday() != "1990-01-01" {
		t.Fatalf("adapter update: %v %v", got, err)
	}
	session, _, err := ident.Login(ctx, "profile@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	h := web.New(ident, nil, "https://core.test", "https://site.test")
	// A legacy client that never sends the new fields must preserve them.
	rec, _ := call(t, h, http.MethodPost, "/v1/profile", session, `{"country":"IE","birthday":"1990-01-01","display_name":"New Name"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy update: %d %s", rec.Code, rec.Body.String())
	}
	got, err = client.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: a.ID})
	if err != nil || got.GetBio() != bio || got.GetMotto() != motto || got.GetDisplayName() != "New Name" {
		t.Fatalf("legacy client erased profile text: %v %v", got, err)
	}
	rec, _ = call(t, h, http.MethodPost, "/v1/profile", session, `{"country":"IE","birthday":"1990-01-01","bio":"Changed on website","motto":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("web update: %d %s", rec.Code, rec.Body.String())
	}
	got, err = client.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: a.ID})
	if err != nil || got.GetBio() != "Changed on website" || got.GetMotto() != "" {
		t.Fatalf("web values not visible to adapter: %v %v", got, err)
	}
	tooLong := strings.Repeat("😀", 11)
	if _, err := client.UpdatePublicProfile(ctx, &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Motto: &tooLong}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UTF-16 limit not enforced: %v", err)
	}
}
