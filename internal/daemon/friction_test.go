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

	friction "github.com/sageox/frictionax"
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

	if fc.engine == nil {
		t.Error("engine should be initialized")
	}

	if !fc.IsEnabled() {
		t.Error("collector should be enabled by default")
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

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})

	// Stop triggers a final flush
	fc.Stop()

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

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.Stop()

	auth, ok := receivedAuth.Load().(string)
	require.True(t, ok, "server should have received a request")
	assert.Equal(t, "Bearer test-friction-token", auth)
}

// TestNewFrictionCollector_NoAuthTokenWhenGetterNotSet verifies friction events
// are sent without auth when SetAuthTokenGetter is not called.
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

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.Stop()

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

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.Stop()

	assert.True(t, envServerHit.Load(), "env var endpoint should receive the event")
	assert.False(t, projectServerHit.Load(), "project endpoint should NOT receive the event")
}

func TestFrictionCollector_Record(t *testing.T) {
	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record some events (unique inputs for dedup)
	for i := range 5 {
		fc.Record(friction.FrictionEvent{
			Kind:  "unknown-command",
			Input: fmt.Sprintf("test command %d", i),
		})
	}

	stats := fc.Stats()
	if stats.BufferCount != 5 {
		t.Errorf("BufferCount = %d, want 5", stats.BufferCount)
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
				fc.Record(friction.FrictionEvent{
					Kind:  "unknown-command",
					Input: fmt.Sprintf("concurrent g%d-e%d", goroutineID, j),
				})
			}
		}(g)
	}

	wg.Wait()

	// verify buffer is bounded (frictionax default buffer size is 100)
	stats := fc.Stats()
	if stats.BufferCount > stats.BufferSize {
		t.Errorf("BufferCount = %d, exceeds buffer size %d after concurrent load", stats.BufferCount, stats.BufferSize)
	}
}

// TestFrictionCollector_StartStop verifies lifecycle management.
func TestFrictionCollector_StartStop(t *testing.T) {
	// use a local server so the shutdown flush doesn't hit the real network
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// start should not panic
	fc.Start()

	// record some events
	for range 5 {
		fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
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
	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})

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

	if stats.BufferSize <= 0 {
		t.Errorf("BufferSize = %d, want > 0", stats.BufferSize)
	}

	if stats.SampleRate != 1.0 {
		t.Errorf("SampleRate = %f, want 1.0 (default)", stats.SampleRate)
	}

	if stats.CatalogVersion != "" {
		t.Errorf("CatalogVersion = %q, want empty initially", stats.CatalogVersion)
	}
}

// TestFrictionCollector_SubmitRequestFormat verifies the JSON wire format sent to the server.
func TestFrictionCollector_SubmitRequestFormat(t *testing.T) {
	var (
		receivedReq    friction.SubmitRequest
		receivedMethod string
		receivedPath   string
		receivedCT     string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedCT = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	fc.Record(friction.FrictionEvent{
		Kind:     "unknown-command",
		Actor:    "human",
		Input:    "ox foo",
		ErrorMsg: "unknown command",
	})
	fc.Stop()

	assert.Equal(t, http.MethodPost, receivedMethod)
	assert.Equal(t, "/api/v1/cli/friction", receivedPath)
	assert.Equal(t, "application/json", receivedCT)
	assert.NotEmpty(t, receivedReq.Version, "Version should be set")
	require.Len(t, receivedReq.Events, 1)

	event := receivedReq.Events[0]
	assert.Equal(t, friction.FailureKind("unknown-command"), event.Kind)
	assert.Equal(t, "human", event.Actor)
	assert.Equal(t, "ox foo", event.Input)
	assert.NotEmpty(t, event.Timestamp, "Timestamp should be set")
}

// TestFrictionCollector_Record_SetsTimestamp verifies Record auto-sets timestamp when empty.
func TestFrictionCollector_Record_SetsTimestamp(t *testing.T) {
	var receivedReq friction.SubmitRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	// record event without timestamp
	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.Stop()

	require.Len(t, receivedReq.Events, 1)
	assert.NotEmpty(t, receivedReq.Events[0].Timestamp, "timestamp should be set automatically")

	// verify it's valid RFC3339
	_, err := time.Parse(time.RFC3339, receivedReq.Events[0].Timestamp)
	assert.NoError(t, err, "timestamp should be valid RFC3339")
}

// TestFrictionCollector_DisabledViaSageoxFrictionEnv verifies SAGEOX_FRICTION=false disables collection.
func TestFrictionCollector_DisabledViaSageoxFrictionEnv(t *testing.T) {
	t.Setenv("SAGEOX_FRICTION", "false")

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	if fc.IsEnabled() {
		t.Error("collector should be disabled when SAGEOX_FRICTION=false")
	}

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})

	stats := fc.Stats()
	if stats.BufferCount != 0 {
		t.Errorf("BufferCount = %d, want 0 (disabled collector)", stats.BufferCount)
	}
}

// TestFrictionCollector_RecordFromIPC verifies IPC payload conversion preserves all fields.
func TestFrictionCollector_RecordFromIPC(t *testing.T) {
	var receivedReq friction.SubmitRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	payload := FrictionPayload{
		Timestamp:  "2026-01-15T10:00:00Z",
		Kind:       "unknown-command",
		Command:    "ox",
		Subcommand: "foo",
		Actor:      "human",
		AgentType:  "claude-code",
		PathBucket: "/home/user/project",
		Input:      "ox foo",
		ErrorMsg:   "unknown command",
	}

	fc.RecordFromIPC(payload)
	fc.Stop()

	require.Len(t, receivedReq.Events, 1)
	event := receivedReq.Events[0]
	assert.Equal(t, "2026-01-15T10:00:00Z", event.Timestamp)
	assert.Equal(t, friction.FailureKind("unknown-command"), event.Kind)
	assert.Equal(t, "ox", event.Command)
	assert.Equal(t, "foo", event.Subcommand)
	assert.Equal(t, "human", event.Actor)
	assert.Equal(t, "claude-code", event.AgentType)
	assert.Equal(t, "/home/user/project", event.PathBucket)
	assert.Equal(t, "ox foo", event.Input)
	assert.Equal(t, "unknown command", event.ErrorMsg)
}

// TestFrictionCollector_UserAgentHeader verifies the daemon User-Agent is sent on requests.
func TestFrictionCollector_UserAgentHeader(t *testing.T) {
	var receivedUA atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA.Store(r.Header.Get("User-Agent"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_FRICTION_ENDPOINT", server.URL)

	logger := testLogger()
	fc := NewFrictionCollector(logger, "")

	fc.Record(friction.FrictionEvent{Kind: "unknown-command", Input: "test"})
	fc.Stop()

	ua, ok := receivedUA.Load().(string)
	require.True(t, ok, "server should have received a request")
	assert.Contains(t, ua, "ox-daemon/", "User-Agent should contain daemon identifier")
}

// TestFrictionEvent_SynchronousDelivery_ExitSafe verifies that friction event
// IPC delivery completes synchronously before SendOneWay returns. This is the
// critical property that makes os.Exit() safe immediately after sending:
// no background goroutine can be killed mid-flight.
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
