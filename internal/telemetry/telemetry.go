// Package telemetry configures structured logging with mandatory redaction.
package telemetry

import (
	"context"
	"io"
	"log/slog"

	"example.com/iam-perf/internal/auth"
)

// NewLogger builds a slog.Logger whose output is scrubbed of token-shaped
// values regardless of level or format.
func NewLogger(level, format string, w io.Writer) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(&redactingHandler{inner: h})
}

// redactingHandler scrubs message text and string attribute values.
type redactingHandler struct{ inner slog.Handler }

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// slog.Record attrs are immutable in place; rebuild a scrubbed record.
	nr := slog.NewRecord(r.Time, r.Level, auth.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func scrubAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, auth.Redact(a.Value.String()))
	case slog.KindGroup:
		grp := a.Value.Group()
		out := make([]slog.Attr, len(grp))
		for i, sub := range grp {
			out[i] = scrubAttr(sub)
		}
		anyAttrs := make([]any, len(out))
		for i, sub := range out {
			anyAttrs[i] = sub
		}
		return slog.Group(a.Key, anyAttrs...)
	default:
		return a
	}
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		if s, ok := a.Value.Any().(string); ok {
			a.Value = slog.StringValue(auth.Redact(s))
		}
		scrubbed[i] = a
	}
	return &redactingHandler{inner: h.inner.WithAttrs(scrubbed)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}
