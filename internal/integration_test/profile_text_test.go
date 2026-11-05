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
	// A older client that never sends the new fields must preserve them.
	rec, _ := call(t, h, http.MethodPost, "/v1/profile", session, `{"country":"IE","birthday":"1990-01-01","display_name":"New Name"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("older update: %d %s", rec.Code, rec.Body.String())
	}
	got, err = client.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: a.ID})
	if err != nil || got.GetBio() != bio || got.GetMotto() != motto || got.GetDisplayName() != "New Name" {
		t.Fatalf("older client erased profile text: %v %v", got, err)
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

func TestLanguageTimezonePatchAndEvent(t *testing.T) {
	client, _, _, _, _, evc, ident := startGRPC(t)
	ctx := authCtx()
	a, _, err := ident.RegisterWithProfile(ctx, "lang@example.com", "correct horse battery", "Lang User", identity.Profile{Country: "IE"})
	if err != nil {
		t.Fatal(err)
	}
	since := latestEventVersion(t, evc)
	lang, tz := "pt-br", "Europe/Dublin"
	got, err := client.UpdatePublicProfile(ctx, &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Language: &lang, Timezone: &tz})
	if err != nil || got.GetLanguage() != "pt-BR" || got.GetTimezone() != tz {
		t.Fatalf("set: %v %v", got, err)
	}
	// Omitted fields preserve both.
	bio := "hi"
	got, err = client.UpdatePublicProfile(ctx, &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Bio: &bio})
	if err != nil || got.GetLanguage() != "pt-BR" || got.GetTimezone() != tz {
		t.Fatalf("omitted erased: %v %v", got, err)
	}
	// An invalid timezone is rejected and changes nothing.
	bad := "Mars/Base"
	if _, err := client.UpdatePublicProfile(ctx, &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Timezone: &bad}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad timezone: %v", err)
	}
	// Empty clears the language.
	empty := ""
	got, err = client.UpdatePublicProfile(ctx, &accountv1.UpdatePublicProfileRequest{AccountId: a.ID, Language: &empty})
	if err != nil || got.GetLanguage() != "" || got.GetTimezone() != tz {
		t.Fatalf("clear: %v %v", got, err)
	}
	n := 0
	for _, e := range eventsSince(t, evc, since) {
		if e.GetType() == "profile_updated" && e.GetAccountId() == a.ID {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("profile_updated events = %d, want 3", n)
	}
}

// The rewards service announces a change to a player's game cosmetics on the universal stream.
func TestEntitlementChangedEvent(t *testing.T) {
	client, _, _, _, _, evc, ident := startGRPC(t)
	ctx := authCtx()
	a, _, err := ident.Register(ctx, "ent@example.com", "correct horse battery", "Ent")
	if err != nil {
		t.Fatal(err)
	}
	since := latestEventVersion(t, evc)
	if _, err := evc.EmitEvent(ctx, &accountv1.EmitEventRequest{Type: "entitlement_changed", AccountId: a.ID, SubjectId: "dst"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	found := false
	for _, e := range eventsSince(t, evc, since) {
		if e.GetType() == "entitlement_changed" && e.GetAccountId() == a.ID && e.GetSubjectId() == "dst" {
			found = true
		}
	}
	if !found {
		t.Fatal("entitlement_changed not on the stream")
	}
	_ = client
}
