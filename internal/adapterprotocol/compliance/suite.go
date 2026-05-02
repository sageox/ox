//go:build compliance

// Package compliance provides a black-box test suite that validates any
// ox adapter binary against the protocol spec. Tests spawn the binary,
// exercise subcommands, and verify responses conform to the protocol.
//
// Usage:
//
//	OX_ADAPTER_BINARY=./bin/ox-adapter-claude-code \
//	  go test ./internal/adapterprotocol/compliance/ -tags compliance -v
package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/ndjson"
)

// Suite runs protocol compliance tests against an adapter binary.
type Suite struct {
	// Binary is the path to the adapter binary under test.
	Binary string

	// SessionFile is a path to a real or fake session file for read tests.
	// If empty, read-related tests are skipped.
	SessionFile string

	// AgentID is a fake agent ID for serve-mode tests.
	AgentID string
}

// NewSuiteFromEnv creates a Suite using the OX_ADAPTER_BINARY environment variable.
func NewSuiteFromEnv(t *testing.T) *Suite {
	t.Helper()
	binary := os.Getenv("OX_ADAPTER_BINARY")
	if binary == "" {
		t.Skip("OX_ADAPTER_BINARY not set")
	}
	return &Suite{
		Binary:      binary,
		SessionFile: os.Getenv("OX_ADAPTER_SESSION_FILE"),
		AgentID:     "compliance-test-OxA1b2",
	}
}

// RunAll runs the full compliance suite.
func (s *Suite) RunAll(t *testing.T) {
	t.Run("info", s.TestInfo)
	t.Run("info/protocol_version", s.TestInfoProtocolVersion)
	t.Run("detect", s.TestDetect)
	t.Run("serve/startup", s.TestServeStartup)
	t.Run("serve/shutdown", s.TestServeShutdown)
	t.Run("serve/unknown-method", s.TestServeUnknownMethod)

	t.Run("find-session/bad-repo-root", s.TestFindSessionBadRepoRoot)

	// subagent tests only run if the adapter declares the capability
	if s.hasCapability(t, adapterprotocol.CapSubagentController) {
		t.Run("serve/spawn-subagent", s.TestSpawnSubagent)
		t.Run("serve/subagent-status", s.TestSubagentStatus)
		t.Run("serve/cancel-subagent", s.TestCancelSubagent)
	} else {
		t.Run("serve/spawn-subagent-method-not-found", s.TestSpawnSubagentMethodNotFound)
	}
}

// --- One-shot tests ---

// TestInfo verifies the info subcommand returns valid metadata.
func (s *Suite) TestInfo(t *testing.T) {
	out := s.execOnce(t, "info")
	var resp adapterprotocol.InfoResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("info response is not valid JSON: %v\nraw: %s", err, out)
	}
	if resp.Name == "" {
		t.Error("info.name must not be empty")
	}
	if resp.Version == "" {
		t.Error("info.version must not be empty")
	}
	if resp.Type == "" {
		t.Error("info.type must not be empty")
	}
}

// TestInfoProtocolVersion verifies the protocol version is current.
func (s *Suite) TestInfoProtocolVersion(t *testing.T) {
	out := s.execOnce(t, "info")
	var resp adapterprotocol.InfoResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ProtocolVersion < adapterprotocol.ProtocolVersion {
		t.Errorf("protocol_version = %d, minimum required = %d",
			resp.ProtocolVersion, adapterprotocol.ProtocolVersion)
	}
}

// TestDetect verifies the detect subcommand returns valid JSON.
func (s *Suite) TestDetect(t *testing.T) {
	out := s.execOnce(t, "detect")
	var resp adapterprotocol.DetectResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("detect response is not valid JSON: %v\nraw: %s", err, out)
	}
	// reason should always be present
	if resp.Reason == "" {
		t.Error("detect.reason should explain why detected/not detected")
	}
}

// TestFindSessionBadRepoRoot verifies that find-session with a nonexistent
// repo root returns a structured error, not empty output or a panic.
// Failure prevented: adapter silently returns empty result for bad repoRoot,
// causing session recording to fail without any error message.
func (s *Suite) TestFindSessionBadRepoRoot(t *testing.T) {
	stdout, stderr, err := s.execOnceAllowError(t, "find-session",
		"--repo-root", "/nonexistent/path/compliance-test",
		"--agent-id", s.AgentID,
		"--since", time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339),
	)

	// adapter MUST signal failure: either non-zero exit or a JSON error field
	if err == nil {
		// exited 0 — stdout must contain an error field
		var resp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(stdout, &resp) != nil || resp.Error == "" {
			t.Fatalf("find-session with bad repo-root exited 0 without error field\nstdout: %s", stdout)
		}
		return
	}

	// non-zero exit — check for structured JSON error in stdout
	if len(stdout) > 0 {
		var resp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(stdout, &resp) == nil && resp.Error != "" {
			return // structured error, good
		}
	}

	// non-zero exit with stderr is also acceptable
	if len(stderr) > 0 {
		return
	}

	// non-zero exit with no output at all is still acceptable (it's an error)
	_ = err
}

// --- Serve mode tests ---

// serveSession manages a serve-mode adapter process for testing.
type serveSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *ndjson.Scanner
	enc    *ndjson.Encoder
}

func (s *Suite) startServe(t *testing.T) *serveSession {
	t.Helper()
	cmd := exec.Command(s.Binary, "--serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}

	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})

	return &serveSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: ndjson.NewScanner(stdout),
		enc:    ndjson.NewEncoder(stdin),
	}
}

func (ss *serveSession) send(t *testing.T, req adapterprotocol.Request) {
	t.Helper()
	if err := ss.enc.Encode(req); err != nil {
		t.Fatalf("send request: %v", err)
	}
}

func (ss *serveSession) read(t *testing.T) adapterprotocol.Response {
	t.Helper()
	if !ss.stdout.Scan() {
		err := ss.stdout.Err()
		if err != nil {
			t.Fatalf("read response failed: %v", err)
		}
		t.Fatal("unexpected EOF reading response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(ss.stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nraw: %s", err, ss.stdout.Bytes())
	}
	return resp
}

// readWithTimeout reads from stdout with a deadline. Returns the raw bytes
// or an error if the timeout expires. Useful for reading push events that
// may arrive at unpredictable times.
func (ss *serveSession) readWithTimeout(t *testing.T, timeout time.Duration) ([]byte, error) {
	t.Helper()
	type scanResult struct {
		data []byte
		ok   bool
		err  error
	}
	ch := make(chan scanResult, 1)
	go func() {
		ok := ss.stdout.Scan()
		ch <- scanResult{
			data: append([]byte(nil), ss.stdout.Bytes()...),
			ok:   ok,
			err:  ss.stdout.Err(),
		}
	}()

	select {
	case res := <-ch:
		if !res.ok {
			if res.err != nil {
				return nil, fmt.Errorf("scan error: %w", res.err)
			}
			return nil, fmt.Errorf("unexpected EOF")
		}
		return res.data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout after %v waiting for output", timeout)
	}
}

func (ss *serveSession) wait() error {
	return ss.cmd.Wait()
}

// TestServeStartup verifies the adapter can start in serve mode.
func (s *Suite) TestServeStartup(t *testing.T) {
	ss := s.startServe(t)
	// send shutdown immediately to verify clean startup/shutdown cycle
	ss.send(t, adapterprotocol.Request{ID: 1, Method: adapterprotocol.MethodShutdown})
	resp := ss.read(t)
	if resp.Error != nil {
		t.Errorf("shutdown returned error: %+v", resp.Error)
	}
}

// TestServeShutdown verifies the adapter exits cleanly after shutdown.
func (s *Suite) TestServeShutdown(t *testing.T) {
	ss := s.startServe(t)
	ss.send(t, adapterprotocol.Request{ID: 1, Method: adapterprotocol.MethodShutdown})
	resp := ss.read(t)
	if resp.ID != 1 {
		t.Errorf("response ID = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}

	// process should exit within 2s
	done := make(chan struct{})
	go func() {
		ss.stdin.Close()
		ss.wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not exit after shutdown within 2s")
	}
}

// TestServeUnknownMethod verifies unknown methods return method_not_found.
func (s *Suite) TestServeUnknownMethod(t *testing.T) {
	ss := s.startServe(t)

	ss.send(t, adapterprotocol.Request{ID: 5, Method: "some-future-method", Params: json.RawMessage(`{}`)})
	resp := ss.read(t)

	if resp.ID != 5 {
		t.Errorf("response ID = %d, want 5", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}

	// clean up
	ss.send(t, adapterprotocol.Request{ID: 6, Method: adapterprotocol.MethodShutdown})
	ss.read(t) // consume shutdown response
}

// --- Subagent compliance tests ---

// TestSpawnSubagent verifies the adapter can spawn a subagent worker and
// that the worker eventually sends a completion or failure event.
func (s *Suite) TestSpawnSubagent(t *testing.T) {
	ss := s.startServe(t)

	workerID := "w-compliance-0001"
	params, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID: workerID,
		AgentID:  s.AgentID,
		RepoRoot: "/tmp/compliance-test",
		Task:     "compliance test task",
	})

	ss.send(t, adapterprotocol.Request{ID: 10, Method: adapterprotocol.MethodSpawnSubagent, Params: params})
	resp := ss.read(t)

	if resp.ID != 10 {
		t.Errorf("response ID = %d, want 10", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("spawn-subagent returned error: %+v", resp.Error)
	}

	// verify result contains worker_id and starting status
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result adapterprotocol.SpawnSubagentResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	if result.WorkerID != workerID {
		t.Errorf("result.worker_id = %q, want %q", result.WorkerID, workerID)
	}
	if result.Status != adapterprotocol.WorkerStatusStarting {
		t.Errorf("result.status = %q, want %q", result.Status, adapterprotocol.WorkerStatusStarting)
	}

	// wait for a terminal event (completed or failed) with timeout
	deadline := time.After(5 * time.Second)
	gotTerminal := false
	for !gotTerminal {
		raw, err := ss.readWithTimeout(t, 5*time.Second)
		if err != nil {
			t.Fatalf("waiting for worker event: %v", err)
		}

		// try parsing as event first (push events have "event" field)
		var evt adapterprotocol.Event
		if jsonErr := json.Unmarshal(raw, &evt); jsonErr == nil && evt.Event != "" {
			switch evt.Event {
			case adapterprotocol.EventSubagentCompleted:
				gotTerminal = true
				var data adapterprotocol.SubagentCompletedData
				if err := json.Unmarshal(evt.Data, &data); err != nil {
					t.Fatalf("unmarshal completed data: %v", err)
				}
				if data.WorkerID != workerID {
					t.Errorf("completed event worker_id = %q, want %q", data.WorkerID, workerID)
				}
			case adapterprotocol.EventSubagentFailed:
				gotTerminal = true
				var data adapterprotocol.SubagentFailedData
				if err := json.Unmarshal(evt.Data, &data); err != nil {
					t.Fatalf("unmarshal failed data: %v", err)
				}
				if data.WorkerID != workerID {
					t.Errorf("failed event worker_id = %q, want %q", data.WorkerID, workerID)
				}
			case adapterprotocol.EventSubagentProgress:
				// progress events are expected, keep waiting
				continue
			}
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for terminal worker event")
		default:
		}
	}

	// clean up
	ss.send(t, adapterprotocol.Request{ID: 99, Method: adapterprotocol.MethodShutdown})
	ss.read(t)
}

// TestSubagentStatus verifies the adapter returns valid status after spawning.
func (s *Suite) TestSubagentStatus(t *testing.T) {
	ss := s.startServe(t)

	workerID := "w-compliance-0002"
	spawnParams, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID: workerID,
		AgentID:  s.AgentID,
		RepoRoot: "/tmp/compliance-test",
		Task:     "status check task",
	})

	// spawn first
	ss.send(t, adapterprotocol.Request{ID: 20, Method: adapterprotocol.MethodSpawnSubagent, Params: spawnParams})
	spawnResp := ss.read(t)
	if spawnResp.Error != nil {
		t.Fatalf("spawn-subagent error: %+v", spawnResp.Error)
	}

	// immediately query status
	statusParams, _ := json.Marshal(adapterprotocol.SubagentStatusParams{
		WorkerID: workerID,
	})
	ss.send(t, adapterprotocol.Request{ID: 21, Method: adapterprotocol.MethodSubagentStatus, Params: statusParams})

	// read until we get the status response (skip any interleaved push events)
	var statusResp adapterprotocol.Response
	for {
		raw, err := ss.readWithTimeout(t, 5*time.Second)
		if err != nil {
			t.Fatalf("waiting for status response: %v", err)
		}
		if err := json.Unmarshal(raw, &statusResp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		// status response has an id field matching our request
		if statusResp.ID == 21 {
			break
		}
		// otherwise it's a push event, keep reading
	}

	if statusResp.Error != nil {
		t.Fatalf("subagent-status error: %+v", statusResp.Error)
	}

	resultBytes, err := json.Marshal(statusResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result adapterprotocol.SubagentStatusResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if result.WorkerID != workerID {
		t.Errorf("status.worker_id = %q, want %q", result.WorkerID, workerID)
	}
	// status must be one of the valid values
	validStatuses := map[string]bool{
		adapterprotocol.WorkerStatusStarting:  true,
		adapterprotocol.WorkerStatusRunning:   true,
		adapterprotocol.WorkerStatusCompleted: true,
		adapterprotocol.WorkerStatusFailed:    true,
		adapterprotocol.WorkerStatusCanceled:  true,
		adapterprotocol.WorkerStatusTimedOut:  true,
		adapterprotocol.WorkerStatusCanceling: true,
	}
	if !validStatuses[result.Status] {
		t.Errorf("status.status = %q, not a valid worker status", result.Status)
	}

	// clean up
	ss.send(t, adapterprotocol.Request{ID: 99, Method: adapterprotocol.MethodShutdown})
	// drain remaining events/responses
	ss.readWithTimeout(t, 2*time.Second)
}

// TestCancelSubagent verifies the adapter can cancel a running worker and
// that a failed event with exit_reason "canceled" is emitted.
func (s *Suite) TestCancelSubagent(t *testing.T) {
	ss := s.startServe(t)

	workerID := "w-compliance-0003"
	// use a long worker duration so we have time to cancel
	spawnParams, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID:   workerID,
		AgentID:    s.AgentID,
		RepoRoot:   "/tmp/compliance-test",
		Task:       "cancellation test task",
		TimeoutSec: 60,
	})

	ss.send(t, adapterprotocol.Request{ID: 30, Method: adapterprotocol.MethodSpawnSubagent, Params: spawnParams})
	spawnResp := ss.read(t)
	if spawnResp.Error != nil {
		t.Fatalf("spawn-subagent error: %+v", spawnResp.Error)
	}

	// immediately cancel
	cancelParams, _ := json.Marshal(adapterprotocol.CancelSubagentParams{
		WorkerID: workerID,
		Reason:   "compliance_test",
	})
	ss.send(t, adapterprotocol.Request{ID: 31, Method: adapterprotocol.MethodCancelSubagent, Params: cancelParams})

	// read until we get the cancel response (skip push events)
	var cancelResp adapterprotocol.Response
	var pendingEvents [][]byte
	for {
		raw, err := ss.readWithTimeout(t, 5*time.Second)
		if err != nil {
			t.Fatalf("waiting for cancel response: %v", err)
		}
		if err := json.Unmarshal(raw, &cancelResp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if cancelResp.ID == 31 {
			break
		}
		// save push events for later inspection
		pendingEvents = append(pendingEvents, append([]byte(nil), raw...))
	}

	if cancelResp.Error != nil {
		t.Fatalf("cancel-subagent error: %+v", cancelResp.Error)
	}

	resultBytes, err := json.Marshal(cancelResp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result adapterprotocol.CancelSubagentResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal cancel result: %v", err)
	}
	if result.Status != adapterprotocol.WorkerStatusCanceling {
		t.Errorf("cancel result.status = %q, want %q", result.Status, adapterprotocol.WorkerStatusCanceling)
	}

	// wait for the subagent.failed event with canceled reason
	gotCancelledEvent := false

	// check any events we already buffered
	for _, raw := range pendingEvents {
		var evt adapterprotocol.Event
		if err := json.Unmarshal(raw, &evt); err == nil && evt.Event == adapterprotocol.EventSubagentFailed {
			var data adapterprotocol.SubagentFailedData
			if err := json.Unmarshal(evt.Data, &data); err == nil && data.ExitReason == "canceled" {
				gotCancelledEvent = true
			}
		}
	}

	// if not found yet, keep reading
	deadline := time.After(5 * time.Second)
	for !gotCancelledEvent {
		raw, err := ss.readWithTimeout(t, 5*time.Second)
		if err != nil {
			t.Fatalf("waiting for canceled event: %v", err)
		}
		var evt adapterprotocol.Event
		if err := json.Unmarshal(raw, &evt); err == nil && evt.Event == adapterprotocol.EventSubagentFailed {
			var data adapterprotocol.SubagentFailedData
			if err := json.Unmarshal(evt.Data, &data); err == nil {
				if data.WorkerID != workerID {
					t.Errorf("failed event worker_id = %q, want %q", data.WorkerID, workerID)
				}
				if data.ExitReason != "canceled" {
					t.Errorf("failed event exit_reason = %q, want %q", data.ExitReason, "canceled")
				}
				gotCancelledEvent = true
			}
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for canceled event")
		default:
		}
	}

	// clean up
	ss.send(t, adapterprotocol.Request{ID: 99, Method: adapterprotocol.MethodShutdown})
	ss.readWithTimeout(t, 2*time.Second)
}

// TestSpawnSubagentMethodNotFound verifies adapters without subagent_controller
// capability return method_not_found for subagent methods.
func (s *Suite) TestSpawnSubagentMethodNotFound(t *testing.T) {
	ss := s.startServe(t)

	params, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-should-fail-0001",
		AgentID:  s.AgentID,
		RepoRoot: "/tmp/compliance-test",
		Task:     "should not work",
	})

	ss.send(t, adapterprotocol.Request{ID: 40, Method: adapterprotocol.MethodSpawnSubagent, Params: params})
	resp := ss.read(t)

	if resp.ID != 40 {
		t.Errorf("response ID = %d, want 40", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("expected error for spawn-subagent on adapter without capability")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}

	// clean up
	ss.send(t, adapterprotocol.Request{ID: 41, Method: adapterprotocol.MethodShutdown})
	ss.read(t)
}

// --- Helpers ---

func (s *Suite) execOnce(t *testing.T, subcommand string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdArgs := append([]string{subcommand}, args...)
	cmd := exec.CommandContext(ctx, s.Binary, cmdArgs...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s failed: %v\nstderr: %s", s.Binary, subcommand, err, stderr.String())
	}

	return bytes.TrimSpace(stdout.Bytes())
}

// execOnceAllowError runs a one-shot subcommand, returning stdout, stderr,
// and any execution error. Unlike execOnce, it does not fatal on non-zero exit.
func (s *Suite) execOnceAllowError(t *testing.T, subcommand string, args ...string) (stdout, stderr []byte, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdArgs := append([]string{subcommand}, args...)
	cmd := exec.CommandContext(ctx, s.Binary, cmdArgs...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion),
	)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return bytes.TrimSpace(stdoutBuf.Bytes()), bytes.TrimSpace(stderrBuf.Bytes()), err
}

// hasCapability checks if the adapter under test declares a given capability.
func (s *Suite) hasCapability(t *testing.T, cap string) bool {
	t.Helper()
	out := s.execOnce(t, "info")
	var resp adapterprotocol.InfoResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("info response unmarshal: %v", err)
	}
	for _, c := range resp.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
