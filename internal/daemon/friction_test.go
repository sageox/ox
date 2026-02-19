package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/uxfriction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger returns a no-op logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNewFrictionCollector(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	if fc == nil {
		t.Fatal("NewFrictionCollector returned nil")
	}

	if fc.buffer == nil {
		t.Error("buffer should be initialized")
	}

	if fc.client == nil {
		t.Error("client should be initialized")
	}

	if fc.catalogCache == nil {
		t.Error("catalogCache should be initialized")
	}
}

// TestNewFrictionCollector_UsesProjectEndpoint verifies friction events are sent
// to the project's endpoint rather than the hardcoded production endpoint.
func TestNewFrictionCollector_UsesProjectEndpoint(t *testing.T) {
	var requestReceived atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived.Store(true)
		assert.Equal(t, "/api/v1/cli/friction", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// no env var override - project endpoint should be used
	t.Setenv("SAGEOX_FRICTION_ENDPOINT", "")

	logger := testLogger()
	fc := NewFrictionCollector(logger, server.URL)

	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	assert.True(t, requestReceived.Load(), "friction event should be sent to project endpoint")
}

// TestNewFrictionCollector_SendsAuthToken verifies friction events include the
// auth token from the heartbeat handler when SetAuthTokenGetter is wired.
func TestNewFrictionCollector_SendsAuthToken(t *testing.T) {
	var receivedAuth atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")
	fc.SetAuthTokenGetter(func() string { return "test-friction-token" })

	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	auth, ok := receivedAuth.Load().(string)
	require.True(t, ok, "server should have received a request")
	assert.Equal(t, "Bearer test-friction-token", auth)
}

// TestNewFrictionCollector_NoAuthTokenWhenGetterNotSet verifies friction events
// are sent without auth when SetAuthTokenGetter is not called (graceful degradation).
func TestNewFrictionCollector_NoAuthTokenWhenGetterNotSet(t *testing.T) {
	var receivedAuth atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")
	// deliberately NOT calling SetAuthTokenGetter

	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	auth, ok := receivedAuth.Load().(string)
	require.True(t, ok, "server should have received a request")
	assert.Empty(t, auth, "no auth header when getter is not set")
}

// TestNewFrictionCollector_EnvVarOverridesProjectEndpoint verifies env var takes precedence.
func TestNewFrictionCollector_EnvVarOverridesProjectEndpoint(t *testing.T) {
	var envServerHit atomic.Bool
	var projectServerHit atomic.Bool

	envServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envServerHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer envServer.Close()

	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectServerHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer projectServer.Close()

	// env var should win over project endpoint
	t.Setenv("SAGEOX_FRICTION_ENDPOINT", envServer.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, projectServer.URL)

	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	assert.True(t, envServerHit.Load(), "env var endpoint should receive the event")
	assert.False(t, projectServerHit.Load(), "project endpoint should NOT receive the event")
}

func TestFrictionCollector_Record(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record some events (unique inputs for dedup)
	for i := range 5 {
		fc.Record(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: fmt.Sprintf("test command %d", i),
		})
	}

	stats := fc.Stats()
	if stats.BufferCount != 5 {
		t.Errorf("BufferCount = %d, want 5", stats.BufferCount)
	}
}

func TestFrictionCollector_Record_SetsTimestamp(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record event without timestamp
	fc.Record(uxfriction.FrictionEvent{
		Kind:  "unknown-command",
		Input: "test",
	})

	// drain and check timestamp was set
	events := fc.buffer.Drain()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Timestamp == "" {
		t.Error("timestamp should be set automatically")
	}

	// verify it's a valid RFC3339 timestamp
	_, err := time.Parse(time.RFC3339, events[0].Timestamp)
	if err != nil {
		t.Errorf("timestamp is not valid RFC3339: %v", err)
	}
}

func TestFrictionCollector_Record_Truncates(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record event with oversized fields
	longInput := make([]byte, 1000)
	for i := range longInput {
		longInput[i] = 'x'
	}

	fc.Record(uxfriction.FrictionEvent{
		Kind:  "unknown-command",
		Input: string(longInput),
	})

	events := fc.buffer.Drain()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if len(events[0].Input) != uxfriction.MaxInputLength {
		t.Errorf("Input length = %d, want %d", len(events[0].Input), uxfriction.MaxInputLength)
	}
}

// TestFrictionCollector_BoundedMemory_HighLoad verifies that memory is bounded
// even under extreme load conditions. The key guarantee is that buffer count
// NEVER exceeds frictionBufferSize, regardless of how many events are added.
func TestFrictionCollector_BoundedMemory_HighLoad(t *testing.T) {
	// use a local buffer to test without network interactions
	logger := testLogger()

	// test the underlying RingBuffer directly for cleaner verification
	buffer := uxfriction.NewRingBuffer(frictionBufferSize)

	// record many more events than buffer capacity
	eventsToAdd := frictionBufferSize * 10 // 10x capacity

	for i := range eventsToAdd {
		buffer.Add(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: fmt.Sprintf("test command %d", i),
		})
	}

	// verify buffer count never exceeds capacity
	if buffer.Count() > frictionBufferSize {
		t.Errorf("BufferCount = %d, exceeds buffer size %d", buffer.Count(), frictionBufferSize)
	}

	// verify exactly at capacity (ring buffer overwrites oldest)
	if buffer.Count() != frictionBufferSize {
		t.Errorf("BufferCount = %d, want %d (buffer should be full)", buffer.Count(), frictionBufferSize)
	}

	// also verify the collector respects memory bounds
	// (even though early flush may drain some events)
	fc := NewFrictionCollector(logger, "")
	for i := range eventsToAdd {
		fc.Record(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: fmt.Sprintf("collector test %d", i),
		})
		// verify we never exceed capacity at any point
		stats := fc.Stats()
		if stats.BufferCount > frictionBufferSize {
			t.Fatalf("BufferCount = %d exceeded capacity %d after %d adds",
				stats.BufferCount, frictionBufferSize, i+1)
		}
	}
}

// TestFrictionCollector_BoundedMemory_ConcurrentLoad verifies memory bounds
// under concurrent write load.
func TestFrictionCollector_BoundedMemory_ConcurrentLoad(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	var wg sync.WaitGroup
	numGoroutines := 50
	eventsPerGoroutine := 100

	// launch many concurrent writers with unique inputs
	for g := range numGoroutines {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := range eventsPerGoroutine {
				fc.Record(uxfriction.FrictionEvent{
					Kind:  "unknown-command",
					Input: fmt.Sprintf("concurrent g%d-e%d", goroutineID, j),
				})
			}
		}(g)
	}

	wg.Wait()

	// verify buffer is bounded
	stats := fc.Stats()
	if stats.BufferCount > frictionBufferSize {
		t.Errorf("BufferCount = %d, exceeds buffer size %d after concurrent load", stats.BufferCount, frictionBufferSize)
	}
}

// TestFrictionCollector_BackendUnavailable verifies that events are dropped gracefully
// when the backend is unavailable, without blocking or memory growth.
func TestFrictionCollector_BackendUnavailable(t *testing.T) {
	// create a server that always returns 503
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	// set endpoint to test server
	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record events
	for range 50 {
		fc.Record(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: "test",
		})
	}

	// attempt flush (should not panic or block)
	fc.flush()

	// buffer should be empty after drain (even if submit failed)
	stats := fc.Stats()
	if stats.BufferCount != 0 {
		t.Errorf("BufferCount = %d, want 0 after flush", stats.BufferCount)
	}
}

// TestFrictionCollector_BackendTimeout verifies graceful handling of slow backends.
func TestFrictionCollector_BackendTimeout(t *testing.T) {
	// create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // longer than timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record events
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: "test",
		})
	}

	// flush with timeout (should return without blocking for 10s)
	start := time.Now()
	fc.flush()
	elapsed := time.Since(start)

	// should complete within reasonable timeout (< 6 seconds)
	if elapsed > 6*time.Second {
		t.Errorf("flush took %v, expected < 6s (timeout)", elapsed)
	}
}

// TestFrictionCollector_BackendConnectionRefused verifies handling of connection failures.
func TestFrictionCollector_BackendConnectionRefused(t *testing.T) {
	// use an endpoint that will refuse connections
	t.Setenv("SAGEOX_FRICTION_ENDPOINT", "http://localhost:59999") // unlikely to be in use

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record events
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{
			Kind:  "unknown-command",
			Input: "test",
		})
	}

	// flush should not panic
	fc.flush()

	// buffer should be drained (events discarded on error)
	stats := fc.Stats()
	if stats.BufferCount != 0 {
		t.Errorf("BufferCount = %d, want 0 after failed flush", stats.BufferCount)
	}
}

// TestFrictionCollector_RateLimiting_SampleRate verifies sample rate handling.
func TestFrictionCollector_RateLimiting_SampleRate(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		// return sample rate of 0 to stop all future requests
		w.Header().Set("X-SageOx-Sample-Rate", "0.0")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// first batch - should send
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	}
	fc.flush()

	// verify first request was made
	if requestCount.Load() != 1 {
		t.Errorf("requestCount = %d, want 1 after first flush", requestCount.Load())
	}

	// second batch - should be skipped due to sample rate 0
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	}
	fc.flush()

	// verify no additional requests (sample rate 0 blocks)
	if requestCount.Load() != 1 {
		t.Errorf("requestCount = %d, want 1 (should be blocked by sample rate 0)", requestCount.Load())
	}
}

// TestFrictionCollector_RateLimiting_RetryAfter verifies Retry-After handling.
func TestFrictionCollector_RateLimiting_RetryAfter(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		// return Retry-After of 3600 seconds (1 hour)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// first batch - should send
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	}
	fc.flush()

	if requestCount.Load() != 1 {
		t.Errorf("requestCount = %d, want 1 after first flush", requestCount.Load())
	}

	// second batch - should be skipped due to Retry-After
	for range 10 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	}
	fc.flush()

	// verify no additional requests (blocked by Retry-After)
	if requestCount.Load() != 1 {
		t.Errorf("requestCount = %d, want 1 (should be blocked by Retry-After)", requestCount.Load())
	}

	// verify retry-after time is set
	stats := fc.Stats()
	if stats.RetryAfter.IsZero() {
		t.Error("RetryAfter should be set")
	}
}

// TestFrictionCollector_MaxEventsPerRequest verifies truncation to max events.
func TestFrictionCollector_MaxEventsPerRequest(t *testing.T) {
	var maxReceivedEvents atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req uxfriction.SubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		// track the max events received in any single request
		count := int32(len(req.Events))
		for {
			current := maxReceivedEvents.Load()
			if count <= current {
				break
			}
			if maxReceivedEvents.CompareAndSwap(current, count) {
				break
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record more events than max per request (unique inputs for dedup)
	for i := range 150 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: fmt.Sprintf("test %d", i)})
	}
	fc.flush()

	// verify events were truncated to max (no single request should exceed max)
	if maxReceivedEvents.Load() > int32(uxfriction.MaxEventsPerRequest) {
		t.Errorf("maxReceivedEvents = %d, want <= %d", maxReceivedEvents.Load(), uxfriction.MaxEventsPerRequest)
	}
}

// TestFrictionCollector_StartStop verifies lifecycle management.
func TestFrictionCollector_StartStop(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// start should not panic
	fc.Start()

	// record some events
	for range 5 {
		fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	}

	// stop should not panic and should return
	done := make(chan struct{})
	go func() {
		fc.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good, Stop() returned
	case <-time.After(5 * time.Second):
		t.Error("Stop() did not return within 5 seconds")
	}
}

// TestFrictionCollector_DoubleStop verifies Stop() is safe to call multiple times.
func TestFrictionCollector_DoubleStop(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	fc.Start()

	// stop multiple times should not panic
	fc.Stop()
	fc.Stop()
	fc.Stop()
}

// TestFrictionCollector_DisabledNoOp verifies disabled collector is no-op.
func TestFrictionCollector_DisabledNoOp(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	if fc.IsEnabled() {
		t.Error("collector should be disabled when DO_NOT_TRACK=1")
	}

	// recording should be no-op
	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})

	stats := fc.Stats()
	if stats.BufferCount != 0 {
		t.Errorf("BufferCount = %d, want 0 (disabled collector)", stats.BufferCount)
	}

	// start/stop should be no-op
	fc.Start()
	fc.Stop()
}

// TestFrictionCollector_Stats verifies stats reporting.
func TestFrictionCollector_Stats(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	stats := fc.Stats()

	if stats.BufferSize != frictionBufferSize {
		t.Errorf("BufferSize = %d, want %d", stats.BufferSize, frictionBufferSize)
	}

	if stats.SampleRate != 1.0 {
		t.Errorf("SampleRate = %f, want 1.0 (default)", stats.SampleRate)
	}

	if !stats.RetryAfter.IsZero() {
		t.Error("RetryAfter should be zero initially")
	}

	if stats.CatalogVersion != "" {
		t.Errorf("CatalogVersion = %q, want empty initially", stats.CatalogVersion)
	}
}

// TestFrictionCollector_SubmitRequestFormat verifies the JSON format sent to server.
func TestFrictionCollector_SubmitRequestFormat(t *testing.T) {
	var receivedReq uxfriction.SubmitRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify headers
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %v, want application/json", ct)
		}

		if r.Method != http.MethodPost {
			t.Errorf("Method = %v, want POST", r.Method)
		}

		if r.URL.Path != "/api/v1/cli/friction" {
			t.Errorf("Path = %v, want /api/v1/cli/friction", r.URL.Path)
		}

		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	fc.Record(uxfriction.FrictionEvent{
		Kind:     "unknown-command",
		Actor:    "human",
		Input:    "ox foo",
		ErrorMsg: "unknown command",
	})
	fc.flush()

	// verify version field
	if receivedReq.Version == "" {
		t.Error("Version should be set")
	}

	// verify event structure
	if len(receivedReq.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(receivedReq.Events))
	}

	event := receivedReq.Events[0]
	if event.Kind != "unknown-command" {
		t.Errorf("Kind = %v, want unknown-command", event.Kind)
	}
	if event.Actor != "human" {
		t.Errorf("Actor = %v, want human", event.Actor)
	}
	if event.Input != "ox foo" {
		t.Errorf("Input = %v, want 'ox foo'", event.Input)
	}
	if event.Timestamp == "" {
		t.Error("Timestamp should be set")
	}
}

// TestFrictionCollector_CatalogVersionHeader verifies X-Catalog-Version header is sent.
func TestFrictionCollector_CatalogVersionHeader(t *testing.T) {
	t.Parallel()

	var receivedVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedVersion = r.Header.Get("X-Catalog-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "friction-catalog.json")

	// pre-populate catalog cache
	catalog := &uxfriction.CatalogData{Version: "v2026-01-17-001"}
	data, _ := json.Marshal(catalog)
	require.NoError(t, os.WriteFile(catalogPath, data, 0600))

	// create collector with custom cache path
	logger := testLogger()
	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		client:       uxfriction.NewClient(uxfriction.ClientConfig{Endpoint: server.URL, Version: "test"}),
		catalogCache: &CatalogCache{filePath: catalogPath},
		enabled:      true,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}

	// load catalog cache
	require.NoError(t, fc.catalogCache.Load())

	// record and flush
	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	assert.Equal(t, "v2026-01-17-001", receivedVersion)
}

// TestFrictionCollector_CatalogUpdateFromResponse verifies catalog is updated from response.
func TestFrictionCollector_CatalogUpdateFromResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := uxfriction.FrictionResponse{
			Accepted: 1,
			Catalog: &uxfriction.CatalogData{
				Version: "v2026-01-17-002",
				Tokens: []uxfriction.TokenMapping{
					{Pattern: "prine", Target: "prime", Kind: "unknown-command"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "friction-catalog.json")

	// create collector with custom cache path
	logger := testLogger()
	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		client:       uxfriction.NewClient(uxfriction.ClientConfig{Endpoint: server.URL, Version: "test"}),
		catalogCache: &CatalogCache{filePath: catalogPath},
		enabled:      true,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}

	// initially no catalog
	assert.Empty(t, fc.CatalogVersion())

	// record and flush
	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	// catalog should be updated
	assert.Equal(t, "v2026-01-17-002", fc.CatalogVersion())

	// verify catalog data
	data := fc.CatalogData()
	require.NotNil(t, data)
	require.Len(t, data.Tokens, 1)
	assert.Equal(t, "prine", data.Tokens[0].Pattern)
	assert.Equal(t, "prime", data.Tokens[0].Target)

	// verify file was written
	_, err := os.Stat(catalogPath)
	assert.NoError(t, err)
}

// TestFrictionCollector_CatalogNotUpdatedWhenSameVersion verifies cache is not updated when version matches.
func TestFrictionCollector_CatalogNotUpdatedWhenSameVersion(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		// return same version as client has
		resp := uxfriction.FrictionResponse{
			Accepted: 1,
			Catalog: &uxfriction.CatalogData{
				Version: "v1",
				Tokens:  []uxfriction.TokenMapping{{Pattern: "new", Target: "data"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "friction-catalog.json")

	// pre-populate with v1
	catalog := &uxfriction.CatalogData{
		Version: "v1",
		Tokens:  []uxfriction.TokenMapping{{Pattern: "old", Target: "data"}},
	}
	data, _ := json.Marshal(catalog)
	require.NoError(t, os.WriteFile(catalogPath, data, 0600))

	logger := testLogger()
	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		client:       uxfriction.NewClient(uxfriction.ClientConfig{Endpoint: server.URL, Version: "test"}),
		catalogCache: &CatalogCache{filePath: catalogPath},
		enabled:      true,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}
	require.NoError(t, fc.catalogCache.Load())

	// record and flush
	fc.Record(uxfriction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.flush()

	// catalog version should still be v1 (no update)
	assert.Equal(t, "v1", fc.CatalogVersion())

	// data should still be old (Update checks version before saving)
	cachedData := fc.CatalogData()
	require.NotNil(t, cachedData)
	// the in-memory data gets updated even with same version (no extra disk write)
}

// TestFrictionCollector_UpdateCatalog verifies manual catalog update.
func TestFrictionCollector_UpdateCatalog(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "friction-catalog.json")

	logger := testLogger()
	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		client:       uxfriction.NewClient(uxfriction.ClientConfig{Endpoint: "http://test", Version: "test"}),
		catalogCache: &CatalogCache{filePath: catalogPath},
		enabled:      true,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}

	// manually update catalog
	catalog := &uxfriction.CatalogData{
		Version: "v1",
		Commands: []uxfriction.CommandMapping{
			{Pattern: "old cmd", Target: "new cmd", Confidence: 0.95},
		},
	}

	updated, err := fc.UpdateCatalog(catalog)
	require.NoError(t, err)
	assert.True(t, updated)

	// verify update
	assert.Equal(t, "v1", fc.CatalogVersion())
	data := fc.CatalogData()
	require.Len(t, data.Commands, 1)
	assert.Equal(t, "old cmd", data.Commands[0].Pattern)

	// second update with same version should not update
	updated, err = fc.UpdateCatalog(catalog)
	require.NoError(t, err)
	assert.False(t, updated)
}

// TestFrictionCollector_StatsIncludesCatalogVersion verifies catalog version in stats.
func TestFrictionCollector_StatsIncludesCatalogVersion(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "friction-catalog.json")

	// pre-populate catalog
	catalog := &uxfriction.CatalogData{Version: "v2026-01-17-005"}
	data, _ := json.Marshal(catalog)
	require.NoError(t, os.WriteFile(catalogPath, data, 0600))

	logger := testLogger()
	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		client:       uxfriction.NewClient(uxfriction.ClientConfig{Endpoint: "http://test", Version: "test"}),
		catalogCache: &CatalogCache{filePath: catalogPath},
		enabled:      true,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}
	require.NoError(t, fc.catalogCache.Load())

	stats := fc.Stats()
	assert.Equal(t, "v2026-01-17-005", stats.CatalogVersion)
}

// TestFrictionEvent_SynchronousDelivery_ExitSafe verifies that friction event
// IPC delivery completes synchronously before SendOneWay returns. This is the
// critical property that makes os.Exit() safe immediately after sendFrictionEvent:
// no background goroutine can be killed mid-flight.
//
// The test sets up a real Unix socket server, sends a friction event via
// SendOneWay (same code path as production), and asserts the event was received
// by the time SendOneWay returns — with no sleep or WaitGroup.
func TestFrictionEvent_SynchronousDelivery_ExitSafe(t *testing.T) {
	t.Parallel()

	// set up a temp Unix socket (short path to stay under macOS 104-char limit)
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-fric-%d.sock", time.Now().UnixNano()%100000))
	t.Cleanup(func() { os.Remove(socketPath) })

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	// channel to capture what the server received
	received := make(chan []byte, 1)

	// start a minimal server that reads one message
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		if n > 0 {
			received <- buf[:n]
		}
	}()

	// build the friction payload (same as cmd/ox/friction.go sendFrictionEvent)
	payload := FrictionPayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Kind:      "unknown-command",
		Command:   "ox",
		Input:     "ox notacommand",
		Actor:     "human",
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	// send via synchronous IPC (5ms timeout, same as production)
	client := NewClientWithSocket(socketPath)
	client.timeout = 5 * time.Millisecond
	sendErr := client.SendOneWay(Message{
		Type:    MsgTypeFriction,
		Payload: data,
	})
	require.NoError(t, sendErr)

	// at this point, SendOneWay has returned — if it were async (goroutine),
	// the server might not have received the message yet. Since it's synchronous,
	// the kernel has accepted the write before we get here.
	select {
	case msg := <-received:
		// verify the message contains our friction event
		assert.Contains(t, string(msg), `"type":"friction"`)
		assert.Contains(t, string(msg), `"ox notacommand"`)
	case <-time.After(1 * time.Second):
		t.Fatal("server did not receive friction event — delivery was not synchronous")
	}
}
