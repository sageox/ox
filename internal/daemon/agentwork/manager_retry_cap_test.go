package agentwork

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runDetectCycle simulates one doctor tick: detection (cooldown bypassed) and
// synchronous execution of everything it queued.
func runDetectCycle(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	m.lastDetect = make(map[string]time.Time)
	m.mu.Unlock()
	m.detectAndEnqueue(m.configLoader())
	for item := m.queue.Dequeue(); item != nil; item = m.queue.Dequeue() {
		m.executeItem(context.Background(), item)
	}
}

func newRetryCapManager(t *testing.T, h *mockHandler) *Manager {
	t.Helper()
	m := NewManager(NewMockRunner(true), nil, func() *config.AgentWorkerConfig {
		return enabledConfigWith(1, 1000)
	}, make(chan struct{}, 1), t.TempDir(), "")
	m.RegisterHandler(h)
	return m
}

func detectOneSession(h *mockHandler) {
	// detect hands out a fresh item each scan, like the real handler
	h.detectItems = []*WorkItem{{Type: "session-finalize", DedupKey: "session-finalize:s1"}}
}

func TestManager_ProcessResultFailuresAreCappedAcrossScans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scans     int
		processFn error
		wantCalls int
	}{
		{name: "fails every time, parked after cap", scans: 10, processFn: errors.New("upload failed"), wantCalls: maxRetries},
		{name: "fewer scans than cap keeps retrying", scans: 2, processFn: errors.New("upload failed"), wantCalls: 2},
		{name: "success is never parked", scans: 10, processFn: nil, wantCalls: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &mockHandler{typ: "session-finalize", processErr: tt.processFn}
			m := newRetryCapManager(t, h)

			for i := 0; i < tt.scans; i++ {
				detectOneSession(h)
				runDetectCycle(t, m)
			}

			require.Equal(t, tt.wantCalls, int(h.processCalls.Load()))
		})
	}
}

func TestManager_ParkedItemResumesAfterBackoff(t *testing.T) {
	t.Parallel()

	h := &mockHandler{typ: "session-finalize", processErr: errors.New("upload failed")}
	m := newRetryCapManager(t, h)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }

	for i := 0; i < maxRetries+2; i++ {
		detectOneSession(h)
		runDetectCycle(t, m)
	}
	require.Equal(t, maxRetries, int(h.processCalls.Load()), "parked after cap")

	// still inside the backoff window: nothing runs
	clock = clock.Add(parkBackoffBase / 2)
	detectOneSession(h)
	runDetectCycle(t, m)
	require.Equal(t, maxRetries, int(h.processCalls.Load()))

	// window elapsed and the underlying condition is fixed: work resumes and clears the park
	clock = clock.Add(parkBackoffMax)
	h.processErr = nil
	detectOneSession(h)
	runDetectCycle(t, m)
	require.Equal(t, maxRetries+1, int(h.processCalls.Load()))

	detectOneSession(h)
	runDetectCycle(t, m)
	assert.Equal(t, maxRetries+2, int(h.processCalls.Load()), "success cleared the park state")
}

func TestManager_ParkBackoffGrowsAndCaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: maxRetries, want: parkBackoffBase},
		{failures: maxRetries + 1, want: 2 * parkBackoffBase},
		{failures: maxRetries + 2, want: 4 * parkBackoffBase},
		{failures: maxRetries + 40, want: parkBackoffMax},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parkBackoff(tt.failures), "failures=%d", tt.failures)
	}
}

func TestManager_ForceDetectClearsParkedItems(t *testing.T) {
	t.Parallel()

	h := &mockHandler{typ: "session-finalize", processErr: errors.New("upload failed")}
	m := newRetryCapManager(t, h)
	for i := 0; i < maxRetries+1; i++ {
		detectOneSession(h)
		runDetectCycle(t, m)
	}
	require.Equal(t, maxRetries, int(h.processCalls.Load()))

	detectOneSession(h)
	assert.Equal(t, 1, m.ForceDetect(), "an explicit operator retry bypasses the park")
}

func TestManager_LedgerUnresolvedPausesTypeWithoutRunningHandler(t *testing.T) {
	t.Parallel()

	h := &mockHandler{typ: "session-finalize", processErr: ErrLedgerUnresolved}
	m := newRetryCapManager(t, h)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }

	// two sessions detected; the first failure pauses the whole type
	h.detectItems = []*WorkItem{
		{Type: "session-finalize", DedupKey: "session-finalize:a"},
		{Type: "session-finalize", DedupKey: "session-finalize:b"},
	}
	runDetectCycle(t, m)
	require.Equal(t, 1, int(h.processCalls.Load()), "second queued item skipped once the type is paused")

	for i := 0; i < 5; i++ {
		clock = clock.Add(30 * time.Second)
		runDetectCycle(t, m)
	}
	require.Equal(t, 1, int(h.processCalls.Load()), "paused: nothing is detected or run")

	// operator fixed the ledger; pause window elapsed
	clock = clock.Add(parkBackoffMax)
	h.processErr = nil
	runDetectCycle(t, m)
	assert.Equal(t, 3, int(h.processCalls.Load()), "both sessions resume")
}
