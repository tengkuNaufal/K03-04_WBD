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

func NewLogger(w io.Writer, service string, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr})
	return slog.New(traceHandler{h}).With("service", service)
}

func levelFromEnv() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

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

var sensitiveKeys = []string{"password", "secret", "token", "csrf", "cookie", "authorization", "session_id"}

var dsnPassword = regexp.MustCompile(`(postgres(?:ql)?://[^:/@\s]+:)[^@\s]+@`)

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
