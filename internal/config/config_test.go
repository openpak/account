package config

import (
	"errors"
	"testing"
)

func TestLoadFailsClosed(t *testing.T) {
	t.Setenv("ACCOUNT_DATABASE_URL", "")
	t.Setenv("ACCOUNT_SESSION_SECRET", "")
	if _, err := Load(); !errors.Is(err, ErrMissingConfig) {
		t.Fatalf("expected ErrMissingConfig, got %v", err)
	}
	t.Setenv("ACCOUNT_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("ACCOUNT_SESSION_SECRET", "short")
	if _, err := Load(); err == nil {
		t.Fatal("short session secret must fail startup")
	}
	t.Setenv("ACCOUNT_SESSION_SECRET", "0123456789abcdef0123456789abcdef")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.GRPCListenAddr != ":7000" || cfg.HTTPListenAddr != ":8000" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}
