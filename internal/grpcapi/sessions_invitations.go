// Session registry and invitation RPC handlers (PRD SW-06/SW-07).
package grpcapi

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/invitations"
	"openpak/account/internal/sessions"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) RegisterSession(ctx context.Context, req *accountv1.RegisterSessionRequest) (*accountv1.RegisterSessionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, lease, err := s.sessions.Register(ctx, req.GetAccountId(), req.GetNamespace(),
		req.GetTitleId(), req.GetEndpointRef(), time.Duration(req.GetLeaseSeconds())*time.Second)
	if err != nil {
		return nil, mapSessionErr(err)
	}
	return &accountv1.RegisterSessionResponse{SessionId: id, LeaseSeconds: int32(lease / time.Second)}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *accountv1.HeartbeatRequest) (*accountv1.HeartbeatResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	lease, err := s.sessions.Heartbeat(ctx, req.GetSessionId(), 0)
	if err != nil {
		return nil, mapSessionErr(err)
	}
	return &accountv1.HeartbeatResponse{LeaseSeconds: int32(lease / time.Second)}, nil
}

func (s *Server) ExpireSession(ctx context.Context, req *accountv1.ExpireSessionRequest) (*accountv1.ExpireSessionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if err := s.sessions.Expire(ctx, req.GetSessionId()); err != nil {
		return nil, mapSessionErr(err)
	}
	return &accountv1.ExpireSessionResponse{}, nil
}

func (s *Server) ResolveSession(ctx context.Context, req *accountv1.ResolveSessionRequest) (*accountv1.ResolveSessionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	sess, err := s.sessions.Resolve(ctx, req.GetAccountId(), req.GetTitleId())
	if err != nil {
		return nil, mapSessionErr(err)
	}
	return &accountv1.ResolveSessionResponse{
		SessionId:   sess.ID,
		Namespace:   sess.Namespace,
		EndpointRef: sess.EndpointRef,
	}, nil
}

func (s *Server) SendInvitation(ctx context.Context, req *accountv1.SendInvitationRequest) (*accountv1.SendInvitationResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	in, err := s.invitations.Send(ctx, req.GetFromAccountId(), req.GetToAccountId(),
		req.GetTitleId(), req.GetPayload(), time.Duration(req.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, mapInvitationErr(err)
	}
	return &accountv1.SendInvitationResponse{InvitationId: in.ID, Nonce: in.Nonce}, nil
}

func (s *Server) PollInvitations(ctx context.Context, req *accountv1.PollInvitationsRequest) (*accountv1.PollInvitationsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ins, err := s.invitations.Poll(ctx, req.GetNamespace(), req.GetAccountId(), req.GetLimit())
	if err != nil {
		return nil, mapInvitationErr(err)
	}
	resp := &accountv1.PollInvitationsResponse{}
	for _, in := range ins {
		resp.Invitations = append(resp.Invitations, &accountv1.Invitation{
			InvitationId:    in.ID,
			FromAccountId:   in.FromAccountID,
			FromDisplayName: in.FromDisplayName,
			TitleId:         in.TitleID,
			Payload:         in.Payload,
			Nonce:           in.Nonce,
			ExpiresAtUnix:   in.ExpiresAtUnix,
		})
	}
	return resp, nil
}

func (s *Server) AckInvitations(ctx context.Context, req *accountv1.AckInvitationsRequest) (*accountv1.AckInvitationsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	acked, err := s.invitations.Ack(ctx, req.GetNamespace(), req.GetAccountId(),
		req.GetInvitationIds(), req.GetNonces())
	if err != nil {
		return nil, mapInvitationErr(err)
	}
	return &accountv1.AckInvitationsResponse{Acked: int32(acked)}, nil
}

func mapSessionErr(err error) error {
	switch {
	case errors.Is(err, sessions.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, sessions.ErrAccountUnknown):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sessions.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}

func mapInvitationErr(err error) error {
	switch {
	case errors.Is(err, invitations.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, invitations.ErrSelfInvite):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, invitations.ErrDenied):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, invitations.ErrNoLiveSession):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}

// GetPresence answers who, among the given accounts, is online and in which
// title. Read-only: it registers nothing and renews no lease.
func (s *Server) GetPresence(ctx context.Context, req *accountv1.GetPresenceRequest) (*accountv1.GetPresenceResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ps, err := s.sessions.GetPresence(ctx, req.GetAccountIds())
	if err != nil {
		return nil, mapSessionErr(err)
	}
	resp := &accountv1.GetPresenceResponse{}
	for _, p := range ps {
		resp.Presence = append(resp.Presence, &accountv1.Presence{
			AccountId: p.AccountID, Namespace: p.Namespace, TitleId: p.TitleID, SinceUnix: p.SinceUnix,
		})
	}
	return resp, nil
}

// ListInvitations reads an account's pending invitations without consuming
// them. PollInvitations remains the only delivery path.
func (s *Server) ListInvitations(ctx context.Context, req *accountv1.ListInvitationsRequest) (*accountv1.ListInvitationsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ins, err := s.invitations.List(ctx, req.GetAccountId(), req.GetLimit())
	if err != nil {
		return nil, mapInvitationErr(err)
	}
	resp := &accountv1.ListInvitationsResponse{}
	for _, in := range ins {
		resp.Invitations = append(resp.Invitations, &accountv1.InvitationSummary{
			InvitationId:    in.ID,
			FromAccountId:   in.FromAccountID,
			FromDisplayName: in.FromDisplayName,
			TitleId:         in.TitleID,
			Namespace:       in.Namespace,
			ExpiresAtUnix:   in.ExpiresAtUnix,
		})
	}
	return resp, nil
}

// RejectInvitation declines one, scoped to the recipient.
func (s *Server) RejectInvitation(ctx context.Context, req *accountv1.RejectInvitationRequest) (*accountv1.RejectInvitationResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ok, err := s.invitations.Reject(ctx, req.GetAccountId(), req.GetInvitationId())
	if err != nil {
		return nil, mapInvitationErr(err)
	}
	return &accountv1.RejectInvitationResponse{Rejected: ok}, nil
}
