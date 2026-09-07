// OpenPak account core: console-agnostic identity service (PRD M1).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"

	"openpak/account/internal/config"
	"openpak/account/internal/events"
	"openpak/account/internal/grpcapi"
	"openpak/account/internal/identity"
	"openpak/account/internal/invitations"
	"openpak/account/internal/links"
	"openpak/account/internal/mailer"
	"openpak/account/internal/sessions"
	"openpak/account/internal/social"
	"openpak/account/internal/storage"
	"openpak/account/internal/web"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// sessionRefResolver adapts sessions.Service to invitations.Resolver.
type sessionRefResolver struct{ s *sessions.Service }

func (r sessionRefResolver) Resolve(ctx context.Context, accountID, titleID string) (*invitations.SessionRef, error) {
	sess, err := r.s.Resolve(ctx, accountID, titleID)
	if err != nil {
		return nil, err
	}
	return &invitations.SessionRef{ID: sess.ID, Namespace: sess.Namespace}, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := storage.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := storage.Migrate(ctx, pool); err != nil {
		return err
	}

	sessionTTL, err := time.ParseDuration(cfg.SessionTTL)
	if err != nil {
		return errors.New("invalid ACCOUNT_SESSION_TTL: " + err.Error())
	}
	evSvc := events.New(pool)
	ident := identity.New(pool, cfg.SessionSecret, sessionTTL, evSvc)
	lnks := links.New(pool, evSvc)
	socSvc := social.New(pool)
	sessSvc := sessions.New(pool)
	invSvc := invitations.New(pool, socSvc, sessionRefResolver{sessSvc})

	// Outbound email. Production REQUIRES SMTP configuration: registration
	// and recovery are undeliverable without it (review follow-up P1 #1).
	var m *mailer.Mailer
	if os.Getenv("ACCOUNT_SMTP_HOST") != "" {
		port := 587
		if v := os.Getenv("ACCOUNT_SMTP_PORT"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &port); err != nil {
				return errors.New("invalid ACCOUNT_SMTP_PORT")
			}
		}
		m = mailer.New(mailer.Config{
			Host: os.Getenv("ACCOUNT_SMTP_HOST"), Port: port,
			Username: os.Getenv("ACCOUNT_SMTP_USERNAME"), Password: os.Getenv("ACCOUNT_SMTP_PASSWORD"),
			From: os.Getenv("ACCOUNT_SMTP_FROM"), NoTLSForLocalhost: cfg.Environment == "development",
		})
	} else if cfg.Environment == "production" {
		return errors.New("ACCOUNT_SMTP_HOST is required when ACCOUNT_ENVIRONMENT=production")
	}
	internalKey := os.Getenv("ACCOUNT_INTERNAL_KEY")
	if len(internalKey) < 32 {
		return errors.New("ACCOUNT_INTERNAL_KEY must be set (>=32 bytes); internal RPCs fail closed")
	}
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcapi.Interceptor(internalKey)))
	apiSrv := grpcapi.New(ident, lnks, evSvc, socSvc, sessSvc, invSvc, grpcapi.Options{Mailer: m, BaseURL: os.Getenv("ACCOUNT_PUBLIC_BASE_URL"), SiteBaseURL: os.Getenv("ACCOUNT_EMAIL_LINK_BASE")})
	accountv1.RegisterIdentityServer(grpcSrv, apiSrv)
	accountv1.RegisterLinksServer(grpcSrv, apiSrv)
	accountv1.RegisterEventsServer(grpcSrv, apiSrv)
	accountv1.RegisterRegistrationServer(grpcSrv, apiSrv)
	accountv1.RegisterSocialServer(grpcSrv, apiSrv)
	accountv1.RegisterSessionsServer(grpcSrv, apiSrv)
	accountv1.RegisterInvitationsServer(grpcSrv, apiSrv)
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("account", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcSrv, healthSrv)

	grpcListener, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcSrv.Serve(grpcListener) }()
	httpSrv := &http.Server{
		Addr:              cfg.HTTPListenAddr,
		Handler:           web.New(ident, m, os.Getenv("ACCOUNT_PUBLIC_BASE_URL"), os.Getenv("ACCOUNT_EMAIL_LINK_BASE")),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() { errCh <- httpSrv.ListenAndServe() }()

	log.Printf("account core listening: grpc=%s http=%s", cfg.GRPCListenAddr, cfg.HTTPListenAddr)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	stop()
	grpcSrv.GracefulStop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
