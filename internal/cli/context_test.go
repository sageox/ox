package cli

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestContext_Accessors(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		method   string
		expected bool
	}{
		{"verbose true", &config.Config{Verbose: true}, "IsVerbose", true},
		{"verbose false", &config.Config{Verbose: false}, "IsVerbose", false},
		{"quiet true", &config.Config{Quiet: true}, "IsQuiet", true},
		{"quiet false", &config.Config{Quiet: false}, "IsQuiet", false},
		{"json true", &config.Config{JSON: true}, "IsJSON", true},
		{"json false", &config.Config{JSON: false}, "IsJSON", false},
		{"text true", &config.Config{Text: true}, "IsText", true},
		{"text false", &config.Config{Text: false}, "IsText", false},
		{"review true", &config.Config{Review: true}, "IsReview", true},
		{"review false", &config.Config{Review: false}, "IsReview", false},
		{"no-interactive true", &config.Config{NoInteractive: true}, "IsNoInteractive", true},
		{"no-interactive false", &config.Config{NoInteractive: false}, "IsNoInteractive", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{Config: tt.cfg}
			var got bool
			switch tt.method {
			case "IsVerbose":
				got = ctx.IsVerbose()
			case "IsQuiet":
				got = ctx.IsQuiet()
			case "IsJSON":
				got = ctx.IsJSON()
			case "IsText":
				got = ctx.IsText()
			case "IsReview":
				got = ctx.IsReview()
			case "IsNoInteractive":
				got = ctx.IsNoInteractive()
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestContext_LogMethods_NilLogger(t *testing.T) {
	// should not panic with nil logger
	ctx := &Context{Config: &config.Config{}, Logger: nil}

	assert.NotPanics(t, func() { ctx.LogInfo("test") })
	assert.NotPanics(t, func() { ctx.LogDebug("test") })
	assert.NotPanics(t, func() { ctx.LogWarn("test") })
	assert.NotPanics(t, func() { ctx.LogError("test") })
}

func TestContext_LogMethods_WithLogger(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, Logger: slog.Default()}

	// should not panic with real logger
	assert.NotPanics(t, func() { ctx.LogInfo("info msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogDebug("debug msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogWarn("warn msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogError("error msg", "key", "val") })
}

func TestContext_Shutdown_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.Shutdown() })
}

func TestContext_TrackCommandCompletion_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.TrackCommandCompletion(nil) })
}

func TestContext_TrackCommandError_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.TrackCommandError(nil, nil) })
}

func TestContext_PrintMethods_QuietMode(t *testing.T) {
	// quiet mode should suppress output without panicking
	ctx := &Context{Config: &config.Config{Quiet: true}}

	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
}

func TestContext_PrintMethods_JSONMode(t *testing.T) {
	// json mode should suppress human output without panicking
	ctx := &Context{Config: &config.Config{JSON: true}}

	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintError("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
	assert.NotPanics(t, func() { ctx.Fprintf(os.Stderr, "test %s", "val") })
}

func TestContext_PrintMethods_NormalMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	ctx := &Context{Config: &config.Config{}}

	// capture stdout to avoid noise
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull
	os.Stderr = devNull
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	// these should all execute without panic and actually call the underlying Print* functions
	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintError("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s\n", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
	assert.NotPanics(t, func() { ctx.Fprintf(os.Stderr, "test %s", "val") })
}

func TestContext_TrackCommandError_NilCmd(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	// nil cmd should not panic
	assert.NotPanics(t, func() { ctx.TrackCommandError(nil, nil) })
}

// --- FIX L: telemetry opt-out must gate OTLP network export ---
//
// Failure prevented: internal/cli/context.go used to hand observability.Init
// an unconditional tokenFunc (always calling auth.ExportBearerForEndpoint),
// so a user with 'telemetry off' who still held a valid session token kept
// exporting OTel spans — command name, exit code, redacted args, ox version,
// OS/arch — to the OTLP proxy for as long as the token stayed valid. The
// opt-out was honored by telemetry.Client (product events) but not by this
// path, an inconsistency users could reasonably read as bad faith.

// TestOtlpTokenFunc_DisabledAlwaysDropsRegardlessOfAuth pins the simplest
// property: when the caller says telemetry is disabled, the token func
// never surfaces a resolved bearer — not even one the resolver hands back.
// The injected resolver returns an obviously-fake, non-empty token so this
// test can't pass by accident just because a real, unauthenticated test
// environment would have returned "" anyway (see .claude/rules/testing.md
// "Failure Paths That Render Identically To Success").
func TestOtlpTokenFunc_DisabledAlwaysDropsRegardlessOfAuth(t *testing.T) {
	resolveBearer := func(ep string) string { return "should-never-surface-" + ep }
	fn := otlpTokenFunc(false /* telemetryEnabled */, "https://example.sageox.ai", resolveBearer)
	assert.Empty(t, fn(), "disabled telemetry must always yield an empty token")
}

// TestOtlpTokenFunc_EnabledDelegatesToResolver proves the enabled path is a
// pure passthrough — the exact endpoint reaches the resolver and its return
// value surfaces unmodified — so no precedence logic is reimplemented here
// that could drift from auth.ExportBearerForEndpoint's own behavior.
func TestOtlpTokenFunc_EnabledDelegatesToResolver(t *testing.T) {
	const ep = "https://example.sageox.ai"
	var gotEndpoint string
	resolveBearer := func(e string) string {
		gotEndpoint = e
		return "fake-bearer-xyz"
	}
	fn := otlpTokenFunc(true /* telemetryEnabled */, ep, resolveBearer)
	assert.Equal(t, "fake-bearer-xyz", fn())
	assert.Equal(t, ep, gotEndpoint, "resolver must be called with the exact apiEndpoint")
}

// TestOtlpTokenFunc_EnabledUsesRealExportBearerForEndpoint wires the real
// auth.ExportBearerForEndpoint (the production call site's actual argument)
// to prove the two compose correctly end to end.
func TestOtlpTokenFunc_EnabledUsesRealExportBearerForEndpoint(t *testing.T) {
	const ep = "https://example.sageox.ai"
	fn := otlpTokenFunc(true /* telemetryEnabled */, ep, auth.ExportBearerForEndpoint)
	assert.Equal(t, auth.ExportBearerForEndpoint(ep), fn())
}

// countingSpanProcessor counts every span handed to OnEnd, standing in for
// perf.TreeCollectorProcessor (the real local-tracing consumer wired via
// observability.AddSpanProcessor in NewContext) without pulling in the perf
// package's rendering side effects.
type countingSpanProcessor struct{ n *atomic.Int64 }

func (p countingSpanProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (p countingSpanProcessor) OnEnd(sdktrace.ReadOnlySpan)                     { p.n.Add(1) }
func (p countingSpanProcessor) Shutdown(context.Context) error                  { return nil }
func (p countingSpanProcessor) ForceFlush(context.Context) error                { return nil }

// TestOtlpTokenFunc_GatesNetworkExportButNotLocalTracing is the red-first,
// real-listener proof for FIX L: it drives the actual
// observability.Init/AddSpanProcessor/StartCommand machinery NewContext
// uses, against a real local HTTP listener standing in for the OTLP proxy.
//
//  1. telemetry "on" (a resolved bearer present): the span reaches the
//     listener.
//  2. telemetry "off" (otlpTokenFunc(false, ...)): zero spans reach the
//     listener — proving network export is fully suppressed.
//
// In both phases the local countingSpanProcessor — standing in for the perf
// tree renderer behind OX_TRACE=1/--verbose — receives the span. That is
// the regression this fix could easily have caused: gating by clearing
// apiEndpoint (rather than the token) would skip observability.Init's
// TracerProvider construction entirely and silently kill local tracing too.
func TestOtlpTokenFunc_GatesNetworkExportButNotLocalTracing(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spins up a real OTel exporter + local HTTP listener")
	}

	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var localSpans atomic.Int64
	observability.AddSpanProcessor(countingSpanProcessor{n: &localSpans})

	// Phase 1: telemetry ON, a bearer is present — span must reach the listener.
	require.NoError(t, observability.Init(context.Background(), "ox-cli-test", srv.URL, func() string { return "test-bearer" }))
	_, span := observability.StartCommand(context.Background(), "ox test-cmd-enabled")
	span.End()
	observability.Shutdown(context.Background())

	mu.Lock()
	requestsAfterEnabled := requests
	mu.Unlock()
	assert.Equal(t, 1, requestsAfterEnabled, "telemetry on with a resolved bearer must reach the OTLP listener")
	assert.Equal(t, int64(1), localSpans.Load(), "local span processor must see the span when telemetry is on")

	// Phase 2: telemetry OFF via otlpTokenFunc — zero NEW requests must reach the
	// listener, even though the injected resolver WOULD hand back a usable
	// bearer if it were ever called.
	fakeResolver := func(string) string { return "fake-bearer-that-must-never-be-used" }
	require.NoError(t, observability.Init(context.Background(), "ox-cli-test", srv.URL, otlpTokenFunc(false, srv.URL, fakeResolver)))
	_, span2 := observability.StartCommand(context.Background(), "ox test-cmd-disabled")
	span2.End()
	observability.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, requestsAfterEnabled, requests, "telemetry off must add zero new requests to the OTLP listener")
	assert.Equal(t, int64(2), localSpans.Load(), "local span processor must still see the span when telemetry is off")
}
