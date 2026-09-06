package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
