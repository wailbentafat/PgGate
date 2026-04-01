package logging

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
)

type contextKey string

const requestIDKey contextKey = "request_id"

var globalLogger atomic.Pointer[slog.Logger]

func init() {
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	globalLogger.Store(l)
}

func Setup(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	l := slog.New(handler)
	globalLogger.Store(l)
	slog.SetDefault(l)
}

func L() *slog.Logger {
	return globalLogger.Load()
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func FromContext(ctx context.Context) *slog.Logger {
	l := L()
	if rid := RequestID(ctx); rid != "" {
		l = l.With("request_id", rid)
	}
	return l
}
