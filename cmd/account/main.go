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
	"openpak/account/internal/links"
	"openpak/account/internal/mailer"
	"openpak/account/internal/storage"
	"openpak/account/internal/web"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

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

	// gRPC internal API.
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
	}
	internalKey := os.Getenv("ACCOUNT_INTERNAL_KEY")
	if len(internalKey) < 32 {
		return errors.New("ACCOUNT_INTERNAL_KEY must be set (>=32 bytes); internal RPCs fail closed")
	}
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcapi.Interceptor(internalKey)))
	apiSrv := grpcapi.New(ident, lnks, evSvc, grpcapi.Options{Mailer: m, BaseURL: os.Getenv("ACCOUNT_PUBLIC_BASE_URL")})
	accountv1.RegisterIdentityServer(grpcSrv, apiSrv)
	accountv1.RegisterLinksServer(grpcSrv, apiSrv)
	accountv1.RegisterEventsServer(grpcSrv, apiSrv)
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
		Handler:           web.New(ident),
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
