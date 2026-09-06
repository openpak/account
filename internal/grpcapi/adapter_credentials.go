package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) SetAdapterCredential(ctx context.Context, req *accountv1.SetAdapterCredentialRequest) (*accountv1.SetAdapterCredentialResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetCallerNamespace() == "" || req.GetAccountId() == "" || req.GetSecret() == "" {
		return nil, status.Error(codes.InvalidArgument, "caller_namespace, account_id, and secret are required")
	}
	if err := s.identity.SetAdapterCredential(ctx, req.GetCallerNamespace(), req.GetAccountId(), req.GetSecret()); err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &accountv1.SetAdapterCredentialResponse{}, nil
}

func (s *Server) VerifyAdapterCredential(ctx context.Context, req *accountv1.VerifyAdapterCredentialRequest) (*accountv1.VerifyAdapterCredentialResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetCallerNamespace() == "" || req.GetAccountId() == "" || req.GetSecret() == "" {
		return nil, status.Error(codes.InvalidArgument, "caller_namespace, account_id, and secret are required")
	}
	_, err := s.identity.VerifyAdapterCredential(ctx, req.GetCallerNamespace(), req.GetAccountId(), req.GetSecret())
	switch {
	case err == nil:
		return &accountv1.VerifyAdapterCredentialResponse{
			Valid:  true,
			Status: accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE,
		}, nil
	case err == identityErrBadCredentials():
		return &accountv1.VerifyAdapterCredentialResponse{Valid: false,
			Status: accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE}, nil
	case err == identityErrBanned():
		return &accountv1.VerifyAdapterCredentialResponse{Valid: false,
			Status: accountv1.AccountStatus_ACCOUNT_STATUS_BANNED}, nil
	case err == identityErrDeleted():
		return &accountv1.VerifyAdapterCredentialResponse{Valid: false,
			Status: accountv1.AccountStatus_ACCOUNT_STATUS_DELETED}, nil
	default:
		return nil, status.Error(codes.Internal, "internal error")
	}
}
