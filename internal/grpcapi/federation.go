package grpcapi

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/federation"
	"openpak/account/pkg/routing"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// fed checks a federation call can be served at all.
func (s *Server) fed(ctx context.Context) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if s.federation == nil {
		return status.Error(codes.Unimplemented, "federation not configured")
	}
	return nil
}

// homeOnly guards the calls that manage nodes: a region must not mint or
// approve other regions.
func (s *Server) homeOnly(ctx context.Context) error {
	if err := s.fed(ctx); err != nil {
		return err
	}
	if NodeFromContext(ctx) != federation.Home {
		return status.Error(codes.PermissionDenied, "home only")
	}
	return nil
}

func (s *Server) CreateNode(ctx context.Context, req *accountv1.CreateNodeRequest) (*accountv1.CreateNodeResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	key, err := s.federation.Create(ctx, req.GetNodeId(), req.GetName(), req.GetCountry(), req.GetRegion())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.CreateNodeResponse{NodeId: req.GetNodeId(), Key: key}, nil
}

func (s *Server) RotateNodeKey(ctx context.Context, req *accountv1.RotateNodeKeyRequest) (*accountv1.CreateNodeResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	key, err := s.federation.RotateNodeKey(ctx, req.GetNodeId())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.CreateNodeResponse{NodeId: req.GetNodeId(), Key: key}, nil
}

func (s *Server) JoinServer(ctx context.Context, req *accountv1.JoinServerRequest) (*accountv1.JoinServerResponse, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	id, key, err := s.federation.JoinServer(ctx, CallerFromContext(ctx), req.GetName())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.JoinServerResponse{ServerId: id, Key: key}, nil
}

func (s *Server) RevokeServer(ctx context.Context, req *accountv1.RevokeServerRequest) (*accountv1.RevokeServerResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.RevokeServer(ctx, req.GetServerId()); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.RevokeServerResponse{}, nil
}

func (s *Server) Announce(ctx context.Context, req *accountv1.AnnounceRequest) (*accountv1.AnnounceResponse, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	r := req.GetReport()
	rep := federation.Report{
		Cores: int(r.GetCores()), RAMMB: r.GetRamMb(), DiskGB: r.GetDiskGb(), DiskSSD: r.GetDiskSsd(),
		IPv4: r.GetIpv4(), IPv6: r.GetIpv6(), PortMbps: int(r.GetPortMbps()),
	}
	res, err := s.federation.Announce(ctx, CallerFromContext(ctx), req.GetServices(), rep, req.GetObservedIp())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.AnnounceResponse{
		NodeId: res.Node.ID, Approved: res.Node.Approved, ServerId: res.Server.ID,
		InRotation: res.Node.InRotation, Problems: res.Node.Problems, SelftestDue: res.SelfTestDue,
	}, nil
}

func (s *Server) StartSelfTest(ctx context.Context, req *accountv1.StartSelfTestRequest) (*accountv1.StartSelfTestResponse, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	id, n, err := s.federation.StartSelfTest(ctx, CallerFromContext(ctx), req.GetDirection())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.StartSelfTestResponse{ServerId: id, Bytes: n}, nil
}

func (s *Server) RecordSelfTest(ctx context.Context, req *accountv1.RecordSelfTestRequest) (*accountv1.RecordSelfTestResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.RecordSelfTest(ctx, req.GetServerId(), req.GetDirection(), req.GetMbps(), int(req.GetRttMs())); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.RecordSelfTestResponse{}, nil
}

// ListNodes is what may be handed out. It carries no waiver or server detail:
// the answer feeds the public /api/v1/regions.
func (s *Server) ListNodes(ctx context.Context, _ *accountv1.ListNodesRequest) (*accountv1.ListNodesResponse, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	nodes, err := s.federation.PublicList(ctx) // test nodes are never public
	if err != nil {
		return nil, mapFederationErr(err)
	}
	resp := &accountv1.ListNodesResponse{}
	for _, n := range nodes {
		resp.Nodes = append(resp.Nodes, &accountv1.Node{
			NodeId: n.ID, Name: n.Name, Country: n.Country, Region: n.Region, Services: n.Services,
			LastSeenUnix: unix(n.LastSeen), Approved: n.Approved, InRotation: n.InRotation,
		})
	}
	return resp, nil
}

func (s *Server) ListAllNodes(ctx context.Context, _ *accountv1.ListNodesRequest) (*accountv1.ListNodesResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	nodes, err := s.federation.ListAll(ctx)
	if err != nil {
		return nil, mapFederationErr(err)
	}
	resp := &accountv1.ListNodesResponse{}
	now := time.Now()
	for _, n := range nodes {
		pn := &accountv1.Node{
			NodeId: n.ID, Name: n.Name, Country: n.Country, Region: n.Region, Services: n.Services,
			LastSeenUnix: unix(n.LastSeen), Approved: n.Approved, InRotation: n.InRotation, Problems: n.Problems,
			WaiverReason: n.WaiverReason, WaiverUntilUnix: unix(n.WaiverUntil), Waived: n.Waived(now),
			TotalCores: int32(n.TotalCores), TotalRamMb: n.TotalRAMMB, TotalDiskGb: n.TotalDiskGB, Test: n.Test,
		}
		for _, v := range n.Servers {
			pn.Servers = append(pn.Servers, &accountv1.Server{
				ServerId: v.ID, Name: v.Name, Services: v.Services, ObservedIp: v.ObservedIP, LastSeenUnix: unix(v.LastSeen),
				Report: &accountv1.ServerReport{Cores: int32(v.Cores), RamMb: v.RAMMB, DiskGb: v.DiskGB, DiskSsd: v.DiskSSD,
					Ipv4: v.IPv4, Ipv6: v.IPv6, PortMbps: int32(v.PortMbps)},
				DownMbps: v.DownMbps, DownUnix: unix(v.DownAt), UpMbps: v.UpMbps, UpUnix: unix(v.UpAt),
				RttMs: int32(v.RTTMs), Problems: v.Problems,
			})
		}
		resp.Nodes = append(resp.Nodes, pn)
	}
	return resp, nil
}

func (s *Server) SetNodeApproved(ctx context.Context, req *accountv1.SetNodeApprovedRequest) (*accountv1.SetNodeApprovedResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.SetApproved(ctx, req.GetNodeId(), req.GetApproved()); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.SetNodeApprovedResponse{}, nil
}

func (s *Server) SetNodeWaiver(ctx context.Context, req *accountv1.SetNodeWaiverRequest) (*accountv1.SetNodeWaiverResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	var until time.Time
	if u := req.GetUntilUnix(); u > 0 {
		until = time.Unix(u, 0)
	}
	if err := s.federation.SetWaiver(ctx, req.GetNodeId(), req.GetReason(), until); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.SetNodeWaiverResponse{}, nil
}

func (s *Server) GetRequirements(ctx context.Context, _ *accountv1.GetRequirementsRequest) (*accountv1.Requirements, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	r := s.federation.Req
	return &accountv1.Requirements{
		NodeCores: int32(r.NodeCores), NodeRamGb: int32(r.NodeRAMGB), NodeDiskGb: int32(r.NodeDiskGB),
		ServerCores: int32(r.ServerCores), ServerRamGb: int32(r.ServerRAMGB), ServerBandwidthMbps: int32(r.ServerBandwidthMbps),
		MeasuredMbps: int32(r.MeasuredMbps), AvailabilityPct: r.AvailabilityPct, SlackPct: int32(r.SlackPct),
		ReportStaleSeconds: int64(r.ReportStale / time.Second), SelftestIntervalSeconds: int64(r.SelfTestInterval / time.Second),
		SelftestMaxAgeSeconds: int64(r.SelfTestMaxAge / time.Second), SelftestBytes: r.SelfTestBytes,
		Roles: federation.Roles,
	}, nil
}

func (s *Server) ListLocations(ctx context.Context, _ *accountv1.ListLocationsRequest) (*accountv1.ListLocationsResponse, error) {
	if err := s.fed(ctx); err != nil {
		return nil, err
	}
	locs, err := s.federation.ListLocations(ctx)
	if err != nil {
		return nil, mapFederationErr(err)
	}
	resp := &accountv1.ListLocationsResponse{}
	for _, l := range locs {
		resp.Locations = append(resp.Locations, &accountv1.Location{Code: l.Code, Country: l.Country, City: l.City,
			Wanted: int32(l.Wanted), Status: l.Status, Approved: int32(l.Approved), Accepting: l.Open()})
	}
	return resp, nil
}

func (s *Server) PutLocation(ctx context.Context, req *accountv1.Location) (*accountv1.PutLocationResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	err := s.federation.PutLocation(ctx, federation.Location{Code: req.GetCode(), Country: req.GetCountry(), City: req.GetCity(),
		Wanted: int(req.GetWanted()), Status: req.GetStatus()})
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.PutLocationResponse{}, nil
}

func (s *Server) DeleteLocation(ctx context.Context, req *accountv1.DeleteLocationRequest) (*accountv1.DeleteLocationResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.DeleteLocation(ctx, req.GetCode()); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.DeleteLocationResponse{}, nil
}

func (s *Server) SetNodeTest(ctx context.Context, req *accountv1.SetNodeTestRequest) (*accountv1.SetNodeTestResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.SetTest(ctx, req.GetNodeId(), req.GetTest()); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.SetNodeTestResponse{}, nil
}

func (s *Server) ListTesters(ctx context.Context, _ *accountv1.ListTestersRequest) (*accountv1.ListTestersResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	ts, err := s.federation.ListTesters(ctx)
	if err != nil {
		return nil, mapFederationErr(err)
	}
	resp := &accountv1.ListTestersResponse{}
	for _, t := range ts {
		resp.Testers = append(resp.Testers, &accountv1.Tester{AccountId: t.AccountID, Email: t.Email, Note: t.Note, AddedUnix: unix(t.Added)})
	}
	return resp, nil
}

func (s *Server) PutTester(ctx context.Context, req *accountv1.PutTesterRequest) (*accountv1.PutTesterResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	id, err := s.federation.PutTester(ctx, req.GetAccount(), req.GetNote())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.PutTesterResponse{AccountId: id}, nil
}

func (s *Server) DeleteTester(ctx context.Context, req *accountv1.DeleteTesterRequest) (*accountv1.DeleteTesterResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	if err := s.federation.DeleteTester(ctx, req.GetAccountId()); err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.DeleteTesterResponse{}, nil
}

// ResolveRegion answers home rather than failing whenever the core cannot
// read its own tables: a console must never see a routing error.
func (s *Server) ResolveRegion(ctx context.Context, req *accountv1.ResolveRegionRequest) (*accountv1.ResolveRegionResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	r := routing.Request{Stack: req.GetStack(), Token: req.GetToken(), Country: req.GetCountry(), Service: req.GetService(), Key: req.GetKey()}
	ref := routing.AccountRef{AccountID: req.GetAccountId(), Namespace: req.GetLinkNamespace(), Subject: req.GetLinkSubject()}
	if a, err := s.federation.AccountRouting(ctx, ref); err == nil {
		r.Tester = a.Tester
		if r.Country == "" {
			r.Country = a.Country
		}
	}
	t, err := s.federation.RoutingTable(ctx)
	if err != nil {
		t = nil
	}
	res := t.Resolve(r)
	return &accountv1.ResolveRegionResponse{Region: res.Region, NodeId: res.Node, Address: res.Address, Services: res.Services,
		Via: res.Via, Tester: r.Tester}, nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func mapFederationErr(err error) error {
	switch {
	case errors.Is(err, federation.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, federation.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, federation.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, federation.ErrNotCompliant):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, federation.ErrTooSoon):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, federation.ErrWrongKey):
		return status.Error(codes.PermissionDenied, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
