// Package grpclog writes one slog line per gRPC unary call: method, code and
// duration. It never logs requests, responses, metadata or error messages.
//
// The same file is copied into every OpenPak gRPC service; keep the copies identical.
package grpclog

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Constant polls (the adapters' event poll fires several times a second) are counted and
// summarised once a minute instead of logged per call; a failing poll still logs.
var (
	pollCount atomic.Int64
	pollOnce  sync.Once
)

func isPoll(method string) bool { return strings.HasSuffix(method, "/PollEvents") }

func summarise() {
	for range time.Tick(time.Minute) {
		if n := pollCount.Swap(0); n > 0 {
			slog.Info("grpc poll summary", "method", "PollEvents", "calls", n, "window_s", 60)
		}
	}
}

// Unary is a grpc.UnaryServerInterceptor; put it first in the chain.
func Unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if strings.HasPrefix(info.FullMethod, "/grpc.health.") {
		return h(ctx, req)
	}
	start := time.Now()
	resp, err := h(ctx, req)
	if err == nil && isPoll(info.FullMethod) {
		pollOnce.Do(func() { go summarise() })
		pollCount.Add(1)
		return resp, err
	}
	slog.Info("grpc", "method", info.FullMethod, "code", status.Code(err).String(),
		"ms", time.Since(start).Milliseconds())
	return resp, err
}
