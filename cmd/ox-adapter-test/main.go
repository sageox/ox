// ox-adapter-test is a test adapter for validating ox's adapter protocol
// integration without requiring a real coding agent.
//
// Behavior is controlled via environment variables:
//   - OX_TEST_SESSION_FILE: path to a fake session file to serve
//   - OX_TEST_CRASH_AFTER: crash after N read-from-offset calls
//   - OX_TEST_LATENCY_MS: add latency to each call (milliseconds)
//   - OX_TEST_DIAGNOSE_ISSUES: JSON array of issues to return from diagnose
//   - OX_TEST_DETECT: "true" or "false" (default: true)
//   - OX_TEST_SPAWN_LATENCY_MS: latency before spawn-subagent returns (milliseconds)
//   - OX_TEST_SPAWN_FAIL: if set, spawn-subagent returns an error
//   - OX_TEST_WORKER_DURATION_MS: time before worker completes (default: 100ms)
//   - OX_TEST_WORKER_FAIL: if set, worker sends subagent.failed instead of completed
//
// This adapter is gated by the testadapters build tag in release builds.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "test"
	adapterDisplay = "Test Adapter"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:           handleInfo,
		Detect:         handleDetect,
		InstallHooks:   handleInstallHooks,
		CheckHooks:     handleCheckHooks,
		UninstallHooks: handleUninstallHooks,
		Read:           handleRead,
		ReadMetadata:   handleReadMetadata,
		Diagnose:       handleDiagnose,
		Serve:          handleServe,
	})
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
	addLatency()
	return &adapterprotocol.InfoResponse{
		ProtocolVersion: adapterprotocol.ProtocolVersion,
		Name:            adapterName,
		DisplayName:     adapterDisplay,
		Version:         adapterVersion,
		Type:            adapterprotocol.TypeTest,
		Capabilities: []string{
			adapterprotocol.CapSessionReader,
			adapterprotocol.CapHookInstaller,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapServeMode,
			adapterprotocol.CapSubagentController,
		},
		HookEnvValues: []string{"test"},
		ServeMode:     true,
		SubagentConfig: &adapterprotocol.SubagentConfig{
			MaxConcurrent: 2,
			Models:        []string{"test-model"},
			DefaultModel:  "test-model",
		},
	}, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	addLatency()
	detected := os.Getenv("OX_TEST_DETECT") != "false"
	return &adapterprotocol.DetectResponse{
		Detected: detected,
		Reason:   "test adapter (controlled by OX_TEST_DETECT)",
	}, nil
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	addLatency()
	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{p.RepoRoot + "/.test-adapter/hooks.json"},
		Hooks:        []string{"PostToolUse", "Stop"},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	addLatency()
	return &adapterprotocol.CheckHooksResponse{
		Installed: true,
		Scope:     p.Scope,
		HookFiles: []string{p.RepoRoot + "/.test-adapter/hooks.json"},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	addLatency()
	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{p.RepoRoot + "/.test-adapter/hooks.json"},
	}, nil
}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	addLatency()

	sessionFile := p.SessionFile
	if sessionFile == "" {
		sessionFile = os.Getenv("OX_TEST_SESSION_FILE")
	}
	if sessionFile == "" {
		return &adapterprotocol.ReadResult{
			Entries: []adapterprotocol.RawEntry{
				{Timestamp: time.Now().UTC().Format(time.RFC3339), Role: "user", Content: "test message"},
				{Timestamp: time.Now().UTC().Format(time.RFC3339), Role: "assistant", Content: "test response"},
			},
		}, nil
	}

	entries, err := readTestSessionFile(sessionFile)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.ReadResult{Entries: entries}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	addLatency()
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: adapterVersion,
		Model:        "test-model",
	}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	addLatency()

	issuesJSON := os.Getenv("OX_TEST_DIAGNOSE_ISSUES")
	if issuesJSON != "" {
		var issues []adapterprotocol.DiagnoseIssue
		if err := json.Unmarshal([]byte(issuesJSON), &issues); err != nil {
			return nil, fmt.Errorf("invalid OX_TEST_DIAGNOSE_ISSUES: %w", err)
		}
		return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
	}

	return &adapterprotocol.DiagnoseResult{OK: true, Issues: []adapterprotocol.DiagnoseIssue{}}, nil
}

var readCount atomic.Int64

// testWorker tracks the state of a simulated subagent worker.
type testWorker struct {
	workerID  string
	agentID   string
	task      string
	status    string
	startedAt time.Time
	cancel    context.CancelFunc
	mu        sync.Mutex
}

func (w *testWorker) getStatus() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *testWorker) setStatus(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = s
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*testSessionState]()
	workers := &workerStore{m: make(map[string]*testWorker)}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		addLatency()

		sessionFile := os.Getenv("OX_TEST_SESSION_FILE")
		if sessionFile == "" {
			// create a temporary session file with test data
			sessionFile = "/tmp/ox-test-adapter-session.jsonl"
		}

		store.Set(p.AgentID, &testSessionState{sessionFile: sessionFile, offset: 0})

		return &adapterprotocol.FindSessionResult{
			SessionFile: sessionFile,
			Offset:      0,
		}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		addLatency()

		// crash-after support
		if crashAfter := os.Getenv("OX_TEST_CRASH_AFTER"); crashAfter != "" {
			n, _ := strconv.ParseInt(crashAfter, 10, 64)
			if n > 0 && readCount.Add(1) >= n {
				os.Exit(1)
			}
		}

		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		if state.sessionFile == "" || state.sessionFile == "/tmp/ox-test-adapter-session.jsonl" {
			// return synthetic entries
			now := time.Now().UTC().Format(time.RFC3339)
			return &adapterprotocol.ReadFromOffsetResult{
				Entries: []adapterprotocol.RawEntry{
					{Timestamp: now, Role: "user", Content: "test read-from-offset"},
				},
				NewOffset: p.Offset + 1,
			}, nil
		}

		entries, newOffset, err := readTestFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}
		state.offset = newOffset

		return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: newOffset}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.OnSpawnSubagent(func(ctx context.Context, p adapterprotocol.SpawnSubagentParams) (*adapterprotocol.SpawnSubagentResult, error) {
		// configurable latency before responding
		if latencyStr := os.Getenv("OX_TEST_SPAWN_LATENCY_MS"); latencyStr != "" {
			if ms, err := strconv.ParseInt(latencyStr, 10, 64); err == nil && ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}

		// configurable failure
		if os.Getenv("OX_TEST_SPAWN_FAIL") != "" {
			return nil, fmt.Errorf("spawn failed: OX_TEST_SPAWN_FAIL is set")
		}

		workerCtx, workerCancel := context.WithCancel(srv.Context()) //nolint:gosec // cancel stored in worker struct, called on cancel-subagent
		w := &testWorker{
			workerID:  p.WorkerID,
			agentID:   p.AgentID,
			task:      p.Task,
			status:    adapterprotocol.WorkerStatusStarting,
			startedAt: time.Now().UTC(),
			cancel:    workerCancel,
		}
		workers.set(p.WorkerID, w)

		// simulate async worker execution
		go runTestWorker(workerCtx, w, srv.Writer())

		return &adapterprotocol.SpawnSubagentResult{
			WorkerID: p.WorkerID,
			Status:   adapterprotocol.WorkerStatusStarting,
		}, nil
	})

	srv.OnSubagentStatus(func(ctx context.Context, p adapterprotocol.SubagentStatusParams) (*adapterprotocol.SubagentStatusResult, error) {
		w, ok := workers.get(p.WorkerID)
		if !ok {
			return nil, fmt.Errorf("worker not found: %s", p.WorkerID)
		}

		elapsed := int(time.Since(w.startedAt).Seconds())
		return &adapterprotocol.SubagentStatusResult{
			WorkerID:     w.workerID,
			Status:       w.getStatus(),
			StartedAt:    w.startedAt.Format(time.RFC3339),
			ElapsedSec:   elapsed,
			LastActivity: time.Now().UTC().Format(time.RFC3339),
		}, nil
	})

	srv.OnCancelSubagent(func(ctx context.Context, p adapterprotocol.CancelSubagentParams) (*adapterprotocol.CancelSubagentResult, error) {
		w, ok := workers.get(p.WorkerID)
		if !ok {
			return nil, fmt.Errorf("worker not found: %s", p.WorkerID)
		}

		w.setStatus(adapterprotocol.WorkerStatusCanceling)
		w.cancel()

		return &adapterprotocol.CancelSubagentResult{
			WorkerID: w.workerID,
			Status:   adapterprotocol.WorkerStatusCanceling,
		}, nil
	})

	srv.Serve()
}

// runTestWorker simulates an async worker lifecycle. It sends progress events
// then either a completed or failed event depending on env configuration.
func runTestWorker(ctx context.Context, w *testWorker, writer *adapterruntime.Writer) {
	durationMS := int64(100)
	if durStr := os.Getenv("OX_TEST_WORKER_DURATION_MS"); durStr != "" {
		if ms, err := strconv.ParseInt(durStr, 10, 64); err == nil && ms > 0 {
			durationMS = ms
		}
	}

	w.setStatus(adapterprotocol.WorkerStatusRunning)

	// send a progress event partway through
	progressDelay := time.Duration(durationMS/2) * time.Millisecond
	select {
	case <-ctx.Done():
		sendWorkerFailed(writer, w, "canceled")
		return
	case <-time.After(progressDelay):
	}

	progressData, _ := json.Marshal(adapterprotocol.SubagentProgressData{
		WorkerID:    w.workerID,
		OutputType:  "message",
		Description: "working on task",
	})
	writer.PushEvent(adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentProgress,
		AgentID: w.agentID,
		Data:    progressData,
	})

	// wait for the remaining duration
	select {
	case <-ctx.Done():
		sendWorkerFailed(writer, w, "canceled")
		return
	case <-time.After(time.Duration(durationMS/2) * time.Millisecond):
	}

	if os.Getenv("OX_TEST_WORKER_FAIL") != "" {
		sendWorkerFailed(writer, w, "error")
		return
	}

	// completed
	w.setStatus(adapterprotocol.WorkerStatusCompleted)
	elapsed := int(time.Since(w.startedAt).Seconds())
	completedData, _ := json.Marshal(adapterprotocol.SubagentCompletedData{
		WorkerID:    w.workerID,
		ExitCode:    0,
		DurationSec: elapsed,
		Summary:     "test task completed",
	})
	writer.PushEvent(adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentCompleted,
		AgentID: w.agentID,
		Data:    completedData,
	})
}

func sendWorkerFailed(writer *adapterruntime.Writer, w *testWorker, reason string) {
	w.setStatus(adapterprotocol.WorkerStatusFailed)
	if reason == "canceled" {
		w.setStatus(adapterprotocol.WorkerStatusCanceled)
	}
	elapsed := int(time.Since(w.startedAt).Seconds())
	failedData, _ := json.Marshal(adapterprotocol.SubagentFailedData{
		WorkerID:    w.workerID,
		ExitReason:  reason,
		ExitCode:    1,
		DurationSec: elapsed,
		Error:       "worker " + reason,
	})
	writer.PushEvent(adapterprotocol.Event{
		Event:   adapterprotocol.EventSubagentFailed,
		AgentID: w.agentID,
		Data:    failedData,
	})
}

// workerStore is a thread-safe map of worker ID to testWorker.
type workerStore struct {
	mu sync.Mutex
	m  map[string]*testWorker
}

func (ws *workerStore) set(id string, w *testWorker) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.m[id] = w
}

func (ws *workerStore) get(id string) (*testWorker, bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	w, ok := ws.m[id]
	return w, ok
}

type testSessionState struct {
	sessionFile string
	offset      int64
}

func readTestSessionFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry adapterprotocol.RawEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

func readTestFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry adapterprotocol.RawEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, scanner.Err()
}

func addLatency() {
	if latencyStr := os.Getenv("OX_TEST_LATENCY_MS"); latencyStr != "" {
		if ms, err := strconv.ParseInt(latencyStr, 10, 64); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
}
