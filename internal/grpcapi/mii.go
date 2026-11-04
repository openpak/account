// Mii RPC handlers: the account's primary Mii, shared by every console network.
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/federation"
	"openpak/account/internal/mii"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

func (s *Server) miiCaller(ctx context.Context, homeOnly bool) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if s.mii == nil {
		return status.Error(codes.Unimplemented, "mii not configured")
	}
	if homeOnly && NodeFromContext(ctx) != federation.Home {
		return status.Error(codes.PermissionDenied, "home only")
	}
	return nil
}

func miiFormat(f accountv1.MiiFormat) (mii.Format, error) {
	switch f {
	case accountv1.MiiFormat_MII_FORMAT_VER3:
		return mii.FormatVer3, nil
	case accountv1.MiiFormat_MII_FORMAT_RFL:
		return mii.FormatRFL, nil
	case accountv1.MiiFormat_MII_FORMAT_CHAR_INFO:
		return mii.FormatCharInfo, nil
	}
	return 0, status.Error(codes.InvalidArgument, "mii: format must be VER3, RFL or CHAR_INFO")
}

func (s *Server) PutMii(ctx context.Context, req *accountv1.PutMiiRequest) (*accountv1.PutMiiResponse, error) {
	if err := s.miiCaller(ctx, true); err != nil {
		return nil, err
	}
	f, err := miiFormat(req.GetFormat())
	if err != nil {
		return nil, err
	}
	v, changed, err := s.mii.Put(ctx, req.GetAccountId(), f, req.GetData())
	if err != nil {
		return nil, mapMiiErr(err)
	}
	return &accountv1.PutMiiResponse{Version: v, Changed: changed}, nil
}

func (s *Server) GetMii(ctx context.Context, req *accountv1.GetMiiRequest) (*accountv1.GetMiiResponse, error) {
	if err := s.miiCaller(ctx, false); err != nil {
		return nil, err
	}
	f, err := miiFormat(req.GetFormat())
	if err != nil {
		return nil, err
	}
	m, err := s.mii.Get(ctx, req.GetAccountId(), f, req.GetIfNoneMatch())
	if err != nil {
		return nil, mapMiiErr(err)
	}
	return &accountv1.GetMiiResponse{Data: m.Data, Version: m.Version, Name: m.Name,
		UpdatedAtUnix: m.UpdatedAt.Unix(), NotModified: m.Data == nil}, nil
}

func (s *Server) DeleteMii(ctx context.Context, req *accountv1.DeleteMiiRequest) (*accountv1.DeleteMiiResponse, error) {
	if err := s.miiCaller(ctx, true); err != nil {
		return nil, err
	}
	if err := s.mii.Delete(ctx, req.GetAccountId()); err != nil {
		return nil, mapMiiErr(err)
	}
	return &accountv1.DeleteMiiResponse{}, nil
}

func mapMiiErr(err error) error {
	switch {
	case errors.Is(err, mii.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, mii.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
