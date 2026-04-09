// Package observability provides OpenTelemetry tracing for the ox CLI.
//
// Creates one trace per CLI command invocation. All HTTP requests within a
// command share the same trace ID, appearing as children of the root span
// in Honeycomb. This gives CLI↔server timing visibility.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	mu       sync.RWMutex
	rootCtx  context.Context
	rootSpan trace.Span
	tracer   trace.Tracer
	shutFn   func(context.Context) error
)

// Init sets up the OTel TracerProvider with OTLP/HTTP export to the SageOx
// OTLP proxy at {apiEndpoint}/api/v1/otlp/v1/traces.
//
// Safe to call with empty apiEndpoint — tracing is disabled (noop).
func Init(ctx context.Context, serviceName, apiEndpoint string) error {
	if apiEndpoint == "" {
		slog.Debug("otel tracing disabled", "reason", "no endpoint")
		return nil
	}

	parsed, err := url.Parse(apiEndpoint)
	if err != nil || parsed.Host == "" {
		slog.Debug("otel tracing disabled", "reason", "invalid endpoint", "endpoint", apiEndpoint)
		return nil
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(parsed.Host),
		otlptracehttp.WithURLPath("/api/v1/otlp/v1/traces"),
		otlptracehttp.WithTimeout(2 * time.Second),
	}
	if parsed.Scheme == "http" {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("create OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(1*time.Second),
			sdktrace.WithMaxExportBatchSize(16),
		),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	mu.Lock()
	tracer = tp.Tracer(serviceName)
	shutFn = tp.Shutdown
	mu.Unlock()

	slog.Debug("otel tracing enabled", "endpoint", apiEndpoint, "service", serviceName)
	return nil
}

// StartCommand creates a root span for a CLI command invocation.
// All HTTP requests within this command will be children of this span.
func StartCommand(ctx context.Context, commandName string) (context.Context, trace.Span) {
	mu.RLock()
	t := tracer
	mu.RUnlock()

	if t == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	ctx, span := t.Start(ctx, commandName, trace.WithSpanKind(trace.SpanKindClient))

	mu.Lock()
	rootCtx = ctx
	rootSpan = span
	mu.Unlock()

	return ctx, span
}

// TraceParent returns the W3C traceparent header value from the current
// root span. Returns empty string if tracing is not active.
func TraceParent() string {
	mu.RLock()
	ctx := rootCtx
	mu.RUnlock()

	if ctx == nil {
		return ""
	}

	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}

	flags := "00"
	if sc.TraceFlags().IsSampled() {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), flags)
}

// SetCommandStatus records whether the command succeeded or failed on the
// root span. Call before Shutdown.
func SetCommandStatus(err error) {
	mu.RLock()
	span := rootSpan
	mu.RUnlock()

	if span == nil {
		return
	}

	if err != nil {
		span.RecordError(err)
	}
}

// Shutdown ends the root span and flushes pending exports.
// Blocks up to 3s for flush. Safe to call if Init was never called.
func Shutdown(ctx context.Context) {
	mu.Lock()
	span := rootSpan
	fn := shutFn
	rootSpan = nil
	rootCtx = nil
	mu.Unlock()

	if span != nil {
		span.End()
	}
	if fn != nil {
		flushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := fn(flushCtx); err != nil {
			slog.Debug("otel shutdown", "error", err)
		}
	}
}

// Enabled returns true if OTel tracing is initialized.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return tracer != nil
}

// CommandName extracts a readable command path for span naming.
// e.g., "ox login", "ox agent prime", "ox session stop"
func CommandName(parts ...string) string {
	return "ox " + strings.Join(parts, " ")
}
