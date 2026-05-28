package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

// --- A. TraceParent without initialization ---

// TestTraceParent_NoInit ensures TraceParent returns empty before Init.
// Failure prevented: nil deref or garbage traceparent before OTel setup.
func TestTraceParent_NoInit(t *testing.T) {
	resetGlobals()
	assert.Empty(t, TraceParent())
}

// --- B. StartCommand without Init ---

// TestStartCommand_NoInit ensures StartCommand is safe before Init.
// Failure prevented: panic when tracing is disabled.
func TestStartCommand_NoInit(t *testing.T) {
	resetGlobals()
	ctx, span := StartCommand(context.Background(), "ox test")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	assert.Empty(t, TraceParent(), "should still be empty without Init")
}

// --- C. Enabled state ---

// TestEnabled_Default verifies Enabled returns false before Init.
func TestEnabled_Default(t *testing.T) {
	resetGlobals()
	assert.False(t, Enabled())
}

// --- D. Shutdown safety ---

// TestShutdown_NoInit ensures Shutdown is safe before Init.
// Failure prevented: nil deref on shutdown without init.
func TestShutdown_NoInit(t *testing.T) {
	resetGlobals()
	assert.NotPanics(t, func() {
		Shutdown(context.Background())
	})
}

// --- E. Init with bad endpoint ---

// TestInit_EmptyEndpoint disables tracing gracefully.
func TestInit_EmptyEndpoint(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "test", "", nil)
	require.NoError(t, err)
	assert.False(t, Enabled())
}

// TestInit_InvalidEndpoint disables tracing gracefully.
func TestInit_InvalidEndpoint(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "test", "://bad", nil)
	require.NoError(t, err)
	assert.False(t, Enabled())
}

// --- F. CommandName formatting ---

func TestCommandName(t *testing.T) {
	assert.Equal(t, "ox login", CommandName("login"))
	assert.Equal(t, "ox agent prime", CommandName("agent", "prime"))
	assert.Equal(t, "ox session stop", CommandName("session", "stop"))
}

// --- G. Init with valid endpoint (uses test exporter) ---

// TestInit_ValidEndpoint verifies full lifecycle: init → start → traceparent → shutdown.
// Uses a non-routable endpoint; export will fail silently (which is fine).
func TestInit_ValidEndpoint(t *testing.T) {
	resetGlobals()

	// Use a non-routable IP so export silently fails (no real Collector needed)
	err := Init(context.Background(), "ox-cli-test", "http://192.0.2.1:4318", nil)
	require.NoError(t, err)
	assert.True(t, Enabled())

	// start a command span
	ctx, span := StartCommand(context.Background(), "ox test-cmd")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)

	// traceparent should be valid W3C format
	tp := TraceParent()
	assert.Regexp(t, traceparentRe, tp, "should produce valid W3C traceparent")

	// all calls should share the same trace ID
	tp2 := TraceParent()
	assert.Equal(t, tp, tp2, "same root span = same traceparent")

	// shutdown should not panic
	Shutdown(context.Background())
	assert.Empty(t, TraceParent(), "traceparent should be empty after shutdown")
}

// --- H. SetCommandStatus safety ---

func TestSetCommandStatus_NoSpan(t *testing.T) {
	resetGlobals()
	assert.NotPanics(t, func() {
		SetCommandStatus(nil)
		SetCommandStatus(assert.AnError)
	})
}

// --- I. JWT bearer attachment (regression for OTLP 401 dropouts) ---

// TestInit_BearerAttachedPerRequest verifies the exporter attaches the
// current token from tokenFunc on every export, and picks up rotated
// values without restart.
// Failure prevented: PR #1217 server-side JWT-gating silently dropping
// CLI/daemon trace batches because the exporter sent no Authorization
// header (174k 401s/30d in prod).
func TestInit_BearerAttachedPerRequest(t *testing.T) {
	resetGlobals()

	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var tokenMu sync.Mutex
	current := "tok-A"
	tokenFunc := func() string {
		tokenMu.Lock()
		defer tokenMu.Unlock()
		return current
	}

	require.NoError(t, Init(context.Background(), "ox-cli-test", srv.URL, tokenFunc))
	require.True(t, Enabled())

	_, span := StartCommand(context.Background(), "ox test-cmd")
	span.End()
	Shutdown(context.Background())

	resetGlobals()
	tokenMu.Lock()
	current = "tok-B"
	tokenMu.Unlock()

	require.NoError(t, Init(context.Background(), "ox-cli-test", srv.URL, tokenFunc))
	_, span2 := StartCommand(context.Background(), "ox test-cmd-2")
	span2.End()
	Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen, "exporter should have made at least one request")
	for _, h := range seen {
		assert.True(t, strings.HasPrefix(h, "Bearer "), "every export must carry Bearer header, got %q", h)
	}
	// At least one batch from each phase should reflect its rotated token.
	assert.Contains(t, seen, "Bearer tok-A")
	assert.Contains(t, seen, "Bearer tok-B")
}

// TestInit_NilTokenFuncSendsUnauthenticated verifies that passing nil
// tokenFunc is a no-op for header injection (the server will then 401,
// which matches "logged out" behavior). Documents the contract.
func TestInit_NilTokenFuncSendsUnauthenticated(t *testing.T) {
	resetGlobals()

	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, Init(context.Background(), "ox-cli-test", srv.URL, nil))
	_, span := StartCommand(context.Background(), "ox test-cmd")
	span.End()
	Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen {
		assert.Empty(t, h, "nil tokenFunc must not inject Authorization")
	}
}

// TestInit_EmptyTokenSkipsHeader verifies that a tokenFunc returning ""
// (e.g. user logged out) leaves Authorization unset rather than sending
// "Bearer ", which would be a parse error server-side.
func TestInit_EmptyTokenSkipsHeader(t *testing.T) {
	resetGlobals()

	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, Init(context.Background(), "ox-cli-test", srv.URL, func() string { return "" }))
	_, span := StartCommand(context.Background(), "ox test-cmd")
	span.End()
	Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen {
		assert.Empty(t, h, "empty tokenFunc value must not inject Authorization")
	}
}

func resetGlobals() {
	mu.Lock()
	defer mu.Unlock()
	rootCtx = nil
	rootSpan = nil
	tracer = nil
	shutFn = nil
	extraProcessors = nil
}
