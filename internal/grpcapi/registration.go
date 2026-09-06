package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/identity"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// RegisterAccount implements the adapter-delegated account creation
// (PRD FR-1: Nintendo routes delegate account operations through
// authenticated internal APIs).
func (s *Server) RegisterAccount(ctx context.Context, req *accountv1.RegisterAccountRequest) (*accountv1.RegisterAccountResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetCallerNamespace() == "" || req.GetPurpose() == "" {
		return nil, status.Error(codes.InvalidArgument, "caller_namespace and purpose are required")
	}
	acct, token, err := s.identity.Register(ctx, req.GetEmail(), req.GetPassword(), req.GetDisplayName())
	if err != nil {
		if err == identity.ErrEmailTaken {
			// Do not confirm or deny registration to the console user; the
			// adapter surfaces its protocol-appropriate generic error.
			return &accountv1.RegisterAccountResponse{EmailTaken: true}, nil
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	resp := &accountv1.RegisterAccountResponse{AccountId: acct.ID}
	// Development/zero-config mailer: expose token for test completion.
	if s.mailer == nil {
		resp.VerificationToken = token
		return resp, nil
	}
	verifyURL := s.baseURL + "/v1/verify?token=" + token
	body := "Welcome to OpenPak!\n\nConfirm your email address by opening this link:\n" + verifyURL +
		"\n\nIf you did not create this account, ignore this message."
	if err := s.mailer.Send(acct.Email, "Confirm your OpenPak email", body); err != nil {
		return nil, status.Error(codes.Internal, "verification email could not be queued")
	}
	return resp, nil
}
