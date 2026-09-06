// Package grpcapi implements the internal gRPC contracts consumed by console
// adapters (PRD §7 internal contracts).
package grpcapi

import (
	"context"
	"crypto/subtle"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"openpak/account/internal/events"
	"openpak/account/internal/identity"
	"openpak/account/internal/links"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

type Server struct {
	accountv1.UnimplementedIdentityServer
	accountv1.UnimplementedLinksServer
	accountv1.UnimplementedEventsServer
	identity *identity.Service
	links    *links.Service
	events   *events.Service
}

func New(ident *identity.Service, lnks *links.Service, evs *events.Service) *Server {
	return &Server{identity: ident, links: lnks, events: evs}
}

func (s *Server) PollEvents(ctx context.Context, req *accountv1.PollEventsRequest) (*accountv1.PollEventsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.events.Poll(ctx, req.GetSinceVersion(), req.GetLimit())
}

// Interceptor returns the server interceptor enforcing internal credentials.
// An empty internal key is a startup failure (config), so this can never
// degrade to unauthenticated access.
func Interceptor(internalKey string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		v, ok := credsFromContext(ctx)
		if !ok || v == "" || subtle.ConstantTimeCompare([]byte(v), []byte(internalKey)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid internal credentials")
		}
		return handler(ctx, req)
	}
}

func credsFromContext(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	if vals := md.Get("x-internal-key"); len(vals) > 0 {
		return vals[0], true
	}
	if vals := md.Get("authorization"); len(vals) > 0 {
		const prefix = "Bearer "
		if len(vals[0]) > len(prefix) && vals[0][:len(prefix)] == prefix {
			return vals[0][len(prefix):], true
		}
	}
	return "", false
}

// authorize is redundant with the interceptor for direct-call safety.
func (s *Server) authorize(ctx context.Context) error {
	v, ok := credsFromContext(ctx)
	if !ok || v == "" {
		return status.Error(codes.Unauthenticated, "missing internal credentials")
	}
	return nil
}

func (s *Server) VerifyPassword(ctx context.Context, req *accountv1.VerifyPasswordRequest) (*accountv1.VerifyPasswordResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetPurpose() == "" || req.GetCallerNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "caller_namespace and purpose are required")
	}
	acct, err := s.identity.VerifyCredentials(ctx, req.GetEmail(), req.GetPassword())
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrBadCredentials):
			return nil, status.Error(codes.Unauthenticated, "invalid credentials")
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

func (s *Server) GetAccount(ctx context.Context, req *accountv1.GetAccountRequest) (*accountv1.GetAccountResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	// Implemented against the DB directly to avoid session side effects.
	a, err := s.identity.GetByID(ctx, req.GetAccountId())
	if err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &accountv1.GetAccountResponse{
		AccountId:     a.ID,
		Status:        protoStatus(a.Status),
		EmailVerified: a.EmailVerified,
		DisplayName:   a.DisplayName,
	}, nil
}

func protoStatus(s identity.Status) accountv1.AccountStatus {
	switch s {
	case identity.StatusActive:
		return accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE
	case identity.StatusBanned:
		return accountv1.AccountStatus_ACCOUNT_STATUS_BANNED
	case identity.StatusDeletionPending:
		return accountv1.AccountStatus_ACCOUNT_STATUS_DELETION_PENDING
	default:
		return accountv1.AccountStatus_ACCOUNT_STATUS_DELETED
	}
}

func linkState(s links.State) accountv1.LinkState {
	switch s {
	case links.Pending:
		return accountv1.LinkState_LINK_STATE_PENDING
	case links.Active:
		return accountv1.LinkState_LINK_STATE_ACTIVE
	case links.Revoking:
		return accountv1.LinkState_LINK_STATE_REVOKING
	case links.Revoked:
		return accountv1.LinkState_LINK_STATE_REVOKED
	}
	return accountv1.LinkState_LINK_STATE_UNSPECIFIED
}

func (s *Server) ReserveLink(ctx context.Context, req *accountv1.ReserveLinkRequest) (*accountv1.ReserveLinkResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	l, err := s.links.Reserve(ctx, req.GetAccountId(), req.GetNamespace(), req.GetSubjectId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapLinkErr(err)
	}
	return &accountv1.ReserveLinkResponse{LinkId: l.ID, State: linkState(l.State)}, nil
}

func (s *Server) ActivateLink(ctx context.Context, req *accountv1.ActivateLinkRequest) (*accountv1.ActivateLinkResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	st, err := s.links.Activate(ctx, req.GetLinkId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapLinkErr(err)
	}
	return &accountv1.ActivateLinkResponse{State: linkState(st)}, nil
}

func (s *Server) UnlinkLink(ctx context.Context, req *accountv1.UnlinkLinkRequest) (*accountv1.UnlinkLinkResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	st, err := s.links.Unlink(ctx, req.GetLinkId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapLinkErr(err)
	}
	return &accountv1.UnlinkLinkResponse{State: linkState(st)}, nil
}

func (s *Server) GetLinkBySubject(ctx context.Context, req *accountv1.GetLinkBySubjectRequest) (*accountv1.GetLinkBySubjectResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	l, err := s.links.GetBySubject(ctx, req.GetNamespace(), req.GetSubjectId())
	if err != nil {
		if errors.Is(err, links.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "link not found")
		}
		return nil, mapLinkErr(err)
	}
	return &accountv1.GetLinkBySubjectResponse{LinkId: l.ID, AccountId: l.AccountID, State: linkState(l.State)}, nil
}

func mapLinkErr(err error) error {
	switch {
	case errors.Is(err, links.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, links.ErrNotPending):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, links.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, links.ErrAccountUnknown):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}

var _ = emptypb.Empty{} // keep import stable if messages change
