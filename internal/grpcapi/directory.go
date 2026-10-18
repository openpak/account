package grpcapi

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"openpak/account/internal/federation"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) ListAccounts(ctx context.Context, req *accountv1.ListAccountsRequest) (*accountv1.ListAccountsResponse, error) {
	if e := s.authorize(ctx); e != nil {
		return nil, e
	}
	if NodeFromContext(ctx) != federation.Home {
		return nil, status.Error(codes.PermissionDenied, "home only")
	}
	if len(req.GetAfterAccountId()) > 64 {
		return nil, status.Error(codes.InvalidArgument, "invalid directory cursor")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, status.Error(codes.InvalidArgument, "invalid directory limit")
	}
	accounts, e := s.identity.Directory(ctx, req.GetAfterAccountId(), limit+1)
	if e != nil {
		return nil, status.Error(codes.Internal, "directory unavailable")
	}
	out := &accountv1.ListAccountsResponse{}
	if len(accounts) > limit {
		accounts = accounts[:limit]
		out.NextAfterAccountId = accounts[len(accounts)-1].ID
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, &accountv1.AccountProjection{AccountId: a.ID, DisplayName: a.DisplayName, Country: a.Country, Handle: a.Handle})
	}
	return out, nil
}
