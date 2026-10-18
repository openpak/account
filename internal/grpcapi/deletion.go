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

// RequestAccountDeletion marks an account deletion-pending (FR-8 first
// stage): access is disabled immediately, all sessions are revoked, and a
// durable account_delete_started event is emitted for consumers. Full
// cross-service cleanup orchestration is M3/M4 work (PRD §9).
func (s *Server) RequestAccountDeletion(ctx context.Context, req *accountv1.RequestAccountDeletionRequest) (*accountv1.RequestAccountDeletionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id required")
	}
	if err := s.identity.RequestDeletion(ctx, req.GetAccountId()); err != nil {
		switch err {
		case identity.ErrNotFound:
			return nil, status.Error(codes.NotFound, "account not found")
		default:
			return nil, status.Error(codes.Internal, "internal error")
		}
	}
	return &accountv1.RequestAccountDeletionResponse{}, nil
}

// FinalizeAccountDeletion is an explicit home-operator action, never an
// implicit consequence of a console request or a clock on another node.
func (s *Server) FinalizeAccountDeletion(ctx context.Context, r *accountv1.RequestAccountDeletionRequest) (*accountv1.RequestAccountDeletionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if NodeFromContext(ctx) != federation.Home {
		return nil, status.Error(codes.PermissionDenied, "home only")
	}
	if r.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id required")
	}
	if err := s.identity.FinalizeDeletion(ctx, r.GetAccountId()); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		if errors.Is(err, identity.ErrDeletionNotPending) {
			return nil, status.Error(codes.FailedPrecondition, "account must be pending deletion")
		}
		return nil, status.Error(codes.Internal, "deletion unavailable")
	}
	return &accountv1.RequestAccountDeletionResponse{}, nil
}
