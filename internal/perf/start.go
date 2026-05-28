package perf

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation library name used for spans created
// via perf.Start. Kept separate from the application service name so
// trace-backend dashboards can filter perf-instrumented spans without
// inspecting every service.
const tracerName = "github.com/sageox/ox/internal/perf"

// Start creates a child span under the active span in ctx. The returned
// context carries the new span; the returned trace.Span has the usual
// OTel API (.End, .SetAttributes, .RecordError, .SetStatus).
//
// Use perf.Start instead of observability.Tracer().Start so that:
//
//  1. Callers don't have to import both observability and otel/trace.
//  2. Failed spans can be marked via perf.RecordError without callers
//     pulling in the codes package.
//
// Safe to call when tracing is disabled: returns a no-op span whose End
// is a cheap no-op.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	t := otel.GetTracerProvider().Tracer(tracerName)
	var opts []trace.SpanStartOption
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return t.Start(ctx, name, opts...)
}

// RecordError marks span as failed and attaches err. Convenience over
// repeating two OTel calls at every error site. No-op when err is nil
// or span is nil.
func RecordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
