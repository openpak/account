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
		req.GetTitleId(), req.GetEndpointRef(), NodeFromContext(ctx), time.Duration(req.GetLeaseSeconds())*time.Second, req.GetClient(), req.GetOs())
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
			AccountId: p.AccountID, Namespace: p.Namespace, TitleId: p.TitleID, SinceUnix: p.SinceUnix, NodeId: p.NodeID, Client: p.Client, Os: p.OS,
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

// GetPlayerCounts answers who is playing what. Read-only, and cheap enough for
// a status page to ask on every render.
func (s *Server) GetPlayerCounts(ctx context.Context, _ *accountv1.GetPlayerCountsRequest) (*accountv1.GetPlayerCountsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	titles, networks, nodes, total, err := s.sessions.PlayerCounts(ctx)
	if err != nil {
		return nil, mapSessionErr(err)
	}
	resp := &accountv1.GetPlayerCountsResponse{TotalPlayers: int32(total)}
	for _, t := range titles {
		resp.Titles = append(resp.Titles, &accountv1.TitlePlayers{
			TitleId: t.TitleID, Namespace: t.Namespace, Players: int32(t.Players),
		})
	}
	for _, n := range networks {
		resp.Networks = append(resp.Networks, &accountv1.NetworkPlayers{
			Namespace: n.Namespace, Players: int32(n.Players),
		})
	}
	for _, n := range nodes {
		resp.Nodes = append(resp.Nodes, &accountv1.NodePlayers{NodeId: n.NodeID, Players: int32(n.Players)})
	}
	return resp, nil
}

// GetPlaytime answers how long each title has been played on each client and
// OS. Aggregates only; no account appears in it.
func (s *Server) GetPlaytime(ctx context.Context, _ *accountv1.GetPlaytimeRequest) (*accountv1.GetPlaytimeResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ts, err := s.sessions.Playtime(ctx)
	if err != nil {
		return nil, mapSessionErr(err)
	}
	resp := &accountv1.GetPlaytimeResponse{}
	for _, t := range ts {
		resp.Tallies = append(resp.Tallies, &accountv1.PlaytimeTally{
			Namespace: t.Namespace, TitleId: t.TitleID, Client: t.Client, Os: t.OS,
			TotalSeconds: t.TotalSeconds, LongestSeconds: t.LongestSeconds, Sessions: int32(t.Sessions),
		})
	}
	return resp, nil
}

// GetPlayHistory is one account's titles and play time in a namespace; see sessions.PlayHistory.
func (s *Server) GetPlayHistory(ctx context.Context, req *accountv1.GetPlayHistoryRequest) (*accountv1.GetPlayHistoryResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	entries, err := s.sessions.PlayHistory(ctx, req.GetAccountId(), req.GetNamespace(), int(req.GetLimit()))
	if err != nil {
		return nil, mapSessionErr(err)
	}
	resp := &accountv1.GetPlayHistoryResponse{}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, &accountv1.PlayHistoryEntry{
			TitleId: e.TitleID, Sessions: int32(e.Sessions), TotalSeconds: e.TotalSeconds,
			FirstPlayedUnix: e.FirstPlayed, LastPlayedUnix: e.LastPlayed,
		})
	}
	return resp, nil
}

// MarkOnline is a game server confirming who is connected to it; see sessions.MarkOnline.
func (s *Server) MarkOnline(ctx context.Context, req *accountv1.MarkOnlineRequest) (*accountv1.MarkOnlineResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	players := make([]sessions.OnlinePlayer, 0, len(req.GetPlayers()))
	for _, p := range req.GetPlayers() {
		players = append(players, sessions.OnlinePlayer{AccountID: p.GetAccountId(), Namespace: p.GetNamespace(), TitleID: p.GetTitleId()})
	}
	n, err := s.sessions.MarkOnline(ctx, players)
	if err != nil {
		return nil, mapSessionErr(err)
	}
	return &accountv1.MarkOnlineResponse{Marked: int32(n)}, nil
}
