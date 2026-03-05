package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTelemetryCollector_RespectsOptOut_DoNotTrack(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	assert.False(t, collector.IsEnabled(), "expected telemetry disabled with DO_NOT_TRACK=1")
}

func TestNewTelemetryCollector_RespectsOptOut_SageOxTelemetry(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "false")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	assert.False(t, collector.IsEnabled(), "expected telemetry disabled with SAGEOX_TELEMETRY=false")
}

func TestNewTelemetryCollector_EnabledByDefault(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	assert.True(t, collector.IsEnabled(), "expected telemetry enabled by default")
}

func TestTelemetryCollector_Record_WhenDisabled(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// should not panic
	collector.Record("test:event", map[string]any{"key": "value"})

	stats := collector.Stats()
	assert.Equal(t, 0, stats.BufferCount, "expected no events buffered when disabled")
}

func TestTelemetryCollector_Record_WhenEnabled(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.Record("test:event", map[string]any{"key": "value"})

	stats := collector.Stats()
	assert.Equal(t, 1, stats.BufferCount, "expected 1 event buffered")
}

func TestTelemetryCollector_RingBuffer_Overflow(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// fill beyond buffer size (default 500)
	// note: the collector may auto-flush at threshold (50), so we just verify
	// the count never exceeds the buffer size
	for i := 0; i < telemetryBufferSize+100; i++ {
		collector.mu.Lock()
		collector.buffer[collector.head] = TelemetryEvent{Event: "test"}
		collector.head = (collector.head + 1) % collector.bufferSize
		if collector.count < collector.bufferSize {
			collector.count++
		}
		collector.mu.Unlock()
	}

	stats := collector.Stats()
	assert.Equal(t, telemetryBufferSize, stats.BufferCount, "expected buffer to be capped at max size")
}

func TestTelemetryCollector_RecordFromIPC(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// simulate IPC event from CLI
	collector.RecordFromIPC("command:complete", map[string]any{
		"app_type":    "ox",
		"app_version": "0.2.0",
		"command":     "doctor",
	})

	stats := collector.Stats()
	assert.Equal(t, 1, stats.BufferCount, "expected 1 event from IPC")
}

func TestTelemetryCollector_DrainBuffer_PreservesOrder(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// add events directly to avoid auto-flush
	for i := 0; i < 5; i++ {
		collector.mu.Lock()
		collector.buffer[collector.head] = TelemetryEvent{
			Event: "test:event",
			Props: map[string]any{"index": i},
		}
		collector.head = (collector.head + 1) % collector.bufferSize
		collector.count++
		collector.mu.Unlock()
	}

	// drain buffer
	events := collector.drainBuffer()

	require.Len(t, events, 5)

	// verify order (oldest first)
	for i, e := range events {
		idx, ok := e.Props["index"].(int)
		require.True(t, ok, "expected index to be an int")
		assert.Equal(t, i, idx, "events should be in chronological order")
	}

	// verify buffer is empty after drain
	stats := collector.Stats()
	assert.Equal(t, 0, stats.BufferCount, "expected buffer empty after drain")
}

func TestTelemetryCollector_SendEvents_ToServer(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var received atomic.Int32
	var receivedPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)

		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("SAGEOX_TELEMETRY_ENDPOINT", server.URL)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// record some events
	collector.Record("test:event1", nil)
	collector.Record("test:event2", nil)

	// manually flush
	collector.flush()

	// wait for async send
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, int32(1), received.Load(), "expected 1 batch sent")
	assert.NotEmpty(t, receivedPayload["clientid"], "expected client ID in payload")

	events, ok := receivedPayload["events"].([]any)
	require.True(t, ok, "expected events array in payload")
	assert.Len(t, events, 2, "expected 2 events in batch")
}

func TestTelemetryCollector_RecordDaemonStartup(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.RecordDaemonStartup()

	stats := collector.Stats()
	assert.Equal(t, 1, stats.BufferCount)

	events := collector.drainBuffer()
	require.Len(t, events, 1)
	assert.Equal(t, "app:startup", events[0].Event)
	assert.Equal(t, "ox-daemon", events[0].Props["app_type"])
}

func TestTelemetryCollector_RecordDaemonShutdown(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.RecordDaemonShutdown(5*time.Minute, "graceful")

	events := collector.drainBuffer()
	require.Len(t, events, 1)
	assert.Equal(t, "app:shutdown", events[0].Event)
	assert.Equal(t, "ox-daemon", events[0].Props["app_type"])
	assert.Equal(t, int64(300), events[0].Props["uptime_seconds"])
	assert.Equal(t, "graceful", events[0].Props["reason"])
}

func TestTelemetryCollector_RecordSyncComplete(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.RecordSyncComplete("ledger", "push", "success", 150*time.Millisecond, 5)

	events := collector.drainBuffer()
	require.Len(t, events, 1)
	assert.Equal(t, "sync:complete", events[0].Event)
	assert.Equal(t, "ledger", events[0].Props["sync_type"])
	assert.Equal(t, "push", events[0].Props["operation"])
	assert.Equal(t, "success", events[0].Props["status"])
	assert.Equal(t, int64(150), events[0].Props["duration_ms"])
}

func TestTelemetryCollector_Stats(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.Record("test:event", nil)

	stats := collector.Stats()

	assert.True(t, stats.Enabled)
	assert.Equal(t, 1, stats.BufferCount)
	assert.Equal(t, telemetryBufferSize, stats.BufferSize)
	assert.Equal(t, telemetryDefaultInterval, stats.SendInterval)
	assert.NotEmpty(t, stats.ClientID)
}

func TestGetOrCreateClientID_Persistence(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	t.Setenv("SAGEOX_CLIENT_ID", "") // ensure env var doesn't interfere

	// first call should create ID
	id1 := getOrCreateClientID()
	assert.NotEmpty(t, id1, "expected client ID to be generated")

	// second call should return same ID
	id2 := getOrCreateClientID()
	assert.Equal(t, id1, id2, "expected same client ID on subsequent calls")
}

func TestGetOrCreateClientID_EnvOverride(t *testing.T) {
	customID := "custom-client-id-from-env"
	t.Setenv("SAGEOX_CLIENT_ID", customID)

	id := getOrCreateClientID()
	assert.Equal(t, customID, id, "expected env var to override stored ID")
}

func TestIsTelemetryEnabled_Precedence(t *testing.T) {
	tests := []struct {
		name            string
		doNotTrack      string
		sageoxTelemetry string
		expected        bool
	}{
		{
			name:            "both empty - enabled",
			doNotTrack:      "",
			sageoxTelemetry: "",
			expected:        true,
		},
		{
			name:            "DO_NOT_TRACK=1 - disabled",
			doNotTrack:      "1",
			sageoxTelemetry: "",
			expected:        false,
		},
		{
			name:            "SAGEOX_TELEMETRY=false - disabled",
			doNotTrack:      "",
			sageoxTelemetry: "false",
			expected:        false,
		},
		{
			name:            "both set - disabled (DO_NOT_TRACK wins)",
			doNotTrack:      "1",
			sageoxTelemetry: "true",
			expected:        false,
		},
		{
			name:            "DO_NOT_TRACK=0 - enabled",
			doNotTrack:      "0",
			sageoxTelemetry: "",
			expected:        true,
		},
		{
			name:            "SAGEOX_TELEMETRY=TRUE (case) - enabled",
			doNotTrack:      "",
			sageoxTelemetry: "TRUE",
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// use temp dir to avoid loading actual user config
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("DO_NOT_TRACK", tt.doNotTrack)
			t.Setenv("SAGEOX_TELEMETRY", tt.sageoxTelemetry)

			result := isTelemetryEnabled()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short ASCII string",
			input:    "short",
			maxLen:   10,
			expected: "short",
		},
		{
			name:     "exact length ASCII",
			input:    "exactly ten",
			maxLen:   11,
			expected: "exactly ten",
		},
		{
			name:     "long ASCII string truncated",
			input:    "this is a very long message that should be truncated",
			maxLen:   20,
			expected: "this is a very lo...",
		},
		{
			name:     "emojis - no truncation needed",
			input:    "hello world",
			maxLen:   20,
			expected: "hello world",
		},
		{
			name:     "emojis - truncation preserves valid UTF-8",
			input:    "error with emojis: crash crash crash",
			maxLen:   10,
			expected: "error w...",
		},
		{
			name:     "multi-byte characters truncated correctly",
			input:    "Japanese: Japanese test characters here",
			maxLen:   15,
			expected: "Japanese: Ja...",
		},
		{
			name:     "maxLen <= 3 returns just ellipsis",
			input:    "anything here",
			maxLen:   3,
			expected: "...",
		},
		{
			name:     "maxLen = 1 returns just ellipsis",
			input:    "test",
			maxLen:   1,
			expected: "...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateMessage(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)

			// verify result is valid UTF-8
			assert.True(t, utf8.ValidString(result), "result should be valid UTF-8: %q", result)

			// verify rune count constraint (except for maxLen <= 3 case which returns "...")
			if tt.maxLen > 3 {
				runeCount := utf8.RuneCountInString(result)
				assert.LessOrEqual(t, runeCount, tt.maxLen, "rune count should not exceed maxLen")
			}
		})
	}
}

func TestTruncateMessage_UTF8Validity(t *testing.T) {
	// test that truncation at various lengths never produces invalid UTF-8
	testStrings := []string{
		"Hello crash world rocket fire",
		"Mixed: test characters here more",
		"All emojis: crash crash crash crash crash",
		"Normal ASCII text without any special characters",
	}

	for _, s := range testStrings {
		for maxLen := 1; maxLen <= len([]rune(s))+5; maxLen++ {
			result := truncateMessage(s, maxLen)
			assert.True(t, utf8.ValidString(result),
				"truncateMessage(%q, %d) produced invalid UTF-8: %q", s, maxLen, result)
		}
	}
}

func TestTelemetryCollector_Stop_DoubleStop(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.Start()

	// first stop should work
	collector.Stop()

	// second stop should not panic (sync.Once protection)
	assert.NotPanics(t, func() {
		collector.Stop()
	}, "second Stop() call should not panic")

	// third stop should also not panic
	assert.NotPanics(t, func() {
		collector.Stop()
	}, "third Stop() call should not panic")
}

func TestTelemetryCollector_Stop_Concurrent(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.Start()

	// concurrent stops should not panic
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			collector.Stop()
		}()
	}

	assert.NotPanics(t, func() {
		wg.Wait()
	}, "concurrent Stop() calls should not panic")
}

// TestTelemetryCollector_FlushCooldown_PreventsThunderingHerd verifies that rapid
// event recording does not trigger unbounded HTTP requests. Before this fix,
// every Record() call above the batch threshold (50) spawned a new flush goroutine
// with no cooldown, creating unbounded HTTP POSTs under rapid input.
func TestTelemetryCollector_FlushCooldown_PreventsThunderingHerd(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("SAGEOX_TELEMETRY_ENDPOINT", server.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	// rapidly record 200 events (well above batch threshold of 50)
	for i := range 200 {
		collector.Record("test:rapid", map[string]any{"index": i})
	}

	// allow goroutines to settle
	time.Sleep(100 * time.Millisecond)

	// with cooldown, at most 1 early flush should have fired
	count := requestCount.Load()
	if count > 1 {
		t.Errorf("requestCount = %d, want <= 1 (cooldown should prevent thundering herd)", count)
	}
}

func TestTelemetryCollector_RecordDuringShutdown(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := NewTelemetryCollector(logger)

	collector.Start()

	// start concurrent recording
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				collector.Record("test:event", map[string]any{"index": j})
			}
		}()
	}

	// stop while recording is happening
	time.Sleep(10 * time.Millisecond)
	collector.Stop()

	// wait for all recorders to finish
	assert.NotPanics(t, func() {
		wg.Wait()
	}, "recording during shutdown should not panic")
}
