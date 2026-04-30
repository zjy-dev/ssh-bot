// Package log provides a slog-backed logger with trace_id propagation through
// context.Context. See plan §11 / FR-071.
package log

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
)

type ctxKey struct{}

var traceKey = ctxKey{}

// NewLogger returns a slog.Logger that writes JSON to w at the given level.
// Pass nil w to use os.Stderr.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// NewTraceID returns a fresh 16-hex-char trace id.
func NewTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WithTrace stamps a trace id onto the context.
func WithTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey, id)
}

// TraceID returns the trace id previously stored with WithTrace, or "" if none.
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceKey).(string); ok {
		return v
	}
	return ""
}

// FromContext returns a logger pre-tagged with trace_id from ctx (if any).
// base may be nil, in which case slog.Default() is used.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	if id := TraceID(ctx); id != "" {
		return base.With("trace_id", id)
	}
	return base
}

// ParseLevel maps common level names to slog.Level. Unknown → info.
func ParseLevel(s string) slog.Level {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "warning", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
