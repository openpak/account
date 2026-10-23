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

// A test node is invisible in the public list and reached only by accounts
// on the allow-list; everyone else is routed as if it did not exist.
func TestFederationTestNodeRouting(t *testing.T) {
	_, lc, _, _, _, _, ident := startGRPC(t)
	fc := accountv1.NewFederationClient(lastConn)
	ctx, cancel := context.WithTimeout(authCtx(), 20*time.Second)
	defer cancel()

	// With no nodes at all, everything is home.
	r, err := fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Token: "sa/*", Country: "BR"})
	if err != nil || r.GetNodeId() != "" || r.GetVia() != "home" || r.GetRegion() != "eu-west" {
		t.Fatalf("zero nodes: %v %v", r, err)
	}

	made, err := fc.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "br-test", Name: "Brazil test", Country: "BR", Region: "sa-east"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := fc.JoinServer(metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", made.GetKey()), &accountv1.JoinServerRequest{Name: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", j.GetKey())
	for _, dir := range []string{"down", "up"} {
		if _, err := fc.StartSelfTest(srv, &accountv1.StartSelfTestRequest{Direction: dir}); err != nil {
			t.Fatal(err)
		}
		if _, err := fc.RecordSelfTest(ctx, &accountv1.RecordSelfTestRequest{ServerId: j.GetServerId(), Direction: dir, Mbps: 900}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fc.Announce(srv, &accountv1.AnnounceRequest{
		Services:   map[string]string{"photon": "198.51.100.8:5056", "photon_ping": "198.51.100.8:5055", "impostor": "198.51.100.8:22023"},
		ObservedIp: "198.51.100.8",
		Report:     &accountv1.ServerReport{Cores: 2, RamMb: 4096, DiskGb: 40, DiskSsd: true, Ipv4: "198.51.100.8", PortMbps: 1000}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetNodeWaiver(ctx, &accountv1.SetNodeWaiverRequest{NodeId: "br-test", Reason: "owner test node"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetNodeApproved(ctx, &accountv1.SetNodeApprovedRequest{NodeId: "br-test", Approved: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetNodeTest(srv, &accountv1.SetNodeTestRequest{NodeId: "br-test", Test: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node flagging itself: %v", err)
	}
	if _, err := fc.SetNodeTest(ctx, &accountv1.SetNodeTestRequest{NodeId: "br-test", Test: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetNodeTest(ctx, &accountv1.SetNodeTestRequest{NodeId: "nope", Test: true}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown node: %v", err)
	}

	if l, _ := fc.ListNodes(ctx, &accountv1.ListNodesRequest{}); len(l.GetNodes()) != 0 {
		t.Fatalf("test node in the public list: %v", l)
	}
	if all, _ := fc.ListAllNodes(ctx, &accountv1.ListNodesRequest{}); len(all.GetNodes()) != 1 || !all.GetNodes()[0].GetTest() || !all.GetNodes()[0].GetInRotation() {
		t.Fatalf("admin view: %v", all)
	}

	owner, _, err := ident.Register(ctx, "owner-test@openpak.org", "correct horse battery", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	player, _, err := ident.Register(ctx, "player@example.com", "correct horse battery", "Player")
	if err != nil {
		t.Fatal(err)
	}
	res, err := lc.ReserveLink(ctx, &accountv1.ReserveLinkRequest{AccountId: owner.ID, Namespace: "switch", SubjectId: "00aa11bb22cc33dd", IdempotencyKey: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lc.ActivateLink(ctx, &accountv1.ActivateLinkRequest{LinkId: res.GetLinkId(), IdempotencyKey: "a1"}); err != nil {
		t.Fatal(err)
	}

	// Not yet allow-listed: nobody reaches it.
	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Token: "sa/*", LinkNamespace: "switch", LinkSubject: "00aa11bb22cc33dd"})
	if r.GetNodeId() != "" || r.GetTester() {
		t.Fatalf("before allow-list: %v", r)
	}
	if _, err := fc.PutTester(ctx, &accountv1.PutTesterRequest{Account: "nobody@example.com"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown email: %v", err)
	}
	pt, err := fc.PutTester(ctx, &accountv1.PutTesterRequest{Account: "Owner-Test@openpak.org", Note: "owner"})
	if err != nil || pt.GetAccountId() != owner.ID {
		t.Fatalf("put tester: %v %v", pt, err)
	}
	if lt, _ := fc.ListTesters(ctx, &accountv1.ListTestersRequest{}); len(lt.GetTesters()) != 1 || lt.GetTesters()[0].GetEmail() != "owner-test@openpak.org" {
		t.Fatalf("list testers: %v", lt)
	}

	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Token: "sa/*", LinkNamespace: "switch", LinkSubject: "00aa11bb22cc33dd"})
	if r.GetNodeId() != "br-test" || !r.GetTester() || r.GetVia() != "explicit" || r.GetAddress() != "198.51.100.8:5056" {
		t.Fatalf("tester by link: %v", r)
	}
	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "amongus", Token: "na", Country: "BR", AccountId: owner.ID})
	if r.GetNodeId() != "br-test" {
		t.Fatalf("tester by account id, Among Us na refined by country: %v", r)
	}
	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Token: "sa/*", Country: "BR", AccountId: player.ID})
	if r.GetNodeId() != "" || r.GetVia() != "home" {
		t.Fatalf("ordinary player reached the test node: %v", r)
	}

	if _, err := fc.DeleteTester(ctx, &accountv1.DeleteTesterRequest{AccountId: owner.ID}); err != nil {
		t.Fatal(err)
	}
	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Token: "sa/*", AccountId: owner.ID})
	if r.GetNodeId() != "" {
		t.Fatalf("after removal: %v", r)
	}

	// Unflagged, it is an ordinary public node.
	if _, err := fc.SetNodeTest(ctx, &accountv1.SetNodeTestRequest{NodeId: "br-test", Test: false}); err != nil {
		t.Fatal(err)
	}
	if l, _ := fc.ListNodes(ctx, &accountv1.ListNodesRequest{}); len(l.GetNodes()) != 1 {
		t.Fatalf("public after unflagging: %v", l)
	}
	r, _ = fc.ResolveRegion(ctx, &accountv1.ResolveRegionRequest{Stack: "photon", Country: "BR"})
	if r.GetNodeId() != "br-test" || r.GetVia() != "country" {
		t.Fatalf("public by country: %v", r)
	}
}
