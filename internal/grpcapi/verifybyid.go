package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/identity"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// VerifyPasswordByID verifies credentials for an adapter-resolved account ID
// (console login path; PRD §7). No session is created; only status crosses.
func (s *Server) VerifyPasswordByID(ctx context.Context, req *accountv1.VerifyPasswordByIDRequest) (*accountv1.VerifyPasswordResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" || req.GetPurpose() == "" || req.GetCallerNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id, purpose, and caller_namespace are required")
	}
	acct, err := s.identity.VerifyByID(ctx, req.GetAccountId(), req.GetPassword())
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrBadCredentials):
			return nil, status.Error(codes.Unauthenticated, "invalid credentials")
		case errors.Is(err, identity.ErrNotFound):
			return nil, status.Error(codes.NotFound, "account not found")
		case errors.Is(err, identity.ErrAccountBanned):
			return &accountv1.VerifyPasswordResponse{Status: accountv1.AccountStatus_ACCOUNT_STATUS_BANNED}, nil
		case errors.Is(err, identity.ErrAccountDeleted):
			return &accountv1.VerifyPasswordResponse{Status: accountv1.AccountStatus_ACCOUNT_STATUS_DELETED}, nil
		default:
			return nil, status.Error(codes.Internal, "internal error")
		}
	}
	return &accountv1.VerifyPasswordResponse{
		AccountId:     acct.ID,
		Status:        accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE,
		EmailVerified: acct.EmailVerified,
	}, nil
}
