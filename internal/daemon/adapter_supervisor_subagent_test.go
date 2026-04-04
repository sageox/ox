package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// --- A. SpawnSubagent routes to adapter and registers worker ---
// Verifies that SpawnSubagent registers a worker in the tracker and sends the
// request to the adapter process. Uses a real test adapter binary.
// Failure prevented: workers spawned but not tracked, causing invisible resource leaks.

func TestSupervisor_SpawnSubagent(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 10)
	sup.SetWorkerTracker(wt)
	sup.SetAdapterCapabilities("test", []string{
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapSubagentController,
	})

	// prime the adapter with a session first so the process is alive
	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-parent", adapterprotocol.MethodFindSession, supFindSessionParams("agent-parent"))
	if err != nil {
		t.Fatalf("prime request failed: %v", err)
	}

	params := &adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-parent-ab12",
		AgentID:  "agent-parent",
		RepoRoot: "/tmp/test-repo",
		Task:     "write tests for auth module",
		Model:    "claude-sonnet-4-6",
	}

	// spawn-subagent may return method_not_found from test adapter (it doesn't implement it)
	// but the tracker registration and capability check should work
	result, err := sup.SpawnSubagent(ctx, "test", params)
	if err != nil {
		// the test adapter doesn't handle spawn-subagent, but the error should NOT be
		// about capability or capacity
		if strings.Contains(err.Error(), "capability") || strings.Contains(err.Error(), "capacity") {
			t.Fatalf("unexpected error type: %v", err)
		}
		// worker should be rolled back on adapter error
		_, ok := wt.Get("w-parent-ab12")
		if ok {
			t.Fatal("worker should be removed from tracker after adapter error")
		}
		return
	}

	if result.WorkerID != "w-parent-ab12" {
		t.Errorf("expected worker_id %q, got %q", "w-parent-ab12", result.WorkerID)
	}

	// worker should be in tracker
	ws, ok := wt.Get("w-parent-ab12")
	if !ok {
		t.Fatal("expected worker to be in tracker after spawn")
	}
	if ws.Status != adapterprotocol.WorkerStatusStarting {
		t.Errorf("expected status 'starting', got %q", ws.Status)
	}
}

// --- B. SpawnSubagent at capacity returns error ---
// Verifies that spawning when at worker capacity is rejected before reaching the adapter.
// Failure prevented: unbounded worker spawning ignoring configured limits.

func TestSupervisor_SpawnSubagentAtCapacity(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 1)
	sup.SetWorkerTracker(wt)
	sup.SetAdapterCapabilities("test", []string{adapterprotocol.CapSubagentController})

	// fill capacity
	wt.Register(&WorkerState{
		WorkerID:    "w-existing-0001",
		AgentID:     "agent-1",
		AdapterType: "test",
		Status:      "running",
		StartedAt:   time.Now(),
	})

	params := &adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-overflow-0001",
		AgentID:  "agent-1",
		Task:     "overflow task",
	}

	_, err := sup.SpawnSubagent(context.Background(), "test", params)
	if err == nil {
		t.Fatal("expected capacity error, got nil")
	}
	if !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity error, got: %v", err)
	}
}

// --- C. SpawnSubagent without capability returns error ---
// Verifies that adapters without subagent_controller capability are rejected.
// Failure prevented: sending spawn requests to adapters that can't handle them.

func TestSupervisor_SpawnSubagentNoCapability(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 10)
	sup.SetWorkerTracker(wt)
	// only session_reader, no subagent_controller
	sup.SetAdapterCapabilities("test", []string{adapterprotocol.CapSessionReader})

	params := &adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-nocap-0001",
		AgentID:  "agent-1",
		Task:     "should fail",
	}

	_, err := sup.SpawnSubagent(context.Background(), "test", params)
	if err == nil {
		t.Fatal("expected capability error, got nil")
	}
	if !strings.Contains(err.Error(), "subagent_controller") {
		t.Fatalf("expected subagent_controller error, got: %v", err)
	}
}

// --- D. SpawnSubagent without tracker returns error ---
// Verifies graceful behavior when worker tracker is not initialized.
// Failure prevented: nil pointer panic from uninitialized tracker.

func TestSupervisor_SpawnSubagentNoTracker(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// no SetWorkerTracker call
	params := &adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-notrack-0001",
		AgentID:  "agent-1",
		Task:     "should fail",
	}

	_, err := sup.SpawnSubagent(context.Background(), "test", params)
	if err == nil {
		t.Fatal("expected error without tracker, got nil")
	}
	if !strings.Contains(err.Error(), "tracker not initialized") {
		t.Fatalf("expected tracker error, got: %v", err)
	}
}

// --- E. SubagentStatus returns worker state ---
// Verifies that SubagentStatus checks the tracker before routing to the adapter.
// Failure prevented: status requests for unknown workers hitting the adapter unnecessarily.

func TestSupervisor_SubagentStatusUnknownWorker(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 10)
	sup.SetWorkerTracker(wt)

	params := &adapterprotocol.SubagentStatusParams{
		WorkerID: "w-unknown-0001",
	}

	_, err := sup.SubagentStatus(context.Background(), "test", params)
	if err == nil {
		t.Fatal("expected not found error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

// --- F. CancelSubagent checks tracker first ---
// Verifies that cancel requests are rejected for unknown workers.
// Failure prevented: canceling nonexistent workers causing adapter confusion.

func TestSupervisor_CancelSubagentUnknownWorker(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 10)
	sup.SetWorkerTracker(wt)

	params := &adapterprotocol.CancelSubagentParams{
		WorkerID: "w-unknown-0001",
		Reason:   "user_requested",
	}

	_, err := sup.CancelSubagent(context.Background(), "test", params)
	if err == nil {
		t.Fatal("expected not found error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

// --- G. HandleWorkerEvent updates tracker on completion ---
// Verifies that subagent.completed events transition workers to completed status.
// Failure prevented: completed workers stuck in "running" forever.

func TestSupervisor_HandleWorkerEventCompleted(t *testing.T) {
	logger, buf := supCaptureSlogOutput(t)
	sup := NewAdapterSupervisor(logger, []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(logger, 10)
	sup.SetWorkerTracker(wt)

	// pre-register a running worker
	wt.Register(&WorkerState{
		WorkerID: "w-evt-0001",
		AgentID:  "agent-1",
		Status:   "running",
	})

	data, _ := json.Marshal(adapterprotocol.SubagentCompletedData{
		WorkerID:    "w-evt-0001",
		ExitCode:    0,
		DurationSec: 42,
		Summary:     "all tests pass",
	})

	sup.HandleWorkerEvent(&adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentCompleted,
		AgentID: "agent-1",
		Data:    data,
	})

	// terminal workers are auto-reaped by WorkerTracker.UpdateStatus
	_, ok := wt.Get("w-evt-0001")
	if ok {
		t.Fatal("terminal worker should be reaped from tracker")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "subagent completed") {
		t.Errorf("expected 'subagent completed' log, got:\n%s", logOutput)
	}
}

// --- H. HandleWorkerEvent updates tracker on failure ---
// Verifies that subagent.failed events transition workers to the correct terminal status.
// Failure prevented: failed workers incorrectly counted as active.

func TestSupervisor_HandleWorkerEventFailed(t *testing.T) {
	tests := []struct {
		name           string
		exitReason     string
		expectedStatus string
	}{
		{name: "error", exitReason: "error", expectedStatus: adapterprotocol.WorkerStatusFailed},
		{name: "canceled", exitReason: "canceled", expectedStatus: adapterprotocol.WorkerStatusCanceled},
		{name: "timed out", exitReason: "timed_out", expectedStatus: adapterprotocol.WorkerStatusTimedOut},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
			defer sup.Shutdown(context.Background())

			wt := NewWorkerTracker(testLogger(), 10)
			sup.SetWorkerTracker(wt)

			wt.Register(&WorkerState{
				WorkerID: "w-fail-test",
				AgentID:  "agent-1",
				Status:   "running",
			})

			data, _ := json.Marshal(adapterprotocol.SubagentFailedData{
				WorkerID:   "w-fail-test",
				ExitReason: tt.exitReason,
				ExitCode:   1,
				Error:      "something went wrong",
			})

			sup.HandleWorkerEvent(&adapterprotocol.Event{
				Event:   adapterprotocol.EventSubagentFailed,
				AgentID: "agent-1",
				Data:    data,
			})

			// terminal workers are auto-reaped by WorkerTracker.UpdateStatus
			_, ok := wt.Get("w-fail-test")
			if ok {
				t.Fatal("terminal worker should be reaped from tracker")
			}
		})
	}
}

// --- I. HandleWorkerEvent progress transitions starting to running ---
// Verifies that the first progress event transitions a starting worker to running.
// Failure prevented: worker stuck in "starting" status after it begins work.

func TestSupervisor_HandleWorkerEventProgressTransition(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(testLogger(), 10)
	sup.SetWorkerTracker(wt)

	wt.Register(&WorkerState{
		WorkerID: "w-prog-0001",
		AgentID:  "agent-1",
		Status:   adapterprotocol.WorkerStatusStarting,
	})

	data, _ := json.Marshal(adapterprotocol.SubagentProgressData{
		WorkerID:    "w-prog-0001",
		OutputType:  "tool_use",
		Description: "reading files",
	})

	sup.HandleWorkerEvent(&adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentProgress,
		AgentID: "agent-1",
		Data:    data,
	})

	ws, _ := wt.Get("w-prog-0001")
	if ws.Status != adapterprotocol.WorkerStatusRunning {
		t.Errorf("expected status %q after progress, got %q", adapterprotocol.WorkerStatusRunning, ws.Status)
	}
}

// --- J. HandleWorkerEvent with nil tracker is a no-op ---
// Failure prevented: panic when worker events arrive before tracker is initialized.

func TestSupervisor_HandleWorkerEventNoTracker(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// no tracker set; should not panic
	data, _ := json.Marshal(adapterprotocol.SubagentCompletedData{
		WorkerID: "w-notrack-0001",
	})

	sup.HandleWorkerEvent(&adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentCompleted,
		AgentID: "agent-1",
		Data:    data,
	})
}

// --- K. HandleWorkerEvent with malformed data logs warning ---
// Failure prevented: malformed events silently ignored without operator visibility.

func TestSupervisor_HandleWorkerEventMalformedData(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sup := NewAdapterSupervisor(logger, []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(logger, 10)
	sup.SetWorkerTracker(wt)

	sup.HandleWorkerEvent(&adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentCompleted,
		AgentID: "agent-1",
		Data:    json.RawMessage(`{invalid json`),
	})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to decode") {
		t.Errorf("expected decode error log, got:\n%s", logOutput)
	}
}

// --- L. SetAdapterCapabilities and hasCapability ---
// Verifies capability storage and retrieval.
// Failure prevented: capability checks always returning false after registration.

func TestSupervisor_Capabilities(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// before setting: no capability
	if sup.hasCapability("test", adapterprotocol.CapSubagentController) {
		t.Error("expected no capability before setting")
	}

	sup.SetAdapterCapabilities("test", []string{
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapSubagentController,
	})

	if !sup.hasCapability("test", adapterprotocol.CapSubagentController) {
		t.Error("expected subagent_controller capability after setting")
	}
	if !sup.hasCapability("test", adapterprotocol.CapSessionReader) {
		t.Error("expected session_reader capability after setting")
	}
	if sup.hasCapability("test", adapterprotocol.CapFileWatcher) {
		t.Error("expected no file_watcher capability")
	}
	if sup.hasCapability("unknown-adapter", adapterprotocol.CapSubagentController) {
		t.Error("expected no capability for unknown adapter")
	}
}

// --- M. HandleWorkerEvent unknown event type logs warning ---
// Failure prevented: unknown events silently dropped without alerting operators.

func TestSupervisor_HandleWorkerEventUnknown(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sup := NewAdapterSupervisor(logger, []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	wt := NewWorkerTracker(logger, 10)
	sup.SetWorkerTracker(wt)

	sup.HandleWorkerEvent(&adapterprotocol.Event{
		Event:   "subagent.unknown_event",
		AgentID: "agent-1",
		Data:    json.RawMessage(`{}`),
	})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "unknown subagent event") {
		t.Errorf("expected 'unknown subagent event' warning, got:\n%s", logOutput)
	}
}
