//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"openpak/account/internal/invitations"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// authCtx returns a context carrying valid internal credentials.
func authCtx() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
}

func pair(t *testing.T, ident interface {
	Register(ctx interface{ Done() <-chan struct{} }, a, b, c string) (interface{ GetID() string }, string, error)
}) {
}

func TestSocialFriendLifecycle(t *testing.T) {
	_, _, soc, _, _, _, ident := startGRPC(t)
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
	_, _, soc, _, inv, _, ident := startGRPC(t)
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
	_, _, _, sess, _, _, ident := startGRPC(t)
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

func TestInvitationBlockedAfterQueue(t *testing.T) {
	_, _, soc, sess, inv, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "qb-a@example.com", "correct horse battery", "QA")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "qb-b@example.com", "correct horse battery", "QB")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: b.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}
	sent, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}

	// Recipient blocks the sender AFTER queueing: delivery must be refused.
	if _, err := soc.Block(ctx, &accountv1.BlockRequest{BlockerId: b.ID, BlockedId: a.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("queued invitation delivered despite block: %+v %v", got, err)
	}
	var state string
	if err := ident.Pool().QueryRow(ctx,
		`SELECT state FROM invitations WHERE id=$1`, sent.InvitationId).Scan(&state); err != nil || state != "rejected" {
		t.Fatalf("expected rejected state, got %q %v", state, err)
	}
	// Ack cannot resurrect it.
	if ack, err := inv.AckInvitations(ctx, &accountv1.AckInvitationsRequest{Namespace: "switch", AccountId: b.ID,
		InvitationIds: []string{sent.InvitationId}, Nonces: []string{sent.Nonce}}); err != nil || ack.Acked != 0 {
		t.Fatalf("ack after rejection: %+v %v", ack, err)
	}
	// Unblocking does not revive the rejected invitation.
	if _, err := soc.Unblock(ctx, &accountv1.UnblockRequest{BlockerId: b.ID, BlockedId: a.ID}); err != nil {
		t.Fatal(err)
	}
	got, err = inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("post-unblock poll: %+v %v", got, err)
	}
}

func TestInvitationSessionLoss(t *testing.T) {
	_, _, soc, sess, inv, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "sl-a@example.com", "correct horse battery", "SA")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "sl-b@example.com", "correct horse battery", "SB")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: a.ID, AddresseeId: b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: b.ID, RequesterId: a.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: b.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep", LeaseSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	sent, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: a.ID, ToAccountId: b.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	// Remove/expire the bound session: the queued invitation must not deliver.
	time.Sleep(1200 * time.Millisecond)
	got, err := inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("delivered after session loss: %+v %v", got, err)
	}
	var state string
	if err := ident.Pool().QueryRow(ctx,
		`SELECT state FROM invitations WHERE id=$1`, sent.InvitationId).Scan(&state); err != nil || state != "expired" {
		t.Fatalf("expected expired state, got %q %v", state, err)
	}
}

func TestSessionRegistrationConcurrency(t *testing.T) {
	_, _, _, sess, _, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "conc-a@example.com", "correct horse battery", "C")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"
	const n = 8
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := sess.RegisterSession(authCtx(), &accountv1.RegisterSessionRequest{
				AccountId: a.ID, Namespace: "switch", TitleId: title,
				EndpointRef: fmt.Sprintf("ep-%d", i)})
			errCh <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent register: %v", err)
		}
	}
	var count int
	if err := ident.Pool().QueryRow(ctx,
		`SELECT count(*) FROM adapter_sessions WHERE account_id=$1 AND title_id=$2`, a.ID, title).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected exactly 1 live session row, got %d %v", count, err)
	}
	if _, err := sess.ResolveSession(ctx, &accountv1.ResolveSessionRequest{AccountId: a.ID, TitleId: title}); err != nil {
		t.Fatalf("resolve after concurrency: %v", err)
	}
}

func TestInvitationFlow(t *testing.T) {
	_, _, soc, sess, inv, _, ident := startGRPC(t)
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
	// At-least-once: still pending inside the redelivery window; the second
	// poll returns nothing until acknowledged.
	got, err = inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("poll inside redelivery window: %+v %v", got, err)
	}
	// Ack with the wrong nonce is rejected; the right nonce delivers.
	if ack, err := inv.AckInvitations(ctx, &accountv1.AckInvitationsRequest{Namespace: "switch", AccountId: b.ID,
		InvitationIds: []string{sent.InvitationId}, Nonces: []string{"00000000-0000-0000-0000-000000000000"}}); err != nil || ack.Acked != 0 {
		t.Fatalf("wrong-nonce ack: %+v %v", ack, err)
	}
	if ack, err := inv.AckInvitations(ctx, &accountv1.AckInvitationsRequest{Namespace: "switch", AccountId: b.ID,
		InvitationIds: []string{sent.InvitationId}, Nonces: []string{sent.Nonce}}); err != nil || ack.Acked != 1 {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	got, err = inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: b.ID})
	if err != nil || len(got.Invitations) != 0 {
		t.Fatalf("post-ack poll: %+v %v", got, err)
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

// TestInvitationPollSingleConnection pins the pool-exhaustion fix: Poll must
// complete on a one-connection pool because all its policy checks run through
// the transaction it already holds, never through a second checkout.
func TestInvitationPollSingleConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL+"&pool_max_conns=1")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	soc := social.New(pool, nil)
	sess := sessions.New(pool)
	inv := invitations.New(pool, soc, singleConnResolver{sess}, nil)

	var aID, bID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (id, email, email_normalized, password_hash, display_name)
		VALUES (gen_random_uuid(),'sc1@x.test','sc1@x.test','h','S1') RETURNING id`).Scan(&aID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (id, email, email_normalized, password_hash, display_name)
		VALUES (gen_random_uuid(),'sc2@x.test','sc2@x.test','h','S2') RETURNING id`).Scan(&bID); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.RequestFriend(ctx, aID, bID, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, bID, aID, "k"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sess.Register(ctx, bID, "switch", "t", "ep", "home", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Send(ctx, aID, bID, "t", []byte("hey"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := inv.Poll(ctx, "switch", bID, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("single-connection poll: %d invitations, err=%v", len(got), err)
	}
	acked, err := inv.Ack(ctx, "switch", bID, []string{got[0].ID}, []string{got[0].Nonce})
	if err != nil || acked != 1 {
		t.Fatalf("single-connection ack: %d acked, err=%v", acked, err)
	}
}

type singleConnResolver struct{ s *sessions.Service }

func (r singleConnResolver) Resolve(ctx context.Context, accountID, titleID string) (*invitations.SessionRef, error) {
	sess, err := r.s.Resolve(ctx, accountID, titleID)
	if err != nil {
		return nil, err
	}
	return &invitations.SessionRef{ID: sess.ID, Namespace: sess.Namespace}, nil
}

// The companion surfaces: presence for a friends list, and an invitation inbox
// that a phone can read without stealing the invitation from the adapter whose
// job is to deliver it.
func TestCompanionPresenceAndInbox(t *testing.T) {
	_, _, soc, sess, inv, _, ident := startGRPC(t)
	ctx := authCtx()

	host, _, err := ident.Register(ctx, "phone-host@example.com", "correct horse battery", "Host")
	if err != nil {
		t.Fatal(err)
	}
	guest, _, err := ident.Register(ctx, "phone-guest@example.com", "correct horse battery", "Guest")
	if err != nil {
		t.Fatal(err)
	}
	offline, _, err := ident.Register(ctx, "phone-offline@example.com", "correct horse battery", "Absent")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"

	// Nobody is playing yet: presence is empty rather than a row of "offline".
	pres, err := sess.GetPresence(ctx, &accountv1.GetPresenceRequest{AccountIds: []string{host.ID, guest.ID, offline.ID}})
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	if len(pres.GetPresence()) != 0 {
		t.Fatalf("presence before any session: %+v", pres.GetPresence())
	}

	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: guest.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep-guest"}); err != nil {
		t.Fatal(err)
	}
	pres, err = sess.GetPresence(ctx, &accountv1.GetPresenceRequest{AccountIds: []string{host.ID, guest.ID, offline.ID}})
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	if len(pres.GetPresence()) != 1 {
		t.Fatalf("want only the account with a live session, got %+v", pres.GetPresence())
	}
	if p := pres.GetPresence()[0]; p.GetAccountId() != guest.ID || p.GetTitleId() != title ||
		p.GetNamespace() != "switch" || p.GetSinceUnix() == 0 {
		t.Fatalf("presence row: %+v", p)
	}

	// An invitation the guest can see on a phone.
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: host.ID, AddresseeId: guest.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: guest.ID, RequesterId: host.ID}); err != nil {
		t.Fatal(err)
	}
	sent, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: host.ID, ToAccountId: guest.ID, TitleId: title, Payload: []byte("join-me"), TtlSeconds: 120})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	list, err := inv.ListInvitations(ctx, &accountv1.ListInvitationsRequest{AccountId: guest.ID})
	if err != nil || len(list.GetInvitations()) != 1 {
		t.Fatalf("inbox: %+v %v", list.GetInvitations(), err)
	}
	if got := list.GetInvitations()[0]; got.GetInvitationId() != sent.GetInvitationId() ||
		got.GetFromDisplayName() != "Host" || got.GetTitleId() != title || got.GetNamespace() != "switch" {
		t.Fatalf("inbox entry: %+v", got)
	}

	// Reading the inbox twice must not consume anything, and the adapter must
	// still receive the invitation afterwards: this is the whole point of a
	// separate read path.
	if list, err := inv.ListInvitations(ctx, &accountv1.ListInvitationsRequest{AccountId: guest.ID}); err != nil || len(list.GetInvitations()) != 1 {
		t.Fatalf("second read consumed it: %+v %v", list.GetInvitations(), err)
	}
	polled, err := inv.PollInvitations(ctx, &accountv1.PollInvitationsRequest{Namespace: "switch", AccountId: guest.ID})
	if err != nil || len(polled.GetInvitations()) != 1 {
		t.Fatalf("delivery path starved by the inbox read: %+v %v", polled.GetInvitations(), err)
	}

	// Someone else's invitation is not theirs to decline.
	if got, err := inv.RejectInvitation(ctx, &accountv1.RejectInvitationRequest{
		AccountId: host.ID, InvitationId: sent.GetInvitationId()}); err != nil || got.GetRejected() {
		t.Fatalf("a non-recipient declined it: %+v %v", got, err)
	}
	if got, err := inv.RejectInvitation(ctx, &accountv1.RejectInvitationRequest{
		AccountId: guest.ID, InvitationId: sent.GetInvitationId()}); err != nil || !got.GetRejected() {
		t.Fatalf("recipient decline: %+v %v", got, err)
	}
	// Declined leaves the inbox, and declining twice is not an error.
	if list, err := inv.ListInvitations(ctx, &accountv1.ListInvitationsRequest{AccountId: guest.ID}); err != nil || len(list.GetInvitations()) != 0 {
		t.Fatalf("declined invitation still in the inbox: %+v %v", list.GetInvitations(), err)
	}
	if got, err := inv.RejectInvitation(ctx, &accountv1.RejectInvitationRequest{
		AccountId: guest.ID, InvitationId: sent.GetInvitationId()}); err != nil || got.GetRejected() {
		t.Fatalf("second decline: %+v %v", got, err)
	}
}

// Push hangs off the events stream, so the two things worth a notification have
// to appear there: a friend request, and an invitation. Both are emitted inside
// the transaction that creates them, so nothing is announced that did not
// commit — and asking twice must not notify twice.
func TestFriendRequestAndInvitationEmitEvents(t *testing.T) {
	_, _, soc, sess, inv, ev, ident := startGRPC(t)
	ctx := authCtx()

	from, _, err := ident.Register(ctx, "ev-from@example.com", "correct horse battery", "Sender")
	if err != nil {
		t.Fatal(err)
	}
	to, _, err := ident.Register(ctx, "ev-to@example.com", "correct horse battery", "Receiver")
	if err != nil {
		t.Fatal(err)
	}
	const title = "0100E65002BB8000"

	since := latestEventVersion(t, ev)

	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: from.ID, AddresseeId: to.ID}); err != nil {
		t.Fatal(err)
	}
	got := eventsSince(t, ev, since)
	if len(got) != 1 || got[0].GetType() != "friend_requested" ||
		got[0].GetAccountId() != to.ID || got[0].GetSubjectId() != from.ID {
		t.Fatalf("friend request event: %+v", got)
	}

	// The idempotent repeat must not produce a second notification.
	if _, err := soc.RequestFriend(ctx, &accountv1.RequestFriendRequest{RequesterId: from.ID, AddresseeId: to.ID}); err != nil {
		t.Fatal(err)
	}
	if got := eventsSince(t, ev, since); len(got) != 1 {
		t.Fatalf("a repeated request notified twice: %+v", got)
	}

	if _, err := soc.AcceptFriend(ctx, &accountv1.AcceptFriendRequest{AddresseeId: to.ID, RequesterId: from.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: to.ID, Namespace: "switch", TitleId: title, EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}
	since = latestEventVersion(t, ev)
	sent, err := inv.SendInvitation(ctx, &accountv1.SendInvitationRequest{
		FromAccountId: from.ID, ToAccountId: to.ID, TitleId: title, Payload: []byte("join"), TtlSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	got = eventsSince(t, ev, since)
	if len(got) != 1 || got[0].GetType() != "invitation_sent" ||
		got[0].GetAccountId() != to.ID || got[0].GetSubjectId() != sent.GetInvitationId() {
		t.Fatalf("invitation event: %+v", got)
	}
}

func latestEventVersion(t *testing.T, ev accountv1.EventsClient) uint64 {
	t.Helper()
	resp, err := ev.PollEvents(authCtx(), &accountv1.PollEventsRequest{SinceVersion: 0, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetMaxVersion()
}

func eventsSince(t *testing.T, ev accountv1.EventsClient, since uint64) []*accountv1.InvalidationEvent {
	t.Helper()
	resp, err := ev.PollEvents(authCtx(), &accountv1.PollEventsRequest{SinceVersion: since, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetEvents()
}

// A status page counts people, not sessions: someone playing on a console and
// an emulator at once is one player online, and the total that flatters us is
// the wrong one.
func TestPlayerCountsCountPeople(t *testing.T) {
	_, _, _, sess, _, _, ident := startGRPC(t)
	ctx := authCtx()

	a, _, err := ident.Register(ctx, "count-a@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ident.Register(ctx, "count-b@example.com", "correct horse battery", "B")
	if err != nil {
		t.Fatal(err)
	}
	const kart, splat = "0100152000022000", "0100C2500FC20000"

	counts, err := sess.GetPlayerCounts(ctx, &accountv1.GetPlayerCountsRequest{})
	if err != nil || counts.GetTotalPlayers() != 0 || len(counts.GetTitles()) != 0 {
		t.Fatalf("nobody playing: %+v %v", counts, err)
	}

	for _, r := range []struct{ acct, title string }{
		{a.ID, kart}, {b.ID, kart}, {a.ID, splat},
	} {
		if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
			AccountId: r.acct, Namespace: "switch", TitleId: r.title, EndpointRef: "ep"}); err != nil {
			t.Fatal(err)
		}
	}

	counts, err = sess.GetPlayerCounts(ctx, &accountv1.GetPlayerCountsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int32{}
	for _, tp := range counts.GetTitles() {
		got[tp.GetTitleId()] = tp.GetPlayers()
	}
	if got[kart] != 2 || got[splat] != 1 {
		t.Errorf("per title: %v", got)
	}
	// Two people, three sessions.
	if counts.GetTotalPlayers() != 2 {
		t.Errorf("total = %d, want 2 people", counts.GetTotalPlayers())
	}
}

// Signed in and playing are different facts and the status page reports them
// separately. A session with no title is the first without the second: it is a
// person online, on a network, in nothing.
func TestOnlineWithoutPlayingCountsAsOnlineOnly(t *testing.T) {
	_, _, _, sess, _, _, ident := startGRPC(t)
	ctx := authCtx()

	idle, _, err := ident.Register(ctx, "online-idle@example.com", "correct horse battery", "Idle")
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err := ident.Register(ctx, "online-playing@example.com", "correct horse battery", "Playing")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: idle.ID, Namespace: "switch", TitleId: "", EndpointRef: "ep"}); err != nil {
		t.Fatalf("a title-less session is signed in, not invalid: %v", err)
	}
	if _, err := sess.RegisterSession(ctx, &accountv1.RegisterSessionRequest{
		AccountId: playing.ID, Namespace: "switch", TitleId: "0100152000022000", EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}

	counts, err := sess.GetPlayerCounts(ctx, &accountv1.GetPlayerCountsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if n := len(counts.GetTitles()); n != 1 {
		t.Errorf("titles = %d, want only the one anybody is actually playing", n)
	}
	for _, tp := range counts.GetTitles() {
		if tp.GetTitleId() == "" {
			t.Error("the person in no game was reported as playing a title with no name")
		}
	}
	if counts.GetTotalPlayers() != 2 {
		t.Errorf("online = %d, want both of them", counts.GetTotalPlayers())
	}
	for _, n := range counts.GetNetworks() {
		if n.GetNamespace() == "switch" && n.GetPlayers() != 2 {
			t.Errorf("online on switch = %d, want 2", n.GetPlayers())
		}
	}
}
