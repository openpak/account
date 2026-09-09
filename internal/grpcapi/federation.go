package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/federation"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// homeOnly guards the calls that manage nodes: a region must not mint or
// approve other regions.
func (s *Server) homeOnly(ctx context.Context) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if s.federation == nil {
		return status.Error(codes.Unimplemented, "federation not configured")
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
	key, err := s.federation.Create(ctx, req.GetNodeId(), req.GetName(), req.GetCountry())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.CreateNodeResponse{NodeId: req.GetNodeId(), Key: key}, nil
}

func (s *Server) Announce(ctx context.Context, req *accountv1.AnnounceRequest) (*accountv1.AnnounceResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if s.federation == nil {
		return nil, status.Error(codes.Unimplemented, "federation not configured")
	}
	node := NodeFromContext(ctx)
	approved, err := s.federation.Announce(ctx, node, req.GetServices())
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return &accountv1.AnnounceResponse{NodeId: node, Approved: approved}, nil
}

func (s *Server) ListNodes(ctx context.Context, _ *accountv1.ListNodesRequest) (*accountv1.ListNodesResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if s.federation == nil {
		return nil, status.Error(codes.Unimplemented, "federation not configured")
	}
	nodes, err := s.federation.List(ctx)
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return nodesResponse(nodes), nil
}

func (s *Server) ListAllNodes(ctx context.Context, _ *accountv1.ListNodesRequest) (*accountv1.ListNodesResponse, error) {
	if err := s.homeOnly(ctx); err != nil {
		return nil, err
	}
	nodes, err := s.federation.ListAll(ctx)
	if err != nil {
		return nil, mapFederationErr(err)
	}
	return nodesResponse(nodes), nil
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

func nodesResponse(nodes []federation.Node) *accountv1.ListNodesResponse {
	resp := &accountv1.ListNodesResponse{}
	for _, n := range nodes {
		var seen int64
		if !n.LastSeen.IsZero() {
			seen = n.LastSeen.Unix()
		}
		resp.Nodes = append(resp.Nodes, &accountv1.Node{
			NodeId: n.ID, Name: n.Name, Country: n.Country, Services: n.Services, LastSeenUnix: seen, Approved: n.Approved,
		})
	}
	return resp
}

func mapFederationErr(err error) error {
	switch {
	case errors.Is(err, federation.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, federation.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, federation.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
