// Package grpcapi implements the internal gRPC contracts consumed by console
// adapters (PRD §7 internal contracts).
package grpcapi

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"openpak/account/internal/events"
	"openpak/account/internal/federation"
	"openpak/account/internal/identity"
	"openpak/account/internal/invitations"
	"openpak/account/internal/links"
	"openpak/account/internal/mailer"
	"openpak/account/internal/media"
	"openpak/account/internal/moderation"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

type Server struct {
	accountv1.UnimplementedIdentityServer
	accountv1.UnimplementedLinksServer
	accountv1.UnimplementedEventsServer
	accountv1.UnimplementedRegistrationServer
	accountv1.UnimplementedSocialServer
	accountv1.UnimplementedSessionsServer
	accountv1.UnimplementedInvitationsServer
	accountv1.UnimplementedFederationServer
	accountv1.UnimplementedModerationServer
	accountv1.UnimplementedMediaServer
	identity    *identity.Service
	federation  *federation.Service
	links       *links.Service
	events      *events.Service
	social      *social.Service
	sessions    *sessions.Service
	invitations *invitations.Service
	moderation  *moderation.Service
	media       *media.Service
	mailer      *mailer.Mailer
	baseURL     string
	siteBaseURL string
}

type Options struct {
	Mailer  *mailer.Mailer // nil = development mode (tokens returned to callers)
	BaseURL string         // the core's own public base URL
	// SiteBaseURL is the user-facing site (ACCOUNT_EMAIL_LINK_BASE). When set, emailed
	// links point at its routes instead of the core's, so the core can stay internal.
	SiteBaseURL string
	// Federation is the node register. Nil only in tests that never touch it.
	Federation *federation.Service
	// Moderation is account search and the reports queue. Nil answers
	// Unimplemented on the Moderation service.
	Moderation *moderation.Service
	// Media stores avatars. Nil answers Unimplemented on the Media service.
	Media *media.Service
}

func New(ident *identity.Service, lnks *links.Service, evs *events.Service, soc *social.Service, sess *sessions.Service, inv *invitations.Service, opts Options) *Server {
	if opts.BaseURL == "" {
		opts.BaseURL = "http://localhost:8000"
	}
	return &Server{identity: ident, links: lnks, events: evs, social: soc, sessions: sess, invitations: inv, mailer: opts.Mailer, baseURL: opts.BaseURL, siteBaseURL: opts.SiteBaseURL, federation: opts.Federation, moderation: opts.Moderation, media: opts.Media}
}

func (s *Server) PollEvents(ctx context.Context, req *accountv1.PollEventsRequest) (*accountv1.PollEventsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.events.Poll(ctx, req.GetSinceVersion(), req.GetLimit())
}

// SubscribeAccountEvents is the push form of the poll (universal-social-prd
// §4a). The unary interceptor does not cover streams, so the handler checks
// the credentials itself.
func (s *Server) SubscribeAccountEvents(req *accountv1.SubscribeAccountEventsRequest, stream accountv1.Events_SubscribeAccountEventsServer) error {
	ctx := stream.Context()
	if err := s.authorize(ctx); err != nil {
		return err
	}
	return s.events.Subscribe(ctx, req.GetSinceVersion(), func(ev *accountv1.InvalidationEvent) error {
		return stream.Send(ev)
	})
}

// EmitEvent places an internal service's fact on the universal stream. Same
// credentials as every other internal RPC.
func (s *Server) EmitEvent(ctx context.Context, req *accountv1.EmitEventRequest) (*accountv1.EmitEventResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetType() == "" || req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "type and account_id are required")
	}
	ev, err := s.events.EmitEvent(ctx, req.GetType(), req.GetAccountId(), req.GetNamespace(), req.GetSubjectId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &accountv1.EmitEventResponse{Event: ev}, nil
}

// NodeAuth resolves a presented credential to a federation caller, and says
// whether a region node is admitted (approved and in rotation).
type NodeAuth interface {
	Authenticate(ctx context.Context, key string) (federation.Caller, bool)
	Admitted(ctx context.Context, node string) bool
}

type nodeKey struct{}

// nodeMethods are what a region's keys may call whatever the node's state:
// enrol, announce, self-test, read the rules. Everything else (registering a
// session, which stamps it with the node) needs the node in rotation, so an
// unapproved or non-compliant node serves nothing and stamps nothing.
var nodeMethods = map[string]bool{
	"/openpak.account.v1.Federation/Announce":        true,
	"/openpak.account.v1.Federation/JoinServer":      true,
	"/openpak.account.v1.Federation/StartSelfTest":   true,
	"/openpak.account.v1.Federation/GetRequirements": true,
}

// Interceptor returns the server interceptor enforcing internal credentials.
// The credential names a node ("home" for ACCOUNT_INTERNAL_KEY, a region
// otherwise) and the caller travels in the context, so a handler that must
// know who called reads it from there and never from the request.
func Interceptor(auth NodeAuth) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		v, ok := credsFromContext(ctx)
		if !ok || v == "" {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid internal credentials")
		}
		caller, ok := auth.Authenticate(ctx, v)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid internal credentials")
		}
		if caller.Node != federation.Home && !nodeMethods[info.FullMethod] && !auth.Admitted(ctx, caller.Node) {
			return nil, status.Error(codes.PermissionDenied, "node is not approved or not in rotation")
		}
		return handler(context.WithValue(ctx, nodeKey{}, caller), req)
	}
}

// NodeFromContext is the node the interceptor authenticated; "home" when a
// handler is called without one (direct calls in tests).
func NodeFromContext(ctx context.Context) string {
	if c, ok := ctx.Value(nodeKey{}).(federation.Caller); ok && c.Node != "" {
		return c.Node
	}
	return federation.Home
}

// CallerFromContext is the full caller: node, and the server when a server key
// was presented.
func CallerFromContext(ctx context.Context) federation.Caller {
	if c, ok := ctx.Value(nodeKey{}).(federation.Caller); ok && c.Node != "" {
		return c
	}
	return federation.Caller{Node: federation.Home}
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
	need2FA, err := s.secondFactorGate(ctx, acct.ID, req.GetSecondFactorSupported())
	if err != nil {
		return nil, err
	}
	return &accountv1.VerifyPasswordResponse{
		AccountId:     acct.ID,
		Status:        accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE,
		EmailVerified: acct.EmailVerified,
		// The Switch adapter builds a user object from these; the console rejects it outright
		// when either is missing.
		Country:           acct.Country,
		Birthday:          acct.Birthday,
		TwoFactorRequired: need2FA,
	}, nil
}

// secondFactorGate applies the two-factor rule to a password that has just
// checked out (see VerifyPasswordRequest.second_factor_supported): callers
// that cannot run a second factor fail closed, the rest are told it is owed.
func (s *Server) secondFactorGate(ctx context.Context, accountID string, supported bool) (bool, error) {
	on, err := s.identity.TwoFactorEnabled(ctx, accountID)
	if err != nil {
		return false, status.Error(codes.Internal, "internal error")
	}
	if on && !supported {
		return false, status.Error(codes.FailedPrecondition, "two-factor authentication required")
	}
	return on, nil
}

// VerifySecondFactor is the second half of a password check for an account
// with two-factor authentication.
func (s *Server) VerifySecondFactor(ctx context.Context, req *accountv1.VerifySecondFactorRequest) (*accountv1.VerifySecondFactorResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" || (req.GetCode() == "" && req.GetRecoveryCode() == "") {
		return nil, status.Error(codes.InvalidArgument, "account_id and a code or recovery_code are required")
	}
	res, err := s.identity.VerifySecondFactor(ctx, req.GetAccountId(), req.GetCode(), req.GetRecoveryCode())
	switch {
	case err == nil:
		return &accountv1.VerifySecondFactorResponse{RecoveryCodeUsed: res.RecoveryCodeUsed,
			RecoveryCodesLeft: uint32(res.RecoveryCodesLeft)}, nil
	case errors.Is(err, identity.ErrBadSecondFactor):
		return nil, status.Error(codes.Unauthenticated, "invalid code")
	case errors.Is(err, identity.ErrTwoFactorLocked):
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, identity.ErrTwoFactorNotEnabled):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case strings.Contains(err.Error(), "SQLSTATE 22P02"):
		return nil, status.Error(codes.FailedPrecondition, identity.ErrTwoFactorNotEnabled.Error())
	}
	return nil, status.Error(codes.Internal, "internal error")
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
	twoFA, err := s.identity.TwoFactorEnabled(ctx, a.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &accountv1.GetAccountResponse{
		TwoFactorEnabled: twoFA,
		AccountId:        a.ID,
		Status:           protoStatus(a.Status),
		EmailVerified:    a.EmailVerified,
		DisplayName:      a.DisplayName,
		Bio:              a.Bio, Motto: a.Motto, ProfileVersion: a.ProfileVersion,
		Handle:         a.Handle,
		Country:        a.Country,
		Birthday:       a.Birthday,
		Email:          a.Email,
		EraseAfterUnix: eraseAfterUnix(ctx, s.identity, a),
	}, nil
}

func eraseAfterUnix(ctx context.Context, id *identity.Service, a *identity.Account) int64 {
	if a.Status != identity.StatusDeletionPending {
		return 0
	}
	if t, err := id.EraseAfter(ctx, a.ID); err == nil && !t.IsZero() {
		return t.Unix()
	}
	return 0
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
	return &accountv1.GetLinkBySubjectResponse{LinkId: l.ID, AccountId: l.AccountID, State: linkState(l.State), PublicCode: l.PublicCode}, nil
}

// GetActiveLinkByAccount resolves the account's active link in one adapter
// namespace (PRD SW-01): adapters call this before minting a subject so
// retries never create duplicates.
func (s *Server) GetActiveLinkByAccount(ctx context.Context, req *accountv1.GetActiveLinkByAccountRequest) (*accountv1.GetLinkBySubjectResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetNamespace() == "" || req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace and account_id are required")
	}
	all, err := s.links.ListByAccount(ctx, req.GetAccountId())
	if err != nil {
		return nil, mapLinkErr(err)
	}
	for _, l := range all {
		if l.Namespace == req.GetNamespace() && l.State == links.Active {
			return &accountv1.GetLinkBySubjectResponse{
				LinkId: l.ID, AccountId: l.AccountID, SubjectId: l.SubjectID,
				State: linkState(l.State), PublicCode: l.PublicCode,
			}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "no active link for account in namespace")
}

// SetLinkPublicCode publishes the adapter's human code on an active link
// (CR-3; docs/public-codes.md).
func (s *Server) SetLinkPublicCode(ctx context.Context, req *accountv1.SetLinkPublicCodeRequest) (*accountv1.SetLinkPublicCodeResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetLinkId() == "" {
		return nil, status.Error(codes.InvalidArgument, "link_id is required")
	}
	if err := s.links.SetPublicCode(ctx, req.GetLinkId(), req.GetPublicCode(), req.GetLookupKey()); err != nil {
		return nil, mapLinkErr(err)
	}
	return &accountv1.SetLinkPublicCodeResponse{}, nil
}

// ListAccountLinks is every active link of an account with its subject and
// published code, for surfaces that show them.
func (s *Server) ListAccountLinks(ctx context.Context, req *accountv1.ListAccountLinksRequest) (*accountv1.ListAccountLinksResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id is required")
	}
	all, err := s.links.ListByAccount(ctx, req.GetAccountId())
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 22P02") {
			return nil, status.Error(codes.InvalidArgument, "account_id is not an id")
		}
		return nil, mapLinkErr(err)
	}
	resp := &accountv1.ListAccountLinksResponse{}
	for _, l := range all {
		if l.State != links.Active || (req.GetNamespace() != "" && l.Namespace != req.GetNamespace()) {
			continue
		}
		resp.Links = append(resp.Links, &accountv1.AccountLink{
			LinkId: l.ID, Namespace: l.Namespace, SubjectId: l.SubjectID, PublicCode: l.PublicCode})
	}
	return resp, nil
}

func (s *Server) GetLinkByPublicCode(ctx context.Context, req *accountv1.GetLinkByPublicCodeRequest) (*accountv1.GetLinkBySubjectResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	l, err := s.links.GetByPublicCode(ctx, req.GetNamespace(), req.GetPublicCode())
	if err != nil {
		return nil, mapLinkErr(err)
	}
	return &accountv1.GetLinkBySubjectResponse{LinkId: l.ID, AccountId: l.AccountID, SubjectId: l.SubjectID,
		State: linkState(l.State), PublicCode: l.PublicCode}, nil
}

func mapLinkErr(err error) error {
	switch {
	case errors.Is(err, links.ErrNotActive):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, links.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
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
