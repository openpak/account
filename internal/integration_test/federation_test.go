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

// A region node: minted from home, enrolled server by server, invisible and
// unable to stamp sessions until approved and compliant, refused approval
// while short, dropped from rotation (approval kept) when a server degrades,
// and unable to mint or approve nodes.
func TestFederationNodeLifecycle(t *testing.T) {
	_, _, _, sc, _, _, ident := startGRPC(t)
	fc := accountv1.NewFederationClient(lastConn)
	ctx, cancel := context.WithTimeout(authCtx(), 20*time.Second)
	defer cancel()

	made, err := fc.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "au-sydney", Name: "Sydney", Country: "au", Region: "oc"})
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
	if _, err := fc.Announce(nodeCtx, &accountv1.AnnounceRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node key announcing: %v", err)
	}
	if _, err := fc.CreateNode(nodeCtx, &accountv1.CreateNodeRequest{NodeId: "nz-akl", Name: "x", Country: "NZ"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node minting a node: %v", err)
	}
	if _, err := fc.SetNodeApproved(nodeCtx, &accountv1.SetNodeApprovedRequest{NodeId: "au-sydney", Approved: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node approving itself: %v", err)
	}

	// Two servers, each with its own key.
	j1, err := fc.JoinServer(nodeCtx, &accountv1.JoinServerRequest{Name: "game1"})
	if err != nil || j1.GetServerId() != "au-sydney/game1" || j1.GetKey() == "" {
		t.Fatalf("join: %v %v", j1, err)
	}
	j2, err := fc.JoinServer(nodeCtx, &accountv1.JoinServerRequest{Name: "turn1"})
	if err != nil {
		t.Fatal(err)
	}
	s1 := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", j1.GetKey())
	s2 := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", j2.GetKey())
	if _, err := fc.JoinServer(s1, &accountv1.JoinServerRequest{Name: "x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("server key enrolling: %v", err)
	}

	report := func(c context.Context, ip string, cores int32, svc map[string]string) *accountv1.AnnounceResponse {
		t.Helper()
		a, err := fc.Announce(c, &accountv1.AnnounceRequest{Services: svc, ObservedIp: ip, Report: &accountv1.ServerReport{
			Cores: cores, RamMb: 8192, DiskGb: 80, DiskSsd: true, Ipv4: ip, PortMbps: 1000}})
		if err != nil {
			t.Fatalf("announce: %v", err)
		}
		return a
	}
	a := report(s1, "203.0.113.9", 1, map[string]string{"photon": "203.0.113.9:22002"})
	if a.GetApproved() || a.GetInRotation() || !a.GetSelftestDue() || a.GetServerId() != "au-sydney/game1" {
		t.Fatalf("announce before approval: %v", a)
	}
	report(s2, "203.0.113.10", 2, map[string]string{"turn": "203.0.113.10:3478"})
	if _, err := fc.Announce(s1, &accountv1.AnnounceRequest{Services: map[string]string{"bogus": "x"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown role: %v", err)
	}

	// Short: approval names what is missing.
	_, err = fc.SetNodeApproved(ctx, &accountv1.SetNodeApprovedRequest{NodeId: "au-sydney", Approved: true})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "server game1: 1 CPU cores, minimum 2") ||
		!strings.Contains(err.Error(), "no bandwidth self-test yet") || !strings.Contains(err.Error(), "total CPU cores 3, minimum 8") {
		t.Fatalf("approval while short: %v", err)
	}
	// Sessions cannot be stamped by a node that is not in rotation.
	acc, _, err := ident.Register(ctx, "fed-a@example.com", "correct horse battery", "Fed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RegisterSession(s1, &accountv1.RegisterSessionRequest{AccountId: acc.ID, Namespace: "switch", TitleId: "t1", EndpointRef: "ep"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unapproved node stamping a session: %v", err)
	}

	// Self-tests: a server starts one, home records it, a second is refused.
	for _, sctx := range []context.Context{s1, s2} {
		for _, dir := range []string{"down", "up"} {
			st, err := fc.StartSelfTest(sctx, &accountv1.StartSelfTestRequest{Direction: dir})
			if err != nil || st.GetBytes() <= 0 {
				t.Fatalf("start self-test: %v %v", st, err)
			}
			if _, err := fc.RecordSelfTest(sctx, &accountv1.RecordSelfTestRequest{ServerId: st.GetServerId(), Direction: dir, Mbps: 900}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("server writing its own result: %v", err)
			}
			if _, err := fc.RecordSelfTest(ctx, &accountv1.RecordSelfTestRequest{ServerId: st.GetServerId(), Direction: dir, Mbps: 500, RttMs: 180}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := fc.StartSelfTest(s1, &accountv1.StartSelfTestRequest{Direction: "down"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second self-test in a day: %v", err)
	}

	report(s1, "203.0.113.9", 4, map[string]string{"photon": "203.0.113.9:22002"})
	a = report(s2, "203.0.113.10", 4, map[string]string{"turn": "203.0.113.10:3478"})
	if a.GetSelftestDue() || len(a.GetProblems()) != 0 {
		t.Fatalf("compliant announce: %v", a)
	}
	if _, err := fc.SetNodeApproved(ctx, &accountv1.SetNodeApprovedRequest{NodeId: "au-sydney", Approved: true}); err != nil {
		t.Fatal(err)
	}
	l, err := fc.ListNodes(ctx, &accountv1.ListNodesRequest{})
	if err != nil || len(l.GetNodes()) != 1 || l.GetNodes()[0].GetCountry() != "AU" || l.GetNodes()[0].GetRegion() != "oc" ||
		l.GetNodes()[0].GetServices()["photon"] != "203.0.113.9:22002" || l.GetNodes()[0].GetServices()["turn"] != "203.0.113.10:3478" ||
		len(l.GetNodes()[0].GetServers()) != 0 || l.GetNodes()[0].GetWaiverReason() != "" {
		t.Fatalf("list after approval: %v %v", l, err)
	}

	// Sessions registered through the node say so; home's say home.
	if _, err := sc.RegisterSession(s1, &accountv1.RegisterSessionRequest{AccountId: acc.ID, Namespace: "switch", TitleId: "t1", EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RegisterSession(ctx, &accountv1.RegisterSessionRequest{AccountId: acc.ID, Namespace: "switch", TitleId: "", EndpointRef: "ep"}); err != nil {
		t.Fatal(err)
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

	// A server drops below the floor: out of rotation, still approved.
	report(s2, "203.0.113.10", 1, map[string]string{"turn": "203.0.113.10:3478"})
	if l, _ := fc.ListNodes(ctx, &accountv1.ListNodesRequest{}); len(l.GetNodes()) != 0 {
		t.Fatalf("degraded node still listed: %v", l)
	}
	all, err := fc.ListAllNodes(ctx, &accountv1.ListNodesRequest{})
	if err != nil || len(all.GetNodes()) != 1 || !all.GetNodes()[0].GetApproved() || all.GetNodes()[0].GetInRotation() ||
		len(all.GetNodes()[0].GetServers()) != 2 || all.GetNodes()[0].GetTotalCores() != 5 {
		t.Fatalf("admin view of degraded node: %v %v", all, err)
	}
	// A waiver puts it back; it is admin-only.
	if _, err := fc.SetNodeWaiver(ctx, &accountv1.SetNodeWaiverRequest{NodeId: "au-sydney", UntilUnix: time.Now().Add(time.Hour).Unix()}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("waiver without reason: %v", err)
	}
	if _, err := fc.SetNodeWaiver(s1, &accountv1.SetNodeWaiverRequest{NodeId: "au-sydney", Reason: "mine"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("node waiving itself: %v", err)
	}
	if _, err := fc.SetNodeWaiver(ctx, &accountv1.SetNodeWaiverRequest{NodeId: "au-sydney", Reason: "owner test node", UntilUnix: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if l, _ := fc.ListNodes(ctx, &accountv1.ListNodesRequest{}); len(l.GetNodes()) != 1 {
		t.Fatalf("waived node not listed: %v", l)
	}
	if _, err := fc.SetNodeWaiver(ctx, &accountv1.SetNodeWaiverRequest{NodeId: "au-sydney"}); err != nil {
		t.Fatal(err)
	}

	// Revoking one server kills its key only.
	if _, err := fc.RevokeServer(ctx, &accountv1.RevokeServerRequest{ServerId: "au-sydney/turn1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.Announce(s2, &accountv1.AnnounceRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked server key: %v", err)
	}
	report(s1, "203.0.113.9", 4, map[string]string{"photon": "203.0.113.9:22002"})

	// A wrong key is nobody.
	bad := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", "opnode_0000000000000000000000000000000000000000000000000000000000000000")
	if _, err := fc.Announce(bad, &accountv1.AnnounceRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown node key: %v", err)
	}

	// The rules are readable by any caller; their defaults are the owner's.
	req, err := fc.GetRequirements(s1, &accountv1.GetRequirementsRequest{})
	if err != nil || req.GetNodeCores() != 8 || req.GetNodeRamGb() != 16 || req.GetNodeDiskGb() != 160 || req.GetServerBandwidthMbps() != 1000 {
		t.Fatalf("requirements: %v %v", req, err)
	}
}

// The coverage plan: admins keep it, approved counts are derived.
func TestFederationLocations(t *testing.T) {
	startGRPC(t)
	fc := accountv1.NewFederationClient(lastConn)
	ctx, cancel := context.WithTimeout(authCtx(), 20*time.Second)
	defer cancel()
	if l, err := fc.ListLocations(ctx, &accountv1.ListLocationsRequest{}); err != nil || len(l.GetLocations()) != 0 {
		t.Fatalf("seeded list must be empty: %v %v", l, err)
	}
	if _, err := fc.PutLocation(ctx, &accountv1.Location{Code: "sa-east", Country: "br", City: "São Paulo", Wanted: 1, Status: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.PutLocation(ctx, &accountv1.Location{Code: "SA East", Country: "BR", Status: "open"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad code: %v", err)
	}
	l, _ := fc.ListLocations(ctx, &accountv1.ListLocationsRequest{})
	if len(l.GetLocations()) != 1 || !l.GetLocations()[0].GetAccepting() || l.GetLocations()[0].GetCountry() != "BR" {
		t.Fatalf("list: %v", l)
	}
	if _, err := fc.DeleteLocation(ctx, &accountv1.DeleteLocationRequest{Code: "sa-east"}); err != nil {
		t.Fatal(err)
	}
}

// admittedNode makes a node that is approved and in rotation, with one server
// that meets every minimum on its own, and returns the server's credentials:
// what a real region's relay would call with.
func admittedNode(t *testing.T, id string) context.Context {
	t.Helper()
	fc := accountv1.NewFederationClient(lastConn)
	home := authCtx()
	made, err := fc.CreateNode(home, &accountv1.CreateNodeRequest{NodeId: id, Name: id, Country: "FR"})
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
		if _, err := fc.RecordSelfTest(home, &accountv1.RecordSelfTestRequest{ServerId: j.GetServerId(), Direction: dir, Mbps: 900}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fc.Announce(srv, &accountv1.AnnounceRequest{Services: map[string]string{"turn": "198.51.100.7:3478"}, ObservedIp: "198.51.100.7",
		Report: &accountv1.ServerReport{Cores: 8, RamMb: 16384, DiskGb: 200, DiskSsd: true, Ipv4: "198.51.100.7", PortMbps: 1000}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetNodeApproved(home, &accountv1.SetNodeApprovedRequest{NodeId: id, Approved: true}); err != nil {
		t.Fatal(err)
	}
	return srv
}
