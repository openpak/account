//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// A region node: minted from home, invisible until approved and announcing,
// stamped on the sessions it registers, and unable to mint or approve nodes.
func TestFederationNodeLifecycle(t *testing.T) {
	_, _, _, sc, _, _, ident := startGRPC(t)
	fc := accountv1.NewFederationClient(lastConn)
	ctx, cancel := context.WithTimeout(authCtx(), 20*time.Second)
	defer cancel()

	made, err := fc.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "au-sydney", Name: "Sydney", Country: "au"})
	if err != nil || made.GetKey() == "" {
		t.Fatalf("create: %v %v", made, err)
	}
	if _, err := fc.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "au-sydney", Name: "Again", Country: "AU"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, err := fc.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "Bad Slug", Name: "x", Country: "AU"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad slug: %v", err)
	}

	nodeCtx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", made.GetKey())
	ann, err := fc.Announce(nodeCtx, &accountv1.AnnounceRequest{Services: map[string]string{"photon": "203.0.113.9:22002"}})
	if err != nil || ann.GetNodeId() != "au-sydney" || ann.GetApproved() {
		t.Fatalf("announce before approval: %v %v", ann, err)
	}
	if l, _ := fc.ListNodes(ctx, &accountv1.ListNodesRequest{}); len(l.GetNodes()) != 0 {
		t.Fatalf("unapproved node listed: %v", l)
	}
	if _, err := fc.CreateNode(nodeCtx, &accountv1.CreateNodeRequest{NodeId: "nz-akl", Name: "x", Country: "NZ"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node minting a node: %v", err)
	}
	if _, err := fc.SetNodeApproved(nodeCtx, &accountv1.SetNodeApprovedRequest{NodeId: "au-sydney", Approved: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node approving itself: %v", err)
	}

	if _, err := fc.SetNodeApproved(ctx, &accountv1.SetNodeApprovedRequest{NodeId: "au-sydney", Approved: true}); err != nil {
		t.Fatal(err)
	}
	l, err := fc.ListNodes(ctx, &accountv1.ListNodesRequest{})
	if err != nil || len(l.GetNodes()) != 1 || l.GetNodes()[0].GetCountry() != "AU" || l.GetNodes()[0].GetServices()["photon"] != "203.0.113.9:22002" {
		t.Fatalf("list after approval: %v %v", l, err)
	}

	// Sessions registered through the node say so; home's say home.
	a, _, err := ident.Register(ctx, "fed-a@example.com", "correct horse battery", "Fed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RegisterSession(nodeCtx, &accountv1.RegisterSessionRequest{AccountId: a.ID, Namespace: "switch", TitleId: "t1", EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{AccountId: a.ID, Namespace: "switch", TitleId: "", EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}
	pr, err := sc.GetPresence(ctx, &accountv1.GetPresenceRequest{AccountIds: []string{a.ID}})
	if err != nil || len(pr.GetPresence()) != 1 {
		t.Fatalf("presence: %v %v", pr, err)
	}
	pc, err := sc.GetPlayerCounts(ctx, &accountv1.GetPlayerCountsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[string]int32{}
	for _, n := range pc.GetNodes() {
		byNode[n.GetNodeId()] = n.GetPlayers()
	}
	if byNode["au-sydney"] != 1 || byNode["home"] != 1 || pc.GetTotalPlayers() != 1 {
		t.Fatalf("per-node counts: %v total=%d", byNode, pc.GetTotalPlayers())
	}

	// A wrong key is nobody.
	bad := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", "opnode_0000000000000000000000000000000000000000000000000000000000000000")
	if _, err := fc.Announce(bad, &accountv1.AnnounceRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown node key: %v", err)
	}
}
