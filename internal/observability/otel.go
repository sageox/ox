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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TokenFunc returns the current bearer token to attach to OTLP requests.
// Called per-export so long-running processes (daemon) pick up rotated tokens.
// Returning "" means no bearer is available (logged out / token expired): the
// batch is dropped client-side (see bearerRoundTripper.RoundTrip) rather than
// sent, because the JWT-gated proxy would only 401 it anyway.
type TokenFunc func() string

type bearerRoundTripper struct {
	base      http.RoundTripper
	tokenFunc TokenFunc
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tok := rt.tokenFunc()
	if tok == "" {
		// No bearer available (logged out / token expired). The OTLP proxy is
		// JWT-gated, so actually sending would just 401 and churn the server's
		// error pipeline (the ox-w9yc5 surge). Drop client-side instead —
		// mirrors the browser SDK's 204 no-JWT behavior in
		// apps/web/src/lib/otel-browser.ts. A 2xx with an empty body makes the
		// batch exporter treat the batch as delivered, so it neither retries
		// nor logs an export error.
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Proto:      req.Proto,
			ProtoMajor: req.ProtoMajor,
			ProtoMinor: req.ProtoMinor,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	}
	// clone to avoid mutating the caller's request (otlptracehttp may retry)
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+tok)
	return rt.base.RoundTrip(req)
}

var (
	mu              sync.RWMutex
	rootCtx         context.Context
	rootSpan        trace.Span
	tracer          trace.Tracer
	shutFn          func(context.Context) error
	extraProcessors []sdktrace.SpanProcessor
)

// AddSpanProcessor registers an additional SpanProcessor to be installed
// alongside the OTLP batch exporter on the next Init() call. Must be
// called BEFORE Init — processors added after Init are ignored, because
// the TracerProvider is already built.
//
// Used by callers (CLI bootstrap, daemon bootstrap) to wire in
// internal/perf's TreeCollectorProcessor so per-phase timing renders
// locally in addition to flowing to the OTLP backend.
//
// Safe to call multiple times; each processor is registered once.
func AddSpanProcessor(p sdktrace.SpanProcessor) {
	if p == nil {
		return
	}
	mu.Lock()
	extraProcessors = append(extraProcessors, p)
	mu.Unlock()
}

// Init sets up the OTel TracerProvider with OTLP/HTTP export to the SageOx
// OTLP proxy at {apiEndpoint}/api/v1/otlp/v1/traces.
//
// The proxy is JWT-gated (see apps/api-go/internal/handlers/otlp_proxy.go),
// so tokenFunc is required for exports to succeed. tokenFunc is invoked
// per-request rather than baked into a static header so long-running
// processes (the daemon) pick up rotated tokens without restart.
//
// Extra attrs are merged into the OTel resource alongside service.name.
// The daemon uses this to set client.id, client.class, os.type, etc.
//
// Safe to call with empty apiEndpoint or nil tokenFunc — tracing is
// disabled (noop). When tokenFunc returns "" at export time (logged-out /
// expired), the batch is dropped client-side (synthetic 2xx, nothing sent)
// rather than 401'd by the server. Either is fine for tests and for users
// who are logged out.
func Init(ctx context.Context, serviceName, apiEndpoint string, tokenFunc TokenFunc, attrs ...attribute.KeyValue) error {
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
	if tokenFunc != nil {
		opts = append(opts, otlptracehttp.WithHTTPClient(&http.Client{
			Timeout:   2 * time.Second,
			Transport: &bearerRoundTripper{base: http.DefaultTransport, tokenFunc: tokenFunc},
		}))
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("create OTLP exporter: %w", err)
	}

	resAttrs := []attribute.KeyValue{semconv.ServiceName(serviceName)}
	resAttrs = append(resAttrs, attrs...)

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(1*time.Second),
			sdktrace.WithMaxExportBatchSize(16),
		),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			resAttrs...,
		)),
	}
	// Append any processors registered via AddSpanProcessor before Init.
	// internal/perf uses this hook to install its TreeCollectorProcessor
	// so the local tree renderer sees the same spans the OTLP exporter
	// receives, without duplicate instrumentation.
	mu.RLock()
	for _, p := range extraProcessors {
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(p))
	}
	mu.RUnlock()

	tp := sdktrace.NewTracerProvider(tpOpts...)
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
