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

// RequestAccountDeletion closes an account (FR-8 first stage): access is
// disabled immediately, all sessions are revoked, a durable
// account_delete_started event is emitted for consumers, and the account is
// erased everywhere once its grace period is over (the website's finaliser).
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

// FinalizeAccountDeletion erases the core's part of a closed account: the
// home finaliser's last step once every other service has erased its own, or
// an explicit home-operator action. Never a console request or another node.
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

// ListDueAccountDeletions feeds the erasure finaliser (home only).
func (s *Server) ListDueAccountDeletions(ctx context.Context, r *accountv1.ListDueAccountDeletionsRequest) (*accountv1.ListDueAccountDeletionsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if NodeFromContext(ctx) != federation.Home {
		return nil, status.Error(codes.PermissionDenied, "home only")
	}
	ids, err := s.identity.DueDeletions(ctx, int(r.GetLimit()))
	if err != nil {
		return nil, status.Error(codes.Internal, "deletion unavailable")
	}
	return &accountv1.ListDueAccountDeletionsResponse{AccountIds: ids}, nil
}

// RestoreAccountDeletion is the admin restore within the grace period (home only).
func (s *Server) RestoreAccountDeletion(ctx context.Context, r *accountv1.RequestAccountDeletionRequest) (*accountv1.RequestAccountDeletionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if NodeFromContext(ctx) != federation.Home {
		return nil, status.Error(codes.PermissionDenied, "home only")
	}
	if r.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id required")
	}
	switch err := s.identity.RestoreDeletion(ctx, r.GetAccountId()); {
	case err == nil:
		return &accountv1.RequestAccountDeletionResponse{}, nil
	case errors.Is(err, identity.ErrNotFound):
		return nil, status.Error(codes.NotFound, "account not found")
	case errors.Is(err, identity.ErrDeletionNotPending):
		return nil, status.Error(codes.FailedPrecondition, "account is not closed")
	case errors.Is(err, identity.ErrGraceOver):
		return nil, status.Error(codes.OutOfRange, "grace period over; erasure has started")
	default:
		return nil, status.Error(codes.Internal, "restore unavailable")
	}
}
