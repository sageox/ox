package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// DaemonTracer provides per-task trace creation for the daemon.
// Unlike the CLI (one root span per command), the daemon creates
// many independent traces — one per scheduled task execution.
//
// All methods are nil-safe: callers never need to check whether
// tracing is enabled.
type DaemonTracer struct {
	tracer trace.Tracer
}

// NewDaemonTracer returns a tracer for daemon tasks.
// Must be called after Init() has set up the TracerProvider.
// Returns nil if tracing is not enabled.
func NewDaemonTracer() *DaemonTracer {
	mu.RLock()
	t := tracer
	mu.RUnlock()
	if t == nil {
		return nil
	}
	return &DaemonTracer{tracer: t}
}

// StartTask creates a new root span for a daemon task.
// Each call produces an independent trace (no parent), so the daemon
// generates many short traces over its lifetime rather than one long one.
//
// Example:
//
//	ctx, span := dt.StartTask(ctx, "daemon:gc_check")
//	defer span.End()
func (dt *DaemonTracer) StartTask(ctx context.Context, taskName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if dt == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	opts := []trace.SpanStartOption{
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindInternal),
	}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return dt.tracer.Start(ctx, taskName, opts...)
}

// StartSubtask creates a child span under the current span in ctx.
func (dt *DaemonTracer) StartSubtask(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if dt == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	var opts []trace.SpanStartOption
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return dt.tracer.Start(ctx, name, opts...)
}
