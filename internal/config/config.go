// Package config loads service configuration. Missing required values fail
// startup (fail-closed), per PRD §8.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL    string // required
	GRPCListenAddr string // internal API
	HTTPListenAddr string // web/API
	SessionSecret  string // required; pepper for token hashing
	SessionTTL     string
	Environment    string // "development" | "production"
	AllowedOrigins []string
}

func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:    os.Getenv("ACCOUNT_DATABASE_URL"),
		SessionSecret:  os.Getenv("ACCOUNT_SESSION_SECRET"),
		GRPCListenAddr: envOr("ACCOUNT_GRPC_ADDR", ":7000"),
		HTTPListenAddr: envOr("ACCOUNT_HTTP_ADDR", ":8000"),
		SessionTTL:     envOr("ACCOUNT_SESSION_TTL", "720h"),
		Environment:    envOr("ACCOUNT_ENVIRONMENT", "production"),
	}
	if origins := os.Getenv("ACCOUNT_ALLOWED_ORIGINS"); origins != "" {
		c.AllowedOrigins = strings.Split(origins, ",")
	}
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "ACCOUNT_DATABASE_URL")
	}
	if c.SessionSecret == "" {
		missing = append(missing, "ACCOUNT_SESSION_SECRET")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingConfig, strings.Join(missing, ", "))
	}
	if len(c.SessionSecret) < 32 {
		return nil, errors.New("ACCOUNT_SESSION_SECRET must be at least 32 bytes")
	}
	return c, nil
}

var ErrMissingConfig = errors.New("missing required configuration")

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
