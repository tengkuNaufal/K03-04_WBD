package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type QueryTracer struct {
	Pool      string     
	LogParams bool      
	Level     slog.Level 
}

type queryStart struct {
	at     time.Time
	sql    string
	params []any
	span   trace.Span
}

type queryKey struct{}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	qs := &queryStart{at: time.Now(), sql: d.SQL, params: d.Args}
	if trace.SpanFromContext(ctx).SpanContext().IsValid() {
		ctx, qs.span = Tracer.Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("db.system", "postgresql"), attribute.String("db.pool", t.Pool),
			attribute.String("db.statement", truncate(d.SQL, 1000))))
	}
	return context.WithValue(ctx, queryKey{}, qs)
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	qs, ok := ctx.Value(queryKey{}).(*queryStart)
	if !ok {
		return
	}
	dur := time.Since(qs.at)
	attrs := []any{"event", "sql.query", "pool", t.Pool, "query", truncate(qs.sql, 2000),
		"duration_ms", float64(dur.Microseconds()) / 1000, "rows", d.CommandTag.RowsAffected()}
	if t.LogParams {
		attrs = append(attrs, "params", formatParams(qs.params))
	} else {
		attrs = append(attrs, "params", fmt.Sprintf("[%d redacted]", len(qs.params)))
	}
	level, status := t.Level, "ok"
	if d.Err != nil {
		level, status = slog.LevelWarn, "error"
		attrs = append(attrs, "error", d.Err.Error())
		var pgErr *pgconn.PgError
		if errors.As(d.Err, &pgErr) {
			attrs = append(attrs, "sqlstate", pgErr.Code)
		}
	}
	attrs = append(attrs, "status", status)
	slog.Log(ctx, level, "sql "+status, attrs...)
	if qs.span != nil {
		qs.span.SetAttributes(attribute.Int64("db.rows_affected", d.CommandTag.RowsAffected()))
		End(qs.span, d.Err)
	}
}

func formatParams(ps []any) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = truncate(fmt.Sprint(p), 200)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
