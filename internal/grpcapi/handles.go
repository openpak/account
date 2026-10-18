package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"openpak/account/internal/federation"
	"openpak/account/internal/identity"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) CheckHandle(ctx context.Context, r *accountv1.CheckHandleRequest) (*accountv1.CheckHandleResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	result, err := s.identity.CheckHandle(ctx, r.GetHandle())
	if err != nil {
		return nil, status.Error(codes.Internal, "handle check unavailable")
	}
	return &accountv1.CheckHandleResponse{Valid: result.Valid, Available: result.Available, Reason: result.Reason}, nil
}
func (s *Server) ResolveHandle(ctx context.Context, r *accountv1.ResolveHandleRequest) (*accountv1.ResolveHandleResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := s.identity.ResolveHandle(ctx, r.GetHandle())
	if err != nil {
		return nil, handleStatus(err)
	}
	return &accountv1.ResolveHandleResponse{AccountId: id}, nil
}
func (s *Server) ClaimHandle(ctx context.Context, r *accountv1.ClaimHandleRequest) (*accountv1.ClaimHandleResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if NodeFromContext(ctx) != federation.Home {
		return nil, status.Error(codes.PermissionDenied, "home only")
	}
	if r.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id required")
	}
	if err := s.identity.ClaimHandle(ctx, r.GetAccountId(), r.GetHandle()); err != nil {
		return nil, handleStatus(err)
	}
	return &accountv1.ClaimHandleResponse{}, nil
}
func handleStatus(err error) error {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return status.Error(codes.NotFound, "account not found")
	case errors.Is(err, identity.ErrHandleTaken):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, identity.ErrHandleImmutable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, identity.ErrHandleInvalid), errors.Is(err, identity.ErrHandleReserved), errors.Is(err, identity.ErrHandleProfane):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "handle unavailable")
	}
}
