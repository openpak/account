package grpcapi

import (
	"context"
	"errors"
	"openpak/account/internal/emaillink"
	"strings"

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
	acct, token, err := s.identity.RegisterWithProfile(ctx, req.GetEmail(), req.GetPassword(), req.GetDisplayName(),
		identity.Profile{Country: req.GetCountry()})
	if err != nil {
		if err == identity.ErrEmailTaken {
			// Existing unverified account: resend the verification email so
			// a failed delivery is repairable by retrying registration.
			if token := s.identity.ResendVerificationIfUnverified(ctx, req.GetEmail(), req.GetPassword()); token != "" && s.mailer != nil {
				_ = s.mailer.Send(strings.ToLower(strings.TrimSpace(req.GetEmail())),
					"Confirm your OpenPak email",
					"Confirm your email address: "+emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.Verify, token))
			}
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
	verifyURL := emaillink.Build(s.baseURL, s.siteBaseURL, emaillink.Verify, token)
	body := "Welcome to OpenPak!\n\nConfirm your email address by opening this link:\n" + verifyURL +
		"\n\nIf you did not create this account, ignore this message."
	if err := s.mailer.Send(acct.Email, "Confirm your OpenPak email", body); err != nil {
		return nil, status.Error(codes.Internal, "verification email could not be queued")
	}
	return resp, nil
}

// SetDisplayName implements the adapter-delegated rename: the name a person
// chose on a console becomes the account's.
func (s *Server) SetDisplayName(ctx context.Context, req *accountv1.SetDisplayNameRequest) (*accountv1.SetDisplayNameResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetCallerNamespace() == "" || req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "caller_namespace and account_id are required")
	}
	switch err := s.identity.SetDisplayName(ctx, req.GetAccountId(), req.GetDisplayName()); {
	case err == nil:
		return &accountv1.SetDisplayNameResponse{}, nil
	case errors.Is(err, identity.ErrNotFound):
		return nil, status.Error(codes.NotFound, "no active account")
	case errors.Is(err, identity.ErrBadDisplayName):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	default:
		return nil, status.Error(codes.Internal, "internal error")
	}
}
