// Package observe carries a service's structured logger through a call's context, so what
// a handler logs names the call it belongs to without the handler passing anything along.
package observe

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// NewLogger writes JSON lines to w at level: "debug", "info", "warn" or "error".
func NewLogger(w io.Writer, level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("observe: log level %q: want debug, info, warn or error", level)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l})), nil
}

type loggerKey struct{}

func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// Log is the logger ctx carries, or slog.Default when it carries none.
func Log(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// With returns ctx whose logger adds args to every line.
func With(ctx context.Context, args ...any) context.Context {
	return WithLogger(ctx, Log(ctx).With(args...))
}
