package observability

import (
	"context"
	"runtime"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installTestTracer wires a tracetest.SpanRecorder into the package-global
// tracer/rootSpan so the SetCommandAttrs / SetExitCode / SetResultCount
// helpers can be exercised against a real OTel SDK without an exporter.
//
// Returns the recorder so tests can inspect ended spans, and a cleanup
// function that ends the root span and restores the previous global state.
//
// NOT parallel-safe — tests using this helper must not call t.Parallel().
func installTestTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	mu.Lock()
	prevTracer := tracer
	prevRootSpan := rootSpan
	prevRootCtx := rootCtx
	tracer = tp.Tracer("test")
	mu.Unlock()

	ctx, span := tracer.Start(context.Background(), "test-root")

	mu.Lock()
	rootCtx = ctx
	rootSpan = span
	mu.Unlock()

	t.Cleanup(func() {
		mu.Lock()
		if rootSpan != nil {
			rootSpan.End()
		}
		tracer = prevTracer
		rootSpan = prevRootSpan
		rootCtx = prevRootCtx
		mu.Unlock()
		_ = tp.Shutdown(context.Background())
	})

	return recorder
}

// findAttr returns the typed value of the first attribute with the given key.
func findAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	t.Fatalf("attribute %q not found on span; have: %v", key, span.Attributes())
	return attribute.Value{}
}

// hasAttr reports whether a span carries an attribute with the given key.
func hasAttr(span sdktrace.ReadOnlySpan, key string) bool {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return true
		}
	}
	return false
}

// TestSetCommandAttrs_AttachesUniversalAttrs verifies that the four
// universal attributes (ox.version, host.os, host.arch, ox.command.args)
// land on the root span with the expected values.
//
// Failure prevented: a refactor that drops one of the universal attrs
// would silently make every CLI trace miss the field, and operators
// would only notice when querying for it after the regression shipped.
func TestSetCommandAttrs_AttachesUniversalAttrs(t *testing.T) {
	// Ensure AGENT_ENV is unset so this case isolates the universal-only
	// path. The agent-env branches are covered by their own tests.
	t.Setenv("AGENT_ENV", "")

	recorder := installTestTracer(t)

	SetCommandAttrs("0.99.0", []string{"code", "search", "--limit", "10"})

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(ended))
	}
	span := ended[0]

	if v := findAttr(t, span, AttrOXVersion).AsString(); v != "0.99.0" {
		t.Errorf("ox.version = %q, want %q", v, "0.99.0")
	}
	if v := findAttr(t, span, AttrHostOS).AsString(); v != runtime.GOOS {
		t.Errorf("host.os = %q, want %q", v, runtime.GOOS)
	}
	if v := findAttr(t, span, AttrHostArch).AsString(); v != runtime.GOARCH {
		t.Errorf("host.arch = %q, want %q", v, runtime.GOARCH)
	}

	args := findAttr(t, span, AttrCLIArgs).AsStringSlice()
	wantArgs := []string{"<REDACTED>", "<REDACTED>", "--limit", "<REDACTED>"}
	if len(args) != len(wantArgs) {
		t.Fatalf("ox.command.args = %v, want %v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("ox.command.args[%d] = %q, want %q", i, args[i], wantArgs[i])
		}
	}

	// AGENT_ENV is unset, so ox.agent.env must be absent — not present
	// as an empty string. This is the contract that lets backends use
	// "ox.agent.env exists" to filter agent traffic from human traffic.
	if hasAttr(span, AttrAgentEnv) {
		t.Errorf("ox.agent.env present on human-driven invocation; have: %v",
			span.Attributes())
	}
}

// TestSetCommandAttrs_AttachesAgentEnv_WhenSet verifies that when the
// AGENT_ENV environment variable is set (typical of an AI coworker
// invocation via adapter hooks), the value lands on the root span as
// ox.agent.env.
//
// Failure prevented: a refactor that loses the AGENT_ENV propagation
// would silently make every agent-driven trace look like a human
// invocation, breaking attribution dashboards.
func TestSetCommandAttrs_AttachesAgentEnv_WhenSet(t *testing.T) {
	t.Setenv("AGENT_ENV", "claude-code")

	recorder := installTestTracer(t)

	SetCommandAttrs("0.0.1", []string{"agent", "OxAAA1", "session", "start"})

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	span := recorder.Ended()[0]
	if v := findAttr(t, span, AttrAgentEnv).AsString(); v != "claude-code" {
		t.Errorf("ox.agent.env = %q, want %q", v, "claude-code")
	}
}

// TestSetCommandAttrs_OmitsAgentEnv_WhenEmpty pins the inverse: when
// AGENT_ENV is the empty string, the attribute is OMITTED from the span,
// not set to "". The "exists" filter is the entire point of this design.
//
// Failure prevented: a "set anyway, downstream can filter" simplification
// that would pollute every human invocation with ox.agent.env="".
func TestSetCommandAttrs_OmitsAgentEnv_WhenEmpty(t *testing.T) {
	t.Setenv("AGENT_ENV", "")

	recorder := installTestTracer(t)

	SetCommandAttrs("0.0.1", []string{"version"})

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	span := recorder.Ended()[0]
	if hasAttr(span, AttrAgentEnv) {
		t.Errorf("ox.agent.env present despite AGENT_ENV being empty; have: %v",
			span.Attributes())
	}
}

// TestSetCommandAttrs_RedactsValues is the contract test that proves the
// args attribute never carries raw user input. We deliberately choose
// inputs that look like secrets — if any of them appear verbatim in the
// emitted attribute, the helper has leaked.
//
// Failure prevented: a future change that bypasses RedactArgs (e.g., a
// "fast path for short arg lists") and exports values directly.
func TestSetCommandAttrs_RedactsValues(t *testing.T) {
	recorder := installTestTracer(t)

	secrets := []string{"--token", "ghp_REAL_SECRET_VALUE", "search", "internal-codename-leaks"}
	SetCommandAttrs("0.0.1", secrets)

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	args := recorder.Ended()[0].Attributes()
	for _, kv := range args {
		if string(kv.Key) != AttrCLIArgs {
			continue
		}
		for _, tok := range kv.Value.AsStringSlice() {
			if tok == "ghp_REAL_SECRET_VALUE" || tok == "internal-codename-leaks" {
				t.Errorf("ox.command.args leaked a value token: %q (full slice: %v)",
					tok, kv.Value.AsStringSlice())
			}
		}
	}
}

// TestSetExitCode_SetsAttrAndStatus verifies cli.exit_code lands on the
// span and that a non-zero code marks the span status as Error.
//
// Failure prevented: silently dropping exit_code or marking failures as
// successful in OTel backends — both make it impossible to filter for
// failed CLI invocations from traces.
func TestSetExitCode_SetsAttrAndStatus(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		wantStatus codes.Code
	}{
		{"success_zero", 0, codes.Unset},
		{"failure_one", 1, codes.Error},
		{"failure_other", 42, codes.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := installTestTracer(t)

			SetExitCode(tt.code)

			mu.RLock()
			rootSpan.End()
			mu.RUnlock()

			span := recorder.Ended()[0]
			if v := findAttr(t, span, AttrCLIExitCode).AsInt64(); v != int64(tt.code) {
				t.Errorf("cli.exit_code = %d, want %d", v, tt.code)
			}
			if span.Status().Code != tt.wantStatus {
				t.Errorf("status = %v, want %v", span.Status().Code, tt.wantStatus)
			}
		})
	}
}

// TestSetResultCount_SetsAttr verifies result.count lands on the root span
// with the right integer value.
//
// Failure prevented: a typo in the attribute key would still pass code
// review but make the metric unqueryable in Honeycomb.
func TestSetResultCount_SetsAttr(t *testing.T) {
	recorder := installTestTracer(t)

	SetResultCount(137)

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	span := recorder.Ended()[0]
	if v := findAttr(t, span, AttrResultCount).AsInt64(); v != 137 {
		t.Errorf("result.count = %d, want 137", v)
	}
}

// TestSetResultCount_Zero verifies that zero is recorded as a real value,
// not omitted. A "zero results" search is meaningful telemetry.
//
// Failure prevented: a "skip if empty" optimization would erase the
// distinction between "no result.count attr" (untraced command) and
// "result.count = 0" (search returned nothing).
func TestSetResultCount_Zero(t *testing.T) {
	recorder := installTestTracer(t)

	SetResultCount(0)

	mu.RLock()
	rootSpan.End()
	mu.RUnlock()

	span := recorder.Ended()[0]
	if !hasAttr(span, AttrResultCount) {
		t.Error("result.count = 0 was omitted from span; want explicit zero")
	}
	if v := findAttr(t, span, AttrResultCount).AsInt64(); v != 0 {
		t.Errorf("result.count = %d, want 0", v)
	}
}

// TestSetters_NoOpWhenTracingDisabled verifies that all three setter
// helpers are safe to call when tracing was never initialized. They
// must not panic and must not allocate spans on a nil tracer.
//
// Failure prevented: tracing-disabled paths (no OTel endpoint, daemon
// down, init failure) would panic on every command if the helpers
// dereferenced a nil rootSpan.
func TestSetters_NoOpWhenTracingDisabled(t *testing.T) {
	// Snapshot and clear globals to simulate "tracing not initialized".
	mu.Lock()
	prevTracer := tracer
	prevRootSpan := rootSpan
	tracer = nil
	rootSpan = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		tracer = prevTracer
		rootSpan = prevRootSpan
		mu.Unlock()
	})

	// None of these should panic or do anything observable.
	SetCommandAttrs("0.0.0", []string{"foo", "bar"})
	SetExitCode(7)
	SetResultCount(99)
}
