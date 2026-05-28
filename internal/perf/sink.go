package perf

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SlowThreshold is the root-trace duration at or above which the tree
// is rendered to stderr even when OX_TRACE is unset. Tuned for "user
// notices ox felt slow."
const SlowThreshold = 3 * time.Second

// PerSpanSlogThreshold is the per-span duration at or above which a
// single-line slog record is emitted for that span when OX_TRACE is
// unset. Below this, only OX_TRACE=1 forces emission.
const PerSpanSlogThreshold = 100 * time.Millisecond

// TraceEnabled returns true if OX_TRACE is set to a truthy value. Used
// by sinks to gate full-detail rendering and per-span slog emission.
func TraceEnabled() bool {
	v := os.Getenv("OX_TRACE")
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// CLITreeSink renders the root tree to w when:
//
//   - OX_TRACE is truthy, OR
//   - verbose is true (-v / --verbose), OR
//   - the root trace took at least SlowThreshold (auto-surface).
//
// In the auto-surface case, a footer hint about OX_TRACE is appended so
// users discover the env var the next time they want detail.
//
// w is typically os.Stderr; pass a buffer in tests.
func CLITreeSink(w io.Writer, verbose bool) TreeSink {
	trace := TraceEnabled()
	return func(root *Node) {
		if root == nil {
			return
		}
		explicit := trace || verbose
		autoSurface := !explicit && root.Duration >= SlowThreshold
		if !explicit && !autoSurface {
			return
		}
		RenderTree(w, root)
		if autoSurface {
			// Keep the footer ASCII-only so it renders correctly under
			// NO_COLOR and on Windows consoles.
			_, _ = io.WriteString(w, "(set OX_TRACE=1 to log per-phase timing)\n")
		}
	}
}

// DaemonTreeSink renders the root tree to the daemon logger at debug
// level. Tree blocks land in the daemon log only when the operator
// opts in via OX_TRACE=1 (env var inherited by the daemon child
// process) — otherwise per-span slog records (from PerSpanSlog below)
// are sufficient for grep + jq workflows without inflating the log.
func DaemonTreeSink(logger *slog.Logger) TreeSink {
	return func(root *Node) {
		if root == nil || logger == nil {
			return
		}
		if !TraceEnabled() {
			return
		}
		var b strings.Builder
		RenderTree(&b, root)
		logger.Debug("trace_tree", "root", root.Name, "duration_ms", root.Duration.Milliseconds(), "tree", b.String())
	}
}

// PerSpanSlog returns a SpanSink that emits one slog record per closed
// span when either:
//
//   - OX_TRACE is truthy, OR
//   - the span's duration meets PerSpanSlogThreshold.
//
// Without this gate a 5-second daemon sync loop would produce thousands
// of info-level lines per hour from no-op pull dedup spans, which would
// dominate `ox daemon logs` and worsen disk pressure.
//
// The emitted record is single-line key=value (project convention) so
// it stays greppable and aggregator-friendly.
func PerSpanSlog(logger *slog.Logger) SpanSink {
	if logger == nil {
		logger = slog.Default()
	}
	return func(s sdktrace.ReadOnlySpan) {
		if s == nil {
			return
		}
		dur := s.EndTime().Sub(s.StartTime())
		if !TraceEnabled() && dur < PerSpanSlogThreshold {
			return
		}
		parent := ""
		if pid := s.Parent().SpanID(); pid.IsValid() {
			parent = pid.String()
		}
		logger.Info("perf_span",
			"name", s.Name(),
			"duration_ms", dur.Milliseconds(),
			"trace_id", s.SpanContext().TraceID().String(),
			"span_id", s.SpanContext().SpanID().String(),
			"parent", parent,
		)
	}
}

// NoopSpanSink returns a span sink that does nothing. Useful in tests
// or when only tree-level emission is desired.
func NoopSpanSink() SpanSink { return func(_ sdktrace.ReadOnlySpan) {} }

// NoopTreeSink returns a tree sink that does nothing. Useful in tests
// or when only per-span emission is desired.
func NoopTreeSink() TreeSink { return func(_ *Node) {} }

// Ensure the Shutdown signature matches the SDK so build-time changes
// to the SpanProcessor interface fail fast.
var _ sdktrace.SpanProcessor = (*TreeCollectorProcessor)(nil)

// Ensure context-keyed lookups in callers compile even when ctx is nil.
var _ = func() context.Context { return context.Background() }
