// Moderation RPC handlers (website contract requests CR-5, CR-6).
package grpcapi

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/account/internal/federation"
	"openpak/account/internal/identity"
	"openpak/account/internal/moderation"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// homeCaller is authorize plus the rule that only the home key (the website,
// the core's own operators) may moderate. Unlike homeOnly it does not need
// the federation register: without one every caller is home.
func (s *Server) homeCaller(ctx context.Context) error {
	if err := s.authorize(ctx); err != nil {
		return err
	}
	if s.moderation == nil {
		return status.Error(codes.Unimplemented, "moderation not configured")
	}
	if NodeFromContext(ctx) != federation.Home {
		return status.Error(codes.PermissionDenied, "home only")
	}
	return nil
}

func (s *Server) SearchAccounts(ctx context.Context, req *accountv1.SearchAccountsRequest) (*accountv1.SearchAccountsResponse, error) {
	if err := s.homeCaller(ctx); err != nil {
		return nil, err
	}
	found, err := s.moderation.Search(ctx, req.GetQuery(), req.GetLimit())
	if err != nil {
		return nil, mapModerationErr(err)
	}
	resp := &accountv1.SearchAccountsResponse{}
	for _, a := range found {
		resp.Accounts = append(resp.Accounts, &accountv1.ModeratedAccount{
			AccountId: a.ID, Email: a.Email, DisplayName: a.DisplayName,
			Status: protoStatus(identity.Status(a.Status)), EmailVerified: a.EmailVerified,
			CreatedAtUnix: a.CreatedAt.Unix(), BanReason: a.BanReason, MatchedSubjects: a.MatchedSubjects,
		})
	}
	return resp, nil
}

func (s *Server) SetAccountBanned(ctx context.Context, req *accountv1.SetAccountBannedRequest) (*accountv1.SetAccountBannedResponse, error) {
	if err := s.homeCaller(ctx); err != nil {
		return nil, err
	}
	reason, actor := strings.TrimSpace(req.GetReason()), strings.TrimSpace(req.GetActor())
	if reason == "" || actor == "" || len(reason) > moderation.MaxResolution || len(actor) > 128 {
		return nil, status.Error(codes.InvalidArgument, "reason and actor are required")
	}
	if !moderation.IsUUID(req.GetAccountId()) {
		return nil, status.Error(codes.NotFound, "account not found")
	}
	if err := s.identity.SetBannedBy(ctx, req.GetAccountId(), req.GetBanned(), reason, actor); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}
	st := accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE
	if req.GetBanned() {
		st = accountv1.AccountStatus_ACCOUNT_STATUS_BANNED
	}
	return &accountv1.SetAccountBannedResponse{Status: st}, nil
}

// FileReport is open to every authenticated caller: consoles on region nodes
// report players too.
func (s *Server) FileReport(ctx context.Context, req *accountv1.FileReportRequest) (*accountv1.FileReportResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if s.moderation == nil {
		return nil, status.Error(codes.Unimplemented, "moderation not configured")
	}
	r, err := s.moderation.File(ctx, req.GetReporterId(), req.GetSubjectAccountId(), req.GetKind(), req.GetDetail())
	if err != nil {
		return nil, mapModerationErr(err)
	}
	return &accountv1.FileReportResponse{Report: protoReport(r)}, nil
}

func (s *Server) ListReports(ctx context.Context, req *accountv1.ListReportsRequest) (*accountv1.ListReportsResponse, error) {
	if err := s.homeCaller(ctx); err != nil {
		return nil, err
	}
	st := ""
	switch req.GetStatus() {
	case accountv1.ReportStatus_REPORT_STATUS_OPEN:
		st = "open"
	case accountv1.ReportStatus_REPORT_STATUS_RESOLVED:
		st = "resolved"
	}
	list, err := s.moderation.List(ctx, st, req.GetLimit())
	if err != nil {
		return nil, mapModerationErr(err)
	}
	resp := &accountv1.ListReportsResponse{}
	for i := range list {
		resp.Reports = append(resp.Reports, protoReport(&list[i]))
	}
	return resp, nil
}

func (s *Server) ResolveReport(ctx context.Context, req *accountv1.ResolveReportRequest) (*accountv1.ResolveReportResponse, error) {
	if err := s.homeCaller(ctx); err != nil {
		return nil, err
	}
	r, err := s.moderation.Resolve(ctx, req.GetReportId(), req.GetResolution(), req.GetActor())
	if err != nil {
		return nil, mapModerationErr(err)
	}
	return &accountv1.ResolveReportResponse{Report: protoReport(r)}, nil
}

func protoReport(r *moderation.Report) *accountv1.Report {
	out := &accountv1.Report{
		ReportId: r.ID, ReporterId: r.ReporterID, ReporterDisplayName: r.ReporterName,
		SubjectAccountId: r.SubjectAccountID, SubjectDisplayName: r.SubjectName,
		Kind: r.Kind, Detail: r.Detail, Resolution: r.Resolution, ResolvedBy: r.ResolvedBy,
		CreatedAtUnix: r.CreatedAt.Unix(),
		Status:        accountv1.ReportStatus_REPORT_STATUS_OPEN,
	}
	if r.Status == "resolved" {
		out.Status = accountv1.ReportStatus_REPORT_STATUS_RESOLVED
		out.ResolvedAtUnix = r.ResolvedAt.Unix()
	}
	return out
}

func mapModerationErr(err error) error {
	switch {
	case errors.Is(err, moderation.ErrInvalid), errors.Is(err, moderation.ErrSelfReport):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, moderation.ErrNotFound), errors.Is(err, moderation.ErrUnknownUser):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, moderation.ErrNotOpen):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
