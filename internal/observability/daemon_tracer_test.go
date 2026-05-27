package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// --- A. Nil safety ---

// TestDaemonTracer_NilStartTask verifies StartTask is safe on nil receiver.
// Failure prevented: NPE when tracing is disabled and daemon code calls StartTask.
func TestDaemonTracer_NilStartTask(t *testing.T) {
	var dt *DaemonTracer
	ctx, span := dt.StartTask(context.Background(), "daemon:test")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	assert.False(t, span.SpanContext().IsValid(), "nil tracer should return noop span")
	span.End() // must not panic
}

// TestDaemonTracer_NilStartSubtask verifies StartSubtask is safe on nil receiver.
// Failure prevented: NPE when tracing disabled and daemon calls StartSubtask.
func TestDaemonTracer_NilStartSubtask(t *testing.T) {
	var dt *DaemonTracer
	ctx, span := dt.StartSubtask(context.Background(), "daemon:sub")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	span.End()
}

// --- B. NewDaemonTracer without Init ---

// TestNewDaemonTracer_NoInit returns nil when tracing not initialized.
// Failure prevented: non-nil tracer with broken exporter.
func TestNewDaemonTracer_NoInit(t *testing.T) {
	resetGlobals()
	dt := NewDaemonTracer()
	assert.Nil(t, dt)
}

// --- C. NewDaemonTracer with Init ---

// TestNewDaemonTracer_WithInit returns a usable tracer after Init.
func TestNewDaemonTracer_WithInit(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "ox-daemon-test", "http://192.0.2.1:4318", nil)
	require.NoError(t, err)
	t.Cleanup(func() { Shutdown(context.Background()); resetGlobals() })

	dt := NewDaemonTracer()
	require.NotNil(t, dt)
}

// --- D. StartTask creates independent root traces ---

// TestDaemonTracer_StartTask_IndependentTraces verifies each StartTask produces
// a new trace ID (no parent relationship).
// Failure prevented: all daemon tasks sharing one trace, defeating per-task visibility.
func TestDaemonTracer_StartTask_IndependentTraces(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "ox-daemon-test", "http://192.0.2.1:4318", nil)
	require.NoError(t, err)
	t.Cleanup(func() { Shutdown(context.Background()); resetGlobals() })

	dt := NewDaemonTracer()
	require.NotNil(t, dt)

	ctx1, span1 := dt.StartTask(context.Background(), "daemon:task_a")
	defer span1.End()
	ctx2, span2 := dt.StartTask(context.Background(), "daemon:task_b")
	defer span2.End()

	sc1 := trace.SpanFromContext(ctx1).SpanContext()
	sc2 := trace.SpanFromContext(ctx2).SpanContext()

	assert.True(t, sc1.IsValid())
	assert.True(t, sc2.IsValid())
	assert.NotEqual(t, sc1.TraceID(), sc2.TraceID(), "each task must have a unique trace ID")
}

// --- E. StartSubtask creates child within same trace ---

// TestDaemonTracer_StartSubtask_ChildOfTask verifies subtask shares parent trace ID.
// Failure prevented: subtask spans disconnected from parent task trace.
func TestDaemonTracer_StartSubtask_ChildOfTask(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "ox-daemon-test", "http://192.0.2.1:4318", nil)
	require.NoError(t, err)
	t.Cleanup(func() { Shutdown(context.Background()); resetGlobals() })

	dt := NewDaemonTracer()
	require.NotNil(t, dt)

	taskCtx, taskSpan := dt.StartTask(context.Background(), "daemon:parent")
	defer taskSpan.End()

	childCtx, childSpan := dt.StartSubtask(taskCtx, "daemon:child")
	defer childSpan.End()

	taskSC := trace.SpanFromContext(taskCtx).SpanContext()
	childSC := trace.SpanFromContext(childCtx).SpanContext()

	assert.Equal(t, taskSC.TraceID(), childSC.TraceID(), "child must share parent trace ID")
	assert.NotEqual(t, taskSC.SpanID(), childSC.SpanID(), "child must have its own span ID")
}

// --- F. StartTask with attributes ---

// TestDaemonTracer_StartTask_WithAttrs verifies attributes are accepted without error.
// Failure prevented: panic or ignored attributes on task spans.
func TestDaemonTracer_StartTask_WithAttrs(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "ox-daemon-test", "http://192.0.2.1:4318", nil)
	require.NoError(t, err)
	t.Cleanup(func() { Shutdown(context.Background()); resetGlobals() })

	dt := NewDaemonTracer()
	require.NotNil(t, dt)

	ctx, span := dt.StartTask(context.Background(), "daemon:with_attrs",
		attribute.String("workspace.type", "ledger"),
		attribute.Int("workspace.count", 3),
	)
	defer span.End()

	assert.True(t, trace.SpanFromContext(ctx).SpanContext().IsValid())
}

// --- G. Init with extra resource attributes ---

// TestInit_ExtraAttrs verifies Init accepts additional resource attributes.
// Failure prevented: daemon can't set client.id, client.class, etc.
func TestInit_ExtraAttrs(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "ox-daemon-test", "http://192.0.2.1:4318", nil,
		attribute.String("client.id", "test-id"),
		attribute.String("client.class", "daemon"),
	)
	require.NoError(t, err)
	assert.True(t, Enabled())
	t.Cleanup(func() { Shutdown(context.Background()); resetGlobals() })
}
