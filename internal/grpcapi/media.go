// Media RPC handlers (website contract request CR-2).
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/federation"
	"openpak/account/internal/media"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) mediaCaller(ctx context.Context, homeOnly bool) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if s.media == nil {
		return status.Error(codes.Unimplemented, "media not configured")
	}
	if homeOnly && NodeFromContext(ctx) != federation.Home {
		return status.Error(codes.PermissionDenied, "home only")
	}
	return nil
}

func (s *Server) PutAvatar(ctx context.Context, req *accountv1.PutAvatarRequest) (*accountv1.PutAvatarResponse, error) {
	if err := s.mediaCaller(ctx, true); err != nil {
		return nil, err
	}
	v, err := s.media.Put(ctx, req.GetAccountId(), req.GetPng_256(), req.GetPng_512())
	if err != nil {
		return nil, mapMediaErr(err)
	}
	return &accountv1.PutAvatarResponse{Version: v}, nil
}

func (s *Server) GetAvatar(ctx context.Context, req *accountv1.GetAvatarRequest) (*accountv1.GetAvatarResponse, error) {
	if err := s.mediaCaller(ctx, false); err != nil {
		return nil, err
	}
	a, err := s.media.Get(ctx, req.GetAccountId(), int(req.GetSize()), req.GetIfNoneMatch())
	if err != nil {
		return nil, mapMediaErr(err)
	}
	return &accountv1.GetAvatarResponse{Png: a.PNG, Version: a.Version, UpdatedAtUnix: a.UpdatedAt.Unix(),
		NotModified: a.PNG == nil}, nil
}

func (s *Server) DeleteAvatar(ctx context.Context, req *accountv1.DeleteAvatarRequest) (*accountv1.DeleteAvatarResponse, error) {
	if err := s.mediaCaller(ctx, true); err != nil {
		return nil, err
	}
	if err := s.media.Delete(ctx, req.GetAccountId()); err != nil {
		return nil, mapMediaErr(err)
	}
	return &accountv1.DeleteAvatarResponse{}, nil
}

func mapMediaErr(err error) error {
	switch {
	case errors.Is(err, media.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, media.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
