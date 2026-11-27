package obs

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracer creates the spans of the platform.
var Tracer = otel.Tracer("dbmp")

func Init(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	res := resource.NewSchemaless(semconv.ServiceName(service))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	switch os.Getenv("OTEL_TRACES_EXPORTER") {
	case "otlp":
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	case "console":
		exp, err := stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	closers := []func(context.Context) error{tp.Shutdown}

	var extra []slog.Handler
	if os.Getenv("OTEL_LOGS_EXPORTER") == "otlp" {
		exp, err := otlploghttp.New(ctx)
		if err != nil {
			return nil, err
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
		extra = append(extra, otelslog.NewHandler("dbmp", otelslog.WithLoggerProvider(lp)))
		closers = append(closers, lp.Shutdown)
	}
	slog.SetDefault(NewLogger(os.Stdout, service, levelFromEnv(), extra...))

	return func(ctx context.Context) error {
		var errs []error
		for _, c := range closers {
			errs = append(errs, c(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

func Inject(ctx context.Context) map[string]string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c
}

func Extract(ctx context.Context, m map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(m))
}

func TraceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}

func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
