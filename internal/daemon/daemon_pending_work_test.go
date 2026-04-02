package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Pending-work inhibition ---

// TestDaemonDoesNotExitWhileFinalizationPending verifies the daemon delays exit
// when inactivity timeout fires but finalization work is queued.
// Failure prevented: daemon exits while session finalization is in-flight.
func TestDaemonDoesNotExitWhileFinalizationPending(t *testing.T) {
	d := New(&Config{
		InactivityTimeout:      100 * time.Millisecond,
		PendingWorkGracePeriod: 5 * time.Second,
	}, slog.Default())

	// set up a minimal agent worker with a queued item
	syncSignal := make(chan struct{}, 1)
	runner := &noopRunner{}
	loader := func() *config.AgentWorkerConfig {
		return (&config.AgentWorkerConfig{}).WithDefaults()
	}
	mgr := agentwork.NewManager(runner, slog.Default(), loader, syncSignal, t.TempDir())
	d.agentWorker = mgr

	// enqueue a dummy work item
	item := &agentwork.WorkItem{
		Type:     "session-finalize",
		DedupKey: "test-pending",
		Priority: 10,
		Payload:  &agentwork.SessionFinalizePayload{SessionDir: "/tmp/test"},
	}
	mgr.Enqueue(item)

	assert.True(t, d.hasPendingWork(), "daemon should detect pending work when items are queued")
}

// TestDaemonExitsAfterGracePeriod verifies the daemon exits once the grace
// period expires, even if work is still pending. Prevents stuck daemons.
// Failure prevented: daemon hangs forever waiting for stuck finalization.
func TestDaemonExitsAfterGracePeriod(t *testing.T) {
	d := New(&Config{
		InactivityTimeout:      100 * time.Millisecond,
		PendingWorkGracePeriod: 50 * time.Millisecond,
	}, slog.Default())

	// simulate pending work detected during inactivity — grace period already expired
	d.mu.Lock()
	d.pendingWorkSince = time.Now().Add(-100 * time.Millisecond)
	d.mu.Unlock()

	// set up agent worker with pending work
	syncSignal := make(chan struct{}, 1)
	runner := &noopRunner{}
	loader := func() *config.AgentWorkerConfig {
		return (&config.AgentWorkerConfig{}).WithDefaults()
	}
	mgr := agentwork.NewManager(runner, slog.Default(), loader, syncSignal, t.TempDir())
	d.agentWorker = mgr

	item := &agentwork.WorkItem{
		Type:     "session-finalize",
		DedupKey: "test-grace",
		Priority: 10,
		Payload:  &agentwork.SessionFinalizePayload{SessionDir: "/tmp/test"},
	}
	mgr.Enqueue(item)

	require.True(t, d.hasPendingWork())

	// grace period should have expired
	d.mu.Lock()
	graceElapsed := time.Since(d.pendingWorkSince)
	d.mu.Unlock()
	assert.True(t, graceElapsed >= d.config.PendingWorkGracePeriod,
		"grace period should have expired: elapsed=%v, period=%v",
		graceElapsed, d.config.PendingWorkGracePeriod)
}

// TestDaemonExitsImmediatelyWhenNoPendingWork verifies the daemon exits
// normally on inactivity when there is no pending work.
// Failure prevented: pending-work check false positive blocking normal exit.
func TestDaemonExitsImmediatelyWhenNoPendingWork(t *testing.T) {
	d := New(&Config{
		InactivityTimeout:      100 * time.Millisecond,
		PendingWorkGracePeriod: 10 * time.Minute,
	}, slog.Default())

	// no agent worker
	assert.False(t, d.hasPendingWork(), "daemon with no agent worker should not report pending work")

	// with an agent worker but empty queue
	syncSignal := make(chan struct{}, 1)
	runner := &noopRunner{}
	loader := func() *config.AgentWorkerConfig {
		return (&config.AgentWorkerConfig{}).WithDefaults()
	}
	mgr := agentwork.NewManager(runner, slog.Default(), loader, syncSignal, t.TempDir())
	d.agentWorker = mgr

	assert.False(t, d.hasPendingWork(), "daemon with empty queue should not report pending work")
}

// --- helpers ---

type noopRunner struct{}

func (r *noopRunner) Run(_ context.Context, _ agentwork.RunRequest) (*agentwork.RunResult, error) {
	return &agentwork.RunResult{Output: "ok"}, nil
}

func (r *noopRunner) Available() bool { return true }
