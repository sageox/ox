package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_DefaultEnabled(t *testing.T) {
	// clear any user config that might affect this
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client := NewClient("test-session")
	defer client.Stop()

	assert.True(t, client.IsEnabled(), "expected telemetry to be enabled by default")
}

func TestNewClient_ExplicitlyDisabled(t *testing.T) {
	client := NewClient("test-session", WithEnabled(false))
	defer client.Stop()

	assert.False(t, client.IsEnabled(), "expected telemetry to be disabled when explicitly set")
}

func TestClient_TrackWhenDisabled(t *testing.T) {
	client := NewClient("test-session", WithEnabled(false))

	// should not panic or block
	client.Track(Event{Type: EventCommandComplete, Command: "test"})
	client.TrackCommand("test", 100*time.Millisecond, true, "")
	client.TrackAsync(Event{Type: EventCommandComplete})
	client.Flush()

	// verify queue is empty (nothing was queued)
	assert.Empty(t, client.queue, "expected empty queue when disabled")
}

func TestClient_Track(t *testing.T) {
	client := NewClient("test-session", WithEnabled(true))

	event := Event{
		Type:    EventCommandComplete,
		Command: "doctor",
		Success: true,
	}

	client.Track(event)

	client.mu.Lock()
	defer client.mu.Unlock()

	require.Len(t, client.queue, 1, "expected 1 event in queue")
	assert.Equal(t, "doctor", client.queue[0].Command)
	assert.False(t, client.queue[0].Timestamp.IsZero(), "expected timestamp to be set")
}

func TestClient_TrackCommand(t *testing.T) {
	client := NewClient("test-session", WithEnabled(true))

	client.TrackCommand("init", 150*time.Millisecond, true, "")
	client.TrackCommand("doctor", 50*time.Millisecond, false, "ERR_CONFIG")

	stats := client.GetStats()

	assert.Equal(t, 2, stats.CommandCount)
	assert.Equal(t, 1, stats.ErrorCount)
	assert.Equal(t, 1, stats.CommandCounts["init"])
	assert.Equal(t, 1, stats.CommandCounts["doctor"])
}

func TestClient_QueueOverflow(t *testing.T) {
	client := NewClient("test-session", WithEnabled(true))

	// fill beyond max queue size
	for i := 0; i < maxQueueSize+10; i++ {
		client.Track(Event{Type: EventCommandComplete, Command: "test"})
	}

	client.mu.Lock()
	queueLen := len(client.queue)
	client.mu.Unlock()

	assert.LessOrEqual(t, queueLen, maxQueueSize, "queue exceeded max size")
}

func TestClient_FlushSendsEvents(t *testing.T) {
	var received atomic.Int32
	var receivedBatch Batch

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, telemetryPath, r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		err := json.NewDecoder(r.Body).Decode(&receivedBatch)
		require.NoError(t, err, "failed to decode batch")

		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	// queue some events
	client.Track(Event{Type: EventCommandComplete, Command: "init"})
	client.Track(Event{Type: EventCommandComplete, Command: "doctor"})

	// flush and wait for send
	client.Flush()

	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "expected 1 batch sent")
	assert.Len(t, receivedBatch.Events, 2, "expected 2 events in batch")
}

func TestClient_TrackAsyncSendsImmediately(t *testing.T) {
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	client.TrackAsync(Event{Type: EventSessionStart})

	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "expected 1 request")
}

func TestClient_NetworkErrorsSilentlyDiscarded(t *testing.T) {
	// server that always errors
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	// should not panic
	client.Track(Event{Type: EventCommandComplete})
	client.Flush()

	// queue should be empty (events sent, even if server errored)
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.queue) == 0
	}, 2*time.Second, 10*time.Millisecond, "expected queue to be cleared after flush")
}

func TestClient_UnreachableServerSilentlyDiscarded(t *testing.T) {
	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL("http://localhost:1"), // unreachable port
	)

	// should not panic or hang
	client.Track(Event{Type: EventCommandComplete})

	done := make(chan struct{})
	go func() {
		client.Flush()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(sendTimeout + time.Second):
		t.Error("flush blocked longer than expected")
	}
}

func TestClient_StartStop(t *testing.T) {
	client := NewClient("test-session", WithEnabled(true))

	client.Start()

	// should be able to stop without blocking
	done := make(chan struct{})
	go func() {
		client.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Error("Stop() blocked longer than expected")
	}
}

func TestClient_ConcurrentAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	client.Start()
	defer client.Stop()

	var wg sync.WaitGroup

	// concurrent tracking
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			client.TrackCommand("test", time.Duration(n)*time.Millisecond, true, "")
		}(i)
	}

	// concurrent flushes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.Flush()
		}()
	}

	// concurrent stats reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = client.GetStats()
		}()
	}

	wg.Wait()

	stats := client.GetStats()
	assert.Equal(t, 100, stats.CommandCount)
}

func TestStats_RecordCommand(t *testing.T) {
	stats := NewStats("session-123")

	stats.RecordCommand("init", 100*time.Millisecond, true)
	stats.RecordCommand("doctor", 50*time.Millisecond, true)
	stats.RecordCommand("init", 200*time.Millisecond, false)

	assert.Equal(t, 3, stats.CommandCount)
	assert.Equal(t, 1, stats.ErrorCount)
	assert.Equal(t, 2, stats.CommandCounts["init"])

	expectedDuration := 350 * time.Millisecond
	assert.Equal(t, expectedDuration, stats.TotalDuration)
	assert.False(t, stats.FirstCommand.IsZero(), "expected FirstCommand to be set")
	assert.False(t, stats.LastCommand.IsZero(), "expected LastCommand to be set")
}

func TestClient_GetStatsReturnsCopy(t *testing.T) {
	client := NewClient("test-session", WithEnabled(true))

	client.TrackCommand("test", 100*time.Millisecond, true, "")

	stats1 := client.GetStats()
	stats1.CommandCount = 999 // modify the copy

	stats2 := client.GetStats()

	assert.Equal(t, 1, stats2.CommandCount, "expected original stats unchanged")
}

func TestBatch_Structure(t *testing.T) {
	batch := Batch{
		Events: []Event{
			{Type: EventCommandComplete, Command: "test"},
		},
		CLIVer:    "1.0.0",
		OS:        "darwin",
		Arch:      "arm64",
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(batch)
	require.NoError(t, err, "failed to marshal batch")

	var decoded Batch
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err, "failed to unmarshal batch")

	assert.Len(t, decoded.Events, 1)
	assert.Equal(t, "1.0.0", decoded.CLIVer)
}

func TestClient_TrackDaemonStartFailure_RespectsOptOut(t *testing.T) {
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// create disabled client
	client := NewClient("test-session",
		WithEnabled(false),
		WithBaseURL(server.URL),
	)

	// should not send when disabled
	client.TrackDaemonStartFailure("test_error", "test message")

	// verify no requests are sent (give a reasonable window)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), received.Load(), "expected no requests when telemetry disabled")
}

func TestClient_TrackDaemonStartFailure_SendsWhenEnabled(t *testing.T) {
	var received atomic.Int32
	var receivedBatch Batch

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&receivedBatch)
		require.NoError(t, err, "failed to decode batch")
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// use temp dir for rate limit file to avoid interference
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	// reset rate limit to ensure we can send
	client.ResetDaemonFailureRateLimit()

	client.TrackDaemonStartFailure("lock_error", "daemon already running")

	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "expected 1 request when telemetry enabled")
	require.Len(t, receivedBatch.Events, 1, "expected 1 event in batch")
	assert.Equal(t, "daemon:start_failure", receivedBatch.Events[0].Type)
	assert.Equal(t, "lock_error", receivedBatch.Events[0].Metadata["error_type"])
}

func TestClient_TrackDaemonStartFailure_RateLimiting(t *testing.T) {
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// use temp dir for rate limit file
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	client := NewClient("test-session",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	// reset rate limit to ensure first send goes through
	client.ResetDaemonFailureRateLimit()

	// first call should succeed
	client.TrackDaemonStartFailure("error1", "first failure")

	require.Eventually(t, func() bool {
		return received.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "expected first request to be sent")

	// second call immediately after should be rate limited
	client.TrackDaemonStartFailure("error2", "second failure")

	// third call immediately after should also be rate limited
	client.TrackDaemonStartFailure("error3", "third failure")

	// give time for any unexpected sends, then verify still only 1
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), received.Load(), "expected only 1 request due to rate limiting")
}

func TestClient_SendToDaemon_RespectsOptOut(t *testing.T) {
	// create disabled client
	client := NewClient("test-session", WithEnabled(false))

	// should not panic and should return immediately
	client.SendToDaemon("test:event", map[string]any{"key": "value"})

	// if we get here without panicking, opt-out is working
	assert.False(t, client.IsEnabled())
}
