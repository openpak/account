package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"openpak/account/internal/identity"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) UpdatePublicProfile(ctx context.Context, req *accountv1.UpdatePublicProfileRequest) (*accountv1.GetAccountResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id required")
	}
	_, err := s.identity.UpdatePublicProfile(ctx, req.GetAccountId(), identity.PublicProfilePatch{
		DisplayName: req.DisplayName, Country: req.Country, Bio: req.Bio, Motto: req.Motto,
	})
	if errors.Is(err, identity.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "account not found")
	}
	if errors.Is(err, identity.ErrInvalidProfile) {
		return nil, status.Error(codes.InvalidArgument, "invalid profile update")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "profile update unavailable")
	}
	return s.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: req.GetAccountId()})
}
