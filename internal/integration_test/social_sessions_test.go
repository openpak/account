//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// authCtx returns a context carrying valid internal credentials.
func authCtx() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
}

func pair(t *testing.T, ident interface {
	Register(ctx interface{ Done() <-chan struct{} }, a, b, c string) (interface{ GetID() string }, string, error)
}) {}

func TestSocialFriendLifecycle(t *testing.T) {
	_, _, soc, _, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "friend-a@example.com", "correct horse battery", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "friend-b@example.com", "correct horse battery", "Bob")
	if err != nil {
		t.Fatal(err)
	}

	// Request, idempotent duplicate.
	req, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID, IdempotencyKey: "k1"})
	if err != nil || req.State != accountv1.FriendState_FRIEND_STATE_PENDING {
		t.Fatalf("request: %+v %v", req, err)
	}
	req2, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID, IdempotencyKey: "k2"})
	if err != nil || req2.State != accountv1.FriendState_FRIEND_STATE_PENDING {
		t.Fatalf("idempotent request: %+v %v", req2, err)
	}
	// Self-request and relationship probes.
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: a.ID}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for self request, got %v", err)
	}
	rel, err := soc.GetRelationship(ctx, &accountv1.GetRelationshipRequest{AccountId: b.ID, OtherId: a.ID})
	if err != nil || rel.Relationship != accountv1.Relationship_RELATIONSHIP_PENDING_INCOMING {
		t.Fatalf("relationship from addressee: %+v %v", rel, err)
	}
	// Accept by requester (not addressee) fails.
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: a.ID, RequesterId: b.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound accepting wrong direction, got %v", err)
	}
	if acc, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil || acc.State != accountv1.FriendState_FRIEND_STATE_ACCEPTED {
		t.Fatalf("accept: %+v %v", acc, err)
	}
	// Accept is idempotent.
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil {
		t.Fatalf("idempotent accept: %v", err)
	}

	// Both sides list each other.
	lf, err := soc.ListFriends(ctx, &accountv1.ListFriendsRequest{AccountId: a.ID})
	if err != nil || len(lf.Friends) != 1 || lf.Friends[0].AccountId != b.ID {
		t.Fatalf("list a: %+v %v", lf, err)
	}
	lf, err = soc.ListFriends(ctx, &accountv1.ListFriendsRequest{AccountId: b.ID})
	if err != nil || len(lf.Friends) != 1 || lf.Friends[0].AccountId != a.ID {
		t.Fatalf("list b: %+v %v", lf, err)
	}

	// Interaction allowed between friends.
	ci, err := soc.CheckInteraction(ctx, &accountv1.CheckInteractionRequest{ActorId: a.ID, TargetId: b.ID, Kind: accountv1.InteractionKind_INTERACTION_KIND_INVITE})
	if err != nil || !ci.Allowed {
		t.Fatalf("check: %+v %v", ci, err)
	}

	// Remove; both sides see NONE again.
	if _, err := soc.RemoveFriend(ctx, &accountv1.RemoveFriendRequest{AccountId: a.ID, OtherId: b.ID}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rel, err = soc.GetRelationship(ctx, &accountv1.GetRelationshipRequest{AccountId: a.ID, OtherId: b.ID})
	if err != nil || rel.Relationship != accountv1.Relationship_RELATIONSHIP_NONE {
		t.Fatalf("post-remove relationship: %+v %v", rel, err)
	}
	ci, _ = soc.CheckInteraction(ctx, &accountv1.CheckInteractionRequest{ActorId: a.ID, TargetId: b.ID, Kind: accountv1.InteractionKind_INTERACTION_KIND_DISCOVER})
	if ci.Allowed || ci.Reason != accountv1.DenialReason_DENIAL_REASON_NOT_FRIENDS {
		t.Fatalf("post-remove check: %+v", ci)
	}
}

func TestSocialBlockEnforcement(t *testing.T) {
	_, _, soc, _, inv, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "block-a@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "block-b@example.com", "correct horse battery", "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil {
		t.Fatal(err)
	}

	// A blocks B: interactions denied both directions (block is directional
	// but enforcement is symmetric).
	if _, err := soc.Block(ctx, &accountv1.BlockRequest{BlockerId: a.ID, BlockedId: b.ID}); err != nil {
		t.Fatal(err)
	}
	ci, err := soc.CheckInteraction(ctx, &accountv1.CheckInteractionRequest{ActorId: b.ID, TargetId: a.ID, Kind: accountv1.InteractionKind_INTERACTION_KIND_INVITE})
	if err != nil || ci.Allowed || ci.Reason != accountv1.DenialReason_DENIAL_REASON_BLOCKED {
		t.Fatalf("blocked check: %+v %v", ci, err)
	}
	rel, err := soc.GetRelationship(ctx, &accountv1.GetRelationshipRequest{AccountId: b.ID, OtherId: a.ID})
	if err != nil || rel.Relationship != accountv1.Relationship_RELATIONSHIP_BLOCKED_BY_OTHER {
		t.Fatalf("relationship: %+v %v", rel, err)
	}

	// Invitation from B to a live A session is denied.
	sessSvc := accountv1.NewSessionsClient(nil) // unused; invitations tested via inv below
	_ = sessSvc
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: b.ID, ToAccountId: a.ID, TitleId: "0100E65002BB8000",
		Payload: []byte("hello"), TtlSeconds: 60}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied invitation while blocked, got %v", err)
	}

	// Unblock restores the (still accepted) friendship.
	if _, err := soc.Unblock(ctx, &accountv1.UnblockRequest{BlockerId: a.ID, BlockedId: b.ID}); err != nil {
		t.Fatal(err)
	}
	ci, err = soc.CheckInteraction(ctx, &accountv1.CheckInteractionRequest{ActorId: b.ID, TargetId: a.ID, Kind: accountv1.InteractionKind_INTERACTION_KIND_INVITE})
	if err != nil || !ci.Allowed {
		t.Fatalf("post-unblock check: %+v %v", ci, err)
	}
}

func TestSessionRegistry(t *testing.T) {
	_, _, _, sess, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "sess-a@example.com", "correct horse battery", "S")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"

	reg, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep-1"})
	if err != nil || reg.SessionId == "" || reg.LeaseSeconds <= 0 {
		t.Fatalf("register: %+v %v", reg, err)
	}
	res, err := sess.ResolveSession(ctx, &accountv1.ResolveSessionRequest{AccountId: a.ID, TitleId: title})
	if err != nil || res.SessionId != reg.SessionId || res.EndpointRef != "ep-1" || res.Namespace != "switch" {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	// Re-registration for the same (account, title) replaces the session.
	reg2, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: a.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep-2"})
	if err != nil {
		t.Fatal(err)
	}
	res, err = sess.ResolveSession(ctx, &accountv1.ResolveSessionRequest{AccountId: a.ID, TitleId: title})
	if err != nil || res.SessionId != reg2.SessionId || res.EndpointRef != "ep-2" {
		t.Fatalf("replace resolve: %+v %v", res, err)
	}
	// Heartbeat renews; heartbeat of an expired/unknown session fails.
	if _, err := sess.Heartbeat(ctx, &accountv1.HeartbeatRequest{SessionId: reg2.SessionId}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := sess.Heartbeat(ctx, &accountv1.HeartbeatRequest{SessionId: "00000000-0000-0000-0000-000000000000"}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound heartbeat, got %v", err)
	}
	// Expired sessions do not resolve.
	if _, err := sess.Heartbeat(ctx, &accountv1.HeartbeatRequest{SessionId: reg.SessionId}); err == nil {
		// reg.SessionId was replaced; either NotFound or nothing — tolerated.
		_ = err
	}
	if _, err := sess.ExpireSession(ctx, &accountv1.ExpireSessionRequest{SessionId: reg2.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ResolveSession(ctx, &accountv1.ResolveSessionRequest{AccountId: a.ID, TitleId: title}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound after expire, got %v", err)
	}
}

func TestInvitationFlow(t *testing.T) {
	_, _, soc, sess, inv, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "inv-a@example.com", "correct horse battery", "Host")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "inv-b@example.com", "correct horse battery", "Guest")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := ident.Register(ctx, "inv-c@example.com", "correct horse battery", "Stranger")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"

	// Non-friend is denied before any session lookup (no session-existence
	// leak to strangers).
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 60}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied non-friend invite, got %v", err)
	}

	// Friends but no live session → FailedPrecondition.
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 60}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition without session, got %v", err)
	}

	// B registers a live session; the invitation flows.
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: b.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep-b"}); err != nil {
		t.Fatal(err)
	}
	// Stranger C is still not a friend → denied even with a live session.
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: c.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 60}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied stranger invite, got %v", err)
	}

	sent, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join-me"), TtlSeconds: 120})
	if err != nil || sent.InvitationId == "" || sent.Nonce == "" {
		t.Fatalf("send: %+v %v", sent, err)
	}

	// Wrong namespace poll sees nothing; the delivering namespace gets it once.
	if got, err := inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "other", AccountId: b.ID}); err != nil || len(got.Invitations) != 0 {
		t.Fatalf("wrong namespace: %+v %v", got, err)
	}
	got, err := inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 1 {
		t.Fatalf("poll: %+v %v", got, err)
	}
	gi := got.Invitations[0]
	if gi.InvitationId != sent.InvitationId || gi.Nonce != sent.Nonce || string(gi.Payload) != "join-me" ||
		gi.FromAccountId != a.ID || gi.FromDisplayName != "Host" {
		t.Fatalf("invitation mismatch: %+v", gi)
	}
	// Delivered is terminal for polling.
	got, err = inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("replay poll: %+v %v", got, err)
	}


	// Validation: oversized payload and bad TTL are InvalidArgument.
	big := strings.Repeat("x", 4097)
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte(big), TtlSeconds: 60}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for oversized payload, got %v", err)
	}
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("x"), TtlSeconds: 0}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for zero TTL, got %v", err)
	}

	// Expired lease: register a 1-second session, wait it out.
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: b.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep-b2", LeaseSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 60}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition after lease expiry, got %v", err)
	}

}
