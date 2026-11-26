package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

func NewLogger(w io.Writer, service string, level slog.Level, extra ...slog.Handler) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr})
	h = traceHandler{h}
	for _, e := range extra {
		h = fanout{h, redactHandler{e}}
	}
	return slog.New(h).With("service", service)
}

func levelFromEnv() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

// traceHandler adds the IDs of the current span to each record.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}
func (h traceHandler) WithGroup(n string) slog.Handler { return traceHandler{h.Handler.WithGroup(n)} }

type fanout struct{ a, b slog.Handler }

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return f.a.Enabled(ctx, l) || f.b.Enabled(ctx, l)
}
func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	if f.a.Enabled(ctx, r.Level) {
		_ = f.a.Handle(ctx, r.Clone())
	}
	if f.b.Enabled(ctx, r.Level) {
		return f.b.Handle(ctx, r)
	}
	return nil
}
func (f fanout) WithAttrs(as []slog.Attr) slog.Handler {
	return fanout{f.a.WithAttrs(as), f.b.WithAttrs(as)}
}
func (f fanout) WithGroup(n string) slog.Handler { return fanout{f.a.WithGroup(n), f.b.WithGroup(n)} }

// redactHandler applies redactAttr for handlers without ReplaceAttr.
type redactHandler struct{ slog.Handler }

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool { out.AddAttrs(redactAttr(nil, a)); return true })
	return h.Handler.Handle(ctx, out)
}
func (h redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	for i := range as {
		as[i] = redactAttr(nil, as[i])
	}
	return redactHandler{h.Handler.WithAttrs(as)}
}
func (h redactHandler) WithGroup(n string) slog.Handler { return redactHandler{h.Handler.WithGroup(n)} }

var sensitiveKeys = []string{"password", "secret", "token", "csrf", "cookie", "authorization", "session_id"}

var dsnPassword = regexp.MustCompile(`(postgres(?:ql)?://[^:/@\s]+:)[^@\s]+@`)

// redactAttr hides credentials, session identifiers and passwords in DSNs.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, s := range sensitiveKeys {
		if strings.Contains(key, s) {
			return slog.String(a.Key, "[REDACTED]")
		}
	}
	var v string
	switch a.Value.Kind() {
	case slog.KindString:
		v = a.Value.String()
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			v = err.Error()
			a = slog.String(a.Key, v)
		}
	}
	if strings.Contains(v, "://") {
		return slog.String(a.Key, dsnPassword.ReplaceAllString(v, "${1}[REDACTED]@"))
	}
	return a
}
