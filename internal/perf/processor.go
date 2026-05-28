// Package perf collects OpenTelemetry spans into an in-process tree so
// `ox` commands and the daemon can render per-phase timing to a local sink
// (stderr, daemon log) without needing the OTLP backend.
//
// The package wraps OTel — it does NOT replace it. Spans produced via
// perf.Start are real OTel spans that still flow to the configured
// exporter. perf adds a SpanProcessor that buffers each trace's spans
// in memory and, on root-span end, hands the assembled tree to a sink.
package perf

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Node is a span captured by the processor with its children attached.
// Trees are rooted at the trace's root span (no valid parent SpanID).
type Node struct {
	Name     string
	SpanID   trace.SpanID
	Start    time.Time
	End      time.Time
	Duration time.Duration
	Status   sdktrace.Status
	Attrs    []attribute.KeyValue
	Children []*Node

	// Detached is true when the span's parent ended before it did and the
	// parent's tree was already emitted, or when its parent SpanID was not
	// present in the trace's pending set. Detached spans render under a
	// synthetic "<detached>" node so the user sees the timing rather than
	// silently dropping it.
	Detached bool
}

// SpanSink is called for every closed span (one call per OnEnd). Used to
// emit per-span slog records when OX_TRACE=1 or duration exceeds a
// threshold. Nil is a no-op.
type SpanSink func(s sdktrace.ReadOnlySpan)

// TreeSink is called once per closed trace, with the assembled root Node.
// Used to render the tree to stderr or to the daemon log. Nil is a no-op.
type TreeSink func(root *Node)

// Options configures a TreeCollectorProcessor.
type Options struct {
	OnSpan SpanSink
	OnTree TreeSink
}

// TreeCollectorProcessor is an OTel sdktrace.SpanProcessor that buffers
// spans per trace and, when each trace's root span ends, builds a Node
// tree and calls Options.OnTree with it.
//
// Concurrency: the OTel SDK may call OnEnd from multiple goroutines for
// spans in the same or different traces. All map mutations are guarded
// by mu.
type TreeCollectorProcessor struct {
	opts Options

	mu      sync.Mutex
	pending map[trace.TraceID][]sdktrace.ReadOnlySpan
	emitted map[trace.TraceID]time.Time
}

// NewTreeProcessor returns a processor ready to register with an OTel
// TracerProvider via sdktrace.WithSpanProcessor.
func NewTreeProcessor(opts Options) *TreeCollectorProcessor {
	return &TreeCollectorProcessor{
		opts:    opts,
		pending: make(map[trace.TraceID][]sdktrace.ReadOnlySpan),
		emitted: make(map[trace.TraceID]time.Time),
	}
}

// emittedRetention keeps a short-lived tombstone for completed traces so
// child spans that end just after the root are dropped instead of creating
// an un-emittable pending entry. Root traces are pruned opportunistically
// on the next completed trace so long-lived daemons don't retain one
// TraceID forever per sync cycle.
const emittedRetention = 2 * time.Minute

// OnStart is part of the SpanProcessor interface. We don't need it.
func (p *TreeCollectorProcessor) OnStart(_ context.Context, _ sdktrace.ReadWriteSpan) {}

// OnEnd buffers the span. If the span is the root of its trace, we
// assemble the tree and hand it to OnTree.
func (p *TreeCollectorProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if s == nil {
		return
	}
	if !s.SpanContext().IsSampled() {
		// Honors future sampling without rendering empty trees.
		return
	}
	if p.opts.OnSpan != nil {
		p.opts.OnSpan(s)
	}

	tid := s.SpanContext().TraceID()
	isRoot := !s.Parent().SpanID().IsValid()
	now := time.Now()

	p.mu.Lock()
	if isRoot {
		p.pruneExpiredEmittedLocked(now)
	}
	if expiresAt, done := p.emitted[tid]; done {
		if expiresAt.After(now) {
			// Late arrival after the root already emitted. Drop quietly —
			// the tree is already gone. A per-span slog (above) still ran.
			p.mu.Unlock()
			return
		}
		delete(p.emitted, tid)
	}
	if p.pending == nil || p.emitted == nil {
		p.mu.Unlock()
		return
	}
	p.pending[tid] = append(p.pending[tid], s)
	if !isRoot {
		p.mu.Unlock()
		return
	}
	spans := p.pending[tid]
	delete(p.pending, tid)
	p.emitted[tid] = now.Add(emittedRetention)
	p.mu.Unlock()

	if p.opts.OnTree == nil {
		return
	}
	tree := buildTree(spans, s.SpanContext().SpanID())
	if tree != nil {
		p.opts.OnTree(tree)
	}
}

// Shutdown drops any pending spans. No-op flush — the underlying
// exporter handles real flushing.
func (p *TreeCollectorProcessor) Shutdown(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = nil
	p.emitted = nil
	return nil
}

func (p *TreeCollectorProcessor) pruneExpiredEmittedLocked(now time.Time) {
	for tid, expiresAt := range p.emitted {
		if !expiresAt.After(now) {
			delete(p.emitted, tid)
		}
	}
}

// ForceFlush is a no-op. Trees emit synchronously on root OnEnd.
func (p *TreeCollectorProcessor) ForceFlush(_ context.Context) error { return nil }

// buildTree assembles a Node tree from a flat slice of spans, rooted at
// rootID. Spans whose parent is not in the slice are attached under a
// synthetic <detached> node so the user sees their timing rather than
// silently losing it.
func buildTree(spans []sdktrace.ReadOnlySpan, rootID trace.SpanID) *Node {
	byID := make(map[trace.SpanID]*Node, len(spans))
	for _, s := range spans {
		n := &Node{
			Name:     s.Name(),
			SpanID:   s.SpanContext().SpanID(),
			Start:    s.StartTime(),
			End:      s.EndTime(),
			Duration: s.EndTime().Sub(s.StartTime()),
			Status:   s.Status(),
			Attrs:    s.Attributes(),
		}
		byID[n.SpanID] = n
	}
	root, ok := byID[rootID]
	if !ok {
		return nil
	}

	var detached []*Node
	for _, s := range spans {
		sid := s.SpanContext().SpanID()
		if sid == rootID {
			continue
		}
		parent := s.Parent().SpanID()
		n := byID[sid]
		if pn, ok := byID[parent]; ok {
			pn.Children = append(pn.Children, n)
			continue
		}
		n.Detached = true
		detached = append(detached, n)
	}

	if len(detached) > 0 {
		root.Children = append(root.Children, &Node{
			Name:     "<detached>",
			Children: detached,
			Detached: true,
		})
	}

	sortByStart(root)
	return root
}

func sortByStart(n *Node) {
	if n == nil {
		return
	}
	sort.SliceStable(n.Children, func(i, j int) bool {
		return n.Children[i].Start.Before(n.Children[j].Start)
	})
	for _, c := range n.Children {
		sortByStart(c)
	}
}
