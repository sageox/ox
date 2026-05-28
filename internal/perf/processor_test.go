package perf

import (
	"context"
	cryptorand "crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// --- A. Single root span emits one tree ---

// TestProcessor_RootOnlyEmitsTree verifies a tree with just a root span
// is emitted as a single-node tree.
// Failure prevented: silently dropping spans with no children.
func TestProcessor_RootOnlyEmitsTree(t *testing.T) {
	var got *Node
	tp := setupTP(t, Options{OnTree: func(n *Node) { got = n }})
	defer func() { _ = tp.Shutdown(context.Background()) }()

	_, span := tp.Tracer("test").Start(context.Background(), "root")
	span.End()

	require.NotNil(t, got)
	assert.Equal(t, "root", got.Name)
	assert.Empty(t, got.Children)
}

// --- B. Nested spans nest correctly ---

// TestProcessor_NestedSpansForm Tree verifies parent/child nesting via
// OTel context inheritance produces the right Children structure.
// Failure prevented: child spans rendered as orphans or as siblings.
func TestProcessor_NestedSpansFormTree(t *testing.T) {
	var got *Node
	tp := setupTP(t, Options{OnTree: func(n *Node) { got = n }})
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	ctx, root := tr.Start(context.Background(), "root")
	_, child := tr.Start(ctx, "child")
	child.End()
	root.End()

	require.NotNil(t, got)
	require.Len(t, got.Children, 1)
	assert.Equal(t, "child", got.Children[0].Name)
}

// --- C. Parallel siblings nest under parent ---

// TestProcessor_ParallelSiblings verifies concurrent child spans (from a
// WaitGroup, like daemon team-context syncs) all attach to the parent.
// Failure prevented: race in pending map; siblings landing on wrong trace.
func TestProcessor_ParallelSiblings(t *testing.T) {
	var got *Node
	tp := setupTP(t, Options{OnTree: func(n *Node) { got = n }})
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	ctx, root := tr.Start(context.Background(), "root")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c := tr.Start(ctx, "sibling")
			time.Sleep(time.Millisecond)
			c.End()
		}()
	}
	wg.Wait()
	root.End()

	require.NotNil(t, got)
	assert.Len(t, got.Children, 5, "all parallel children must nest under root")
}

// --- D. Detached span renders under <detached> ---

// TestProcessor_OrphanSpanRendersUnderDetached verifies that a span whose
// parent SpanID is NOT present in the trace's pending set (e.g. because
// the parent was unsampled, lived in a different process, or arrived
// after the trace was emitted and got dropped) surfaces under a
// synthetic "<detached>" node rather than vanishing.
//
// We test buildTree directly with synthetic tracetest.SpanStub values so
// we can construct the exact "parent SpanID has no matching span"
// scenario, which is hard to provoke through the live SDK because every
// span created via Tracer.Start has a real parent context.
//
// Failure prevented: silent timing loss for spans whose parent missed
// emission.
func TestProcessor_OrphanSpanRendersUnderDetached(t *testing.T) {
	traceID := randomTraceID(t)
	rootID := randomSpanID(t)
	orphanID := randomSpanID(t)
	missingParentID := randomSpanID(t)

	start := time.Now().Add(-time.Second)
	end := start.Add(time.Second)

	rootSpan := tracetest.SpanStub{
		Name: "root",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  rootID,
		}),
		// Parent left zero → buildTree treats this as root.
		StartTime: start,
		EndTime:   end,
	}.Snapshot()

	orphanSpan := tracetest.SpanStub{
		Name: "orphan",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  orphanID,
		}),
		// Parent SpanID points to a span we deliberately omit from the
		// slice — this is what triggers the <detached> branch.
		Parent: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  missingParentID,
		}),
		StartTime: start.Add(100 * time.Millisecond),
		EndTime:   end.Add(-100 * time.Millisecond),
	}.Snapshot()

	tree := buildTree([]sdktrace.ReadOnlySpan{rootSpan, orphanSpan}, rootID)

	require.NotNil(t, tree)
	require.Len(t, tree.Children, 1, "orphan must surface as a child")
	detached := tree.Children[0]
	assert.Equal(t, "<detached>", detached.Name)
	assert.True(t, detached.Detached)
	require.Len(t, detached.Children, 1, "orphan span must be under <detached>")
	assert.Equal(t, "orphan", detached.Children[0].Name)
	assert.True(t, detached.Children[0].Detached, "orphan node should be flagged Detached")
}

func randomTraceID(t *testing.T) trace.TraceID {
	t.Helper()
	var b [16]byte
	_, err := cryptorand.Read(b[:])
	require.NoError(t, err)
	return b
}

func randomSpanID(t *testing.T) trace.SpanID {
	t.Helper()
	var b [8]byte
	_, err := cryptorand.Read(b[:])
	require.NoError(t, err)
	return b
}

// --- E. Error status surfaces on Node ---

// TestProcessor_ErrorStatusSurfaces verifies SetStatus(Error) on a span
// is captured on the corresponding Node so the renderer can flag it.
// Failure prevented: failed phases looking identical to successful ones.
func TestProcessor_ErrorStatusSurfaces(t *testing.T) {
	var got *Node
	tp := setupTP(t, Options{OnTree: func(n *Node) { got = n }})
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	ctx, root := tr.Start(context.Background(), "root")
	_, child := tr.Start(ctx, "child")
	child.SetStatus(codes.Error, "boom")
	child.End()
	root.End()

	require.NotNil(t, got)
	require.Len(t, got.Children, 1)
	assert.Equal(t, codes.Error, got.Children[0].Status.Code)
}

// --- F. PerSpan sink called for every span end ---

// TestProcessor_PerSpanSinkCallback verifies OnSpan fires for every
// closed span, not just the root.
// Failure prevented: per-span slog emission missing for non-root spans.
func TestProcessor_PerSpanSinkCallback(t *testing.T) {
	var spans []string
	var mu sync.Mutex
	tp := setupTP(t, Options{
		OnSpan: func(s sdktrace.ReadOnlySpan) {
			mu.Lock()
			spans = append(spans, s.Name())
			mu.Unlock()
		},
	})
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	ctx, root := tr.Start(context.Background(), "root")
	_, c := tr.Start(ctx, "child")
	c.End()
	root.End()

	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"child", "root"}, spans)
}

// --- G. Independent traces don't cross-contaminate ---

// TestProcessor_IndependentTraces verifies spans from two unrelated
// traces don't end up in the same tree. Daemon spawns one trace per
// task; cross-contamination would conflate timing.
func TestProcessor_IndependentTraces(t *testing.T) {
	var trees []*Node
	var mu sync.Mutex
	tp := setupTP(t, Options{
		OnTree: func(n *Node) {
			mu.Lock()
			trees = append(trees, n)
			mu.Unlock()
		},
	})
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	// Two separate root spans = two independent traces.
	_, a := tr.Start(context.Background(), "task_a", trace.WithNewRoot())
	a.End()
	_, b := tr.Start(context.Background(), "task_b", trace.WithNewRoot())
	b.End()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, trees, 2)
}

// --- H. Late child spans are dropped without reintroducing pending state ---

// TestProcessor_LateChildDoesNotRequeue verifies the short-lived emitted
// tombstone still suppresses child spans that end after the root emitted.
// Failure prevented: late child spans recreating a pending entry that can
// never be flushed because the root is already gone.
func TestProcessor_LateChildDoesNotRequeue(t *testing.T) {
	p := NewTreeProcessor(Options{})
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(p),
	)
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	ctx, root := tr.Start(context.Background(), "root", trace.WithNewRoot())
	_, child := tr.Start(ctx, "child")
	tid := root.SpanContext().TraceID()

	root.End()
	child.End()

	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Empty(t, p.pending)
	_, ok := p.emitted[tid]
	assert.True(t, ok, "completed trace should retain a short-lived tombstone")
}

// --- I. Expired tombstones are pruned on the next completed trace ---

// TestProcessor_PrunesExpiredEmitted verifies completed trace tombstones do
// not accumulate forever in long-lived processes like the daemon.
// Failure prevented: one TraceID leaked per completed background task.
func TestProcessor_PrunesExpiredEmitted(t *testing.T) {
	p := NewTreeProcessor(Options{})
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(p),
	)
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	_, oldRoot := tr.Start(context.Background(), "old", trace.WithNewRoot())
	oldTID := oldRoot.SpanContext().TraceID()
	oldRoot.End()

	p.mu.Lock()
	p.emitted[oldTID] = time.Now().Add(-time.Second)
	p.mu.Unlock()

	_, newRoot := tr.Start(context.Background(), "new", trace.WithNewRoot())
	newTID := newRoot.SpanContext().TraceID()
	newRoot.End()

	p.mu.Lock()
	defer p.mu.Unlock()
	_, oldExists := p.emitted[oldTID]
	_, newExists := p.emitted[newTID]
	assert.False(t, oldExists, "expired tombstone should be pruned on the next root span")
	assert.True(t, newExists, "current root should still install its tombstone")
}

// setupTP builds a TracerProvider with our processor + an in-memory
// exporter installed. The exporter is required for span sampling/export
// semantics to behave like production.
func setupTP(t *testing.T, opts Options) *sdktrace.TracerProvider {
	t.Helper()
	p := NewTreeProcessor(opts)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(p),
	)
	otel.SetTracerProvider(tp)
	return tp
}
