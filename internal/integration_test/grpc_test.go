//go:build integration

package integration_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"openpak/account/internal/events"
	"openpak/account/internal/grpcapi"
	"openpak/account/internal/identity"
	"openpak/account/internal/invitations"
	"openpak/account/internal/links"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

const grpcInternalKey = "test-internal-key-0123456789abcdef0123456789"

func startGRPC(t *testing.T) (accountv1.IdentityClient, accountv1.LinksClient, accountv1.SocialClient, accountv1.SessionsClient, accountv1.InvitationsClient, accountv1.EventsClient, *identity.Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	evSvc := events.New(pool)
	ident := identity.New(pool, "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, evSvc)
	lnks := links.New(pool, evSvc)
	soc := social.New(pool, evSvc)
	sessSvc := sessions.New(pool)
	invSvc := invitations.New(pool, soc, testResolver{sessSvc}, evSvc)

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcapi.Interceptor(grpcInternalKey)))
	api := grpcapi.New(ident, lnks, evSvc, soc, sessSvc, invSvc, grpcapi.Options{})
	accountv1.RegisterIdentityServer(srv, api)
	accountv1.RegisterLinksServer(srv, api)
	accountv1.RegisterEventsServer(srv, api)
	accountv1.RegisterSocialServer(srv, api)
	accountv1.RegisterSessionsServer(srv, api)
	accountv1.RegisterInvitationsServer(srv, api)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return accountv1.NewIdentityClient(conn), accountv1.NewLinksClient(conn),
		accountv1.NewSocialClient(conn), accountv1.NewSessionsClient(conn),
		accountv1.NewInvitationsClient(conn), accountv1.NewEventsClient(conn), ident
}

// testResolver adapts sessions.Service to invitations.Resolver (mirrors
// cmd/account's sessionRefResolver).
type testResolver struct{ s *sessions.Service }

func (r testResolver) Resolve(ctx context.Context, accountID, titleID string) (*invitations.SessionRef, error) {
	sess, err := r.s.Resolve(ctx, accountID, titleID)
	if err != nil {
		return nil, err
	}
	return &invitations.SessionRef{ID: sess.ID, Namespace: sess.Namespace}, nil
}

func TestGRPCAuthRequired(t *testing.T) {
	idc, lc, _, _, _, _, _ := startGRPC(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// No credentials → unauthenticated on every method.
	if _, err := idc.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: "x"}); err == nil {
		t.Fatal("expected unauthenticated without internal key")
	}
	if _, err := lc.GetLinkBySubject(ctx, &accountv1.GetLinkBySubjectRequest{Namespace: "n", SubjectId: "s"}); err == nil {
		t.Fatal("expected unauthenticated without internal key")
	}
	// Wrong key → unauthenticated.
	bad := metadata.AppendToOutgoingContext(ctx, "x-internal-key", "wrong-key-wrong-key-wrong-key-1234")
	if _, err := idc.GetAccount(bad, &accountv1.GetAccountRequest{AccountId: "x"}); err == nil {
		t.Fatal("expected unauthenticated with wrong key")
	}
}

func TestGRPCLinkContract(t *testing.T) {
	idc, lc, _, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)

	acct, _, err := ident.Register(ctx, "grpc@example.com", "correct horse battery", "G")
	if err != nil {
		t.Fatal(err)
	}
	res, err := lc.ReserveLink(ctx, &accountv1.ReserveLinkRequest{
		AccountId: acct.ID, Namespace: "synthetic", SubjectId: "subj-1", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := lc.ActivateLink(ctx, &accountv1.ActivateLinkRequest{LinkId: res.LinkId, IdempotencyKey: "k2"}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got, err := lc.GetLinkBySubject(ctx, &accountv1.GetLinkBySubjectRequest{Namespace: "synthetic", SubjectId: "subj-1"})
	if err != nil || got.State != accountv1.LinkState_LINK_STATE_ACTIVE {
		t.Fatalf("get: %+v %v", got, err)
	}
	// Verification contract: correct password → ACTIVE with account id.
	vp, err := idc.VerifyPassword(ctx, &accountv1.VerifyPasswordRequest{
		CallerNamespace: "synthetic", Purpose: "link", Email: "grpc@example.com", Password: "correct horse battery"})
	if err != nil || vp.Status != accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE {
		t.Fatalf("verify: %+v %v", vp, err)
	}
	// Wrong password → unauthenticated, no status leak.
	if _, err := idc.VerifyPassword(ctx, &accountv1.VerifyPasswordRequest{
		CallerNamespace: "synthetic", Purpose: "link", Email: "grpc@example.com", Password: "wrong password!"}); err == nil {
		t.Fatal("expected error for wrong password")
	}
	// Unlink contract.
	if _, err := lc.UnlinkLink(ctx, &accountv1.UnlinkLinkRequest{LinkId: res.LinkId, IdempotencyKey: "k3"}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
}

func TestGRPCGetActiveLinkByAccount(t *testing.T) {
	_, lc, _, _, _, _, ident := startGRPC(t)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)

	acct, _, err := ident.Register(ctx, "active-link@example.com", "correct horse battery", "A")
	if err != nil {
		t.Fatal(err)
	}
	// No active link yet.
	if _, err := lc.GetActiveLinkByAccount(ctx, &accountv1.GetActiveLinkByAccountRequest{
		Namespace: "switch", AccountId: acct.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound before linking, got %v", err)
	}
	res, err := lc.ReserveLink(ctx, &accountv1.ReserveLinkRequest{
		AccountId: acct.ID, Namespace: "switch", SubjectId: "sw1:01929e60-3e21-4d78-8219-da68c17e6838",
		IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lc.ActivateLink(ctx, &accountv1.ActivateLinkRequest{LinkId: res.LinkId, IdempotencyKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	got, err := lc.GetActiveLinkByAccount(ctx, &accountv1.GetActiveLinkByAccountRequest{
		Namespace: "switch", AccountId: acct.ID})
	if err != nil || got.GetSubjectId() != "sw1:01929e60-3e21-4d78-8219-da68c17e6838" || got.GetState() != accountv1.LinkState_LINK_STATE_ACTIVE {
		t.Fatalf("active lookup: %+v %v", got, err)
	}
	// Other namespaces see nothing.
	if _, err := lc.GetActiveLinkByAccount(ctx, &accountv1.GetActiveLinkByAccountRequest{
		Namespace: "wiiu", AccountId: acct.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound in other namespace, got %v", err)
	}
}
