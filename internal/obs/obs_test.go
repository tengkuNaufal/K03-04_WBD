package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestLoggerRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "test", slog.LevelInfo)
	log.Info("x", "event", "test", "password", "hunter2", "csrf_token", "abc", "session_id", "s3cr3t",
		"dsn", "postgres://dbmp:topsecret@db:5432/dbmp", "error", errors.New("dial postgres://u:pw123@h/db failed"))
	out := buf.String()
	for _, secret := range []string{"hunter2", "abc", "s3cr3t", "topsecret", "pw123"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q in log: %s", secret, out)
		}
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, k := range []string{"time", "level", "msg", "service", "event"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}

func TestLoggerAddsTraceIDs(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()
	var buf bytes.Buffer
	NewLogger(&buf, "test", slog.LevelInfo).InfoContext(ctx, "x")
	var rec map[string]string
	_ = json.Unmarshal(buf.Bytes(), &rec)
	if rec["trace_id"] != span.SpanContext().TraceID().String() || rec["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("trace ids: %v", rec)
	}
}

func TestTraceContextSurvivesTheQueue(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "publish")
	defer span.End()
	if _, err := Init(context.Background(), "test"); err != nil { // installs the W3C propagator
		t.Fatal(err)
	}
	carrier := Inject(ctx)
	if !strings.Contains(carrier["traceparent"], span.SpanContext().TraceID().String()) {
		t.Fatalf("carrier: %v", carrier)
	}
	if TraceID(Extract(context.Background(), carrier)) != span.SpanContext().TraceID().String() {
		t.Fatal("trace id lost through the queue")
	}
}
