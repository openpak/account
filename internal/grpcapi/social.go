// Social RPC handlers (PRD SW-05): canonical friends/blocks enforcement.
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/social"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) RequestFriend(ctx context.Context, req *accountv1.RequestFriendRequest) (*accountv1.RequestFriendResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	f, err := s.social.RequestFriend(ctx, req.GetRequesterId(), req.GetAddresseeId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.RequestFriendResponse{FriendshipId: f.ID, State: protoFriendState(f.State)}, nil
}

func (s *Server) AcceptFriend(ctx context.Context, req *accountv1.AcceptFriendRequest) (*accountv1.AcceptFriendResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	st, err := s.social.AcceptFriend(ctx, req.GetAddresseeId(), req.GetRequesterId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.AcceptFriendResponse{State: protoFriendState(st)}, nil
}

func (s *Server) RemoveFriend(ctx context.Context, req *accountv1.RemoveFriendRequest) (*accountv1.RemoveFriendResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if err := s.social.RemoveFriend(ctx, req.GetAccountId(), req.GetOtherId()); err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.RemoveFriendResponse{}, nil
}

func (s *Server) ListFriends(ctx context.Context, req *accountv1.ListFriendsRequest) (*accountv1.ListFriendsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	friends, err := s.social.ListFriends(ctx, req.GetAccountId())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	resp := &accountv1.ListFriendsResponse{}
	for _, f := range friends {
		resp.Friends = append(resp.Friends, &accountv1.Friend{AccountId: f.AccountID, DisplayName: f.DisplayName})
	}
	return resp, nil
}

func (s *Server) ListIncomingRequests(ctx context.Context, req *accountv1.ListIncomingRequestsRequest) (*accountv1.ListIncomingRequestsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	reqs, err := s.social.ListIncomingRequests(ctx, req.GetAccountId())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	resp := &accountv1.ListIncomingRequestsResponse{}
	for _, f := range reqs {
		resp.Requesters = append(resp.Requesters, &accountv1.Friend{AccountId: f.AccountID, DisplayName: f.DisplayName})
	}
	return resp, nil
}

func (s *Server) GetRelationship(ctx context.Context, req *accountv1.GetRelationshipRequest) (*accountv1.GetRelationshipResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	rel, err := s.social.GetRelationship(ctx, req.GetAccountId(), req.GetOtherId())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.GetRelationshipResponse{Relationship: protoRelationship(rel)}, nil
}

func (s *Server) CheckInteraction(ctx context.Context, req *accountv1.CheckInteractionRequest) (*accountv1.CheckInteractionResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetKind() == accountv1.InteractionKind_INTERACTION_KIND_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	allowed, reason, err := s.social.CheckInteraction(ctx, req.GetActorId(), req.GetTargetId())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	resp := &accountv1.CheckInteractionResponse{Allowed: allowed}
	switch reason {
	case "":
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_ALLOWED
	case "blocked":
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_BLOCKED
	case "not_friends":
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_NOT_FRIENDS
	case "account_inactive":
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_ACCOUNT_INACTIVE
	case "self":
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_SELF
	default:
		resp.Reason = accountv1.DenialReason_DENIAL_REASON_UNSPECIFIED
	}
	return resp, nil
}

func (s *Server) Block(ctx context.Context, req *accountv1.BlockRequest) (*accountv1.BlockResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if err := s.social.Block(ctx, req.GetBlockerId(), req.GetBlockedId()); err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.BlockResponse{}, nil
}

func (s *Server) Unblock(ctx context.Context, req *accountv1.UnblockRequest) (*accountv1.UnblockResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if err := s.social.Unblock(ctx, req.GetBlockerId(), req.GetBlockedId()); err != nil {
		return nil, mapSocialErr(err)
	}
	return &accountv1.UnblockResponse{}, nil
}

func mapSocialErr(err error) error {
	switch {
	case errors.Is(err, social.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, social.ErrSelfRequest):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, social.ErrAccountUnknown):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, social.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, social.ErrBlocked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, social.ErrNotPending):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, social.ErrReversePending):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}

func protoFriendState(s social.State) accountv1.FriendState {
	if s == social.Accepted {
		return accountv1.FriendState_FRIEND_STATE_ACCEPTED
	}
	return accountv1.FriendState_FRIEND_STATE_PENDING
}

func protoRelationship(r social.Relationship) accountv1.Relationship {
	switch r {
	case social.RelFriends:
		return accountv1.Relationship_RELATIONSHIP_FRIENDS
	case social.RelPendingIncoming:
		return accountv1.Relationship_RELATIONSHIP_PENDING_INCOMING
	case social.RelPendingOutgoing:
		return accountv1.Relationship_RELATIONSHIP_PENDING_OUTGOING
	case social.RelBlockedBySelf:
		return accountv1.Relationship_RELATIONSHIP_BLOCKED_BY_SELF
	case social.RelBlockedByOther:
		return accountv1.Relationship_RELATIONSHIP_BLOCKED_BY_OTHER
	default:
		return accountv1.Relationship_RELATIONSHIP_NONE
	}
}

func (s *Server) ListOutgoingRequests(ctx context.Context, req *accountv1.ListOutgoingRequestsRequest) (*accountv1.ListOutgoingRequestsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	reqs, err := s.social.ListOutgoingRequests(ctx, req.GetAccountId())
	if err != nil {
		return nil, mapSocialErr(err)
	}
	resp := &accountv1.ListOutgoingRequestsResponse{}
	for _, f := range reqs {
		resp.Addressees = append(resp.Addressees, &accountv1.Friend{AccountId: f.AccountID, DisplayName: f.DisplayName})
	}
	return resp, nil
}
