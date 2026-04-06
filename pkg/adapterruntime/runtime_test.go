package adapterruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/sageox/ox/pkg/ndjson"
)

func TestServer_Shutdown(t *testing.T) {
	req := adapterprotocol.Request{ID: 1, Method: "shutdown"}
	reqBytes, _ := json.Marshal(req)

	stdin := bytes.NewBuffer(append(reqBytes, '\n'))
	var stdout bytes.Buffer

	srv := adapterruntime.NewServer(stdin, &stdout)
	srv.Serve()

	// parse response
	sc := ndjson.NewScanner(&stdout)
	if !sc.Scan() {
		t.Fatal("expected response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("response ID = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	req := adapterprotocol.Request{ID: 5, Method: "some-future-method"}
	reqBytes, _ := json.Marshal(req)
	// send unknown method followed by shutdown
	shutReq := adapterprotocol.Request{ID: 6, Method: "shutdown"}
	shutBytes, _ := json.Marshal(shutReq)

	var input bytes.Buffer
	input.Write(reqBytes)
	input.WriteByte('\n')
	input.Write(shutBytes)
	input.WriteByte('\n')

	var stdout bytes.Buffer
	srv := adapterruntime.NewServer(&input, &stdout)
	srv.Serve()

	sc := ndjson.NewScanner(&stdout)
	// first response: unknown method error
	if !sc.Scan() {
		t.Fatal("expected first response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 5 {
		t.Errorf("response ID = %d, want 5", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "some-future-method") {
		t.Errorf("error message should contain method name, got: %s", resp.Error.Message)
	}
}

func TestServer_FindSession(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.FindSessionParams{
		AgentID:  "OxA1b2",
		RepoID:   "abc123",
		RepoRoot: "/tmp/repo",
		Since:    "2026-04-02T10:00:00Z",
	})
	req := adapterprotocol.Request{ID: 1, Method: "find-session", Params: params}
	reqBytes, _ := json.Marshal(req)

	shutReq := adapterprotocol.Request{ID: 2, Method: "shutdown"}
	shutBytes, _ := json.Marshal(shutReq)

	var input bytes.Buffer
	input.Write(reqBytes)
	input.WriteByte('\n')
	input.Write(shutBytes)
	input.WriteByte('\n')

	var stdout bytes.Buffer
	srv := adapterruntime.NewServer(&input, &stdout)
	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		return &adapterprotocol.FindSessionResult{
			SessionFile: "/tmp/session.jsonl",
			Offset:      512,
		}, nil
	})
	srv.Serve()

	sc := ndjson.NewScanner(&stdout)
	if !sc.Scan() {
		t.Fatal("expected response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("ID = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}

	// decode result
	resultBytes, _ := json.Marshal(resp.Result)
	var result adapterprotocol.FindSessionResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.SessionFile != "/tmp/session.jsonl" {
		t.Errorf("SessionFile = %q, want /tmp/session.jsonl", result.SessionFile)
	}
	if result.Offset != 512 {
		t.Errorf("Offset = %d, want 512", result.Offset)
	}
}

func TestServer_EOF(t *testing.T) {
	// empty stdin = immediate EOF, server should exit cleanly
	var stdout bytes.Buffer
	srv := adapterruntime.NewServer(strings.NewReader(""), &stdout)
	srv.Serve()
	// no panic, no output expected
}

func TestSessionStore_SetGetDelete(t *testing.T) {
	store := adapterruntime.NewSessionStore[string]()

	// get on empty
	_, ok := store.Get("agent1")
	if ok {
		t.Error("expected not found on empty store")
	}

	// set and get
	store.Set("agent1", "state1")
	v, ok := store.Get("agent1")
	if !ok || v != "state1" {
		t.Errorf("Get = (%q, %v), want (state1, true)", v, ok)
	}

	// delete
	v, ok = store.Delete("agent1")
	if !ok || v != "state1" {
		t.Errorf("Delete = (%q, %v), want (state1, true)", v, ok)
	}

	// get after delete
	_, ok = store.Get("agent1")
	if ok {
		t.Error("expected not found after delete")
	}

	// delete non-existent
	_, ok = store.Delete("agent2")
	if ok {
		t.Error("expected not found for non-existent key")
	}
}

func TestSessionStore_Concurrent(t *testing.T) {
	store := adapterruntime.NewSessionStore[int]()
	var wg sync.WaitGroup
	n := 100

	// concurrent writes
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store.Set("agent", i)
		}(i)
	}
	wg.Wait()

	// should have some value
	_, ok := store.Get("agent")
	if !ok {
		t.Error("expected value after concurrent writes")
	}
}

func TestWriter_ThreadSafety(t *testing.T) {
	var buf bytes.Buffer
	w := adapterruntime.NewWriter(&buf)

	var wg sync.WaitGroup
	n := 50

	// concurrent response writes
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.WriteResponse(adapterprotocol.Response{ID: i, Result: "ok"})
		}(i)
	}
	wg.Wait()

	// verify all lines are valid JSON
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != n {
		t.Errorf("expected %d lines, got %d", n, len(lines))
	}
	for i, line := range lines {
		var resp adapterprotocol.Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("line %d invalid JSON: %v\nline: %s", i, err, line)
		}
	}
}

func TestRunWithArgs_Info(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{
		Info: func() (*adapterprotocol.InfoResponse, error) {
			return &adapterprotocol.InfoResponse{
				ProtocolVersion: 1,
				Name:            "test",
				Version:         "0.1.0",
				Type:            "test",
			}, nil
		},
	}

	adapterruntime.RunWithArgs(cfg, []string{"info"}, nil, &stdout)

	var resp adapterprotocol.InfoResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "test" {
		t.Errorf("Name = %q, want test", resp.Name)
	}
}

func TestRunWithArgs_Detect(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{
		Detect: func() (*adapterprotocol.DetectResponse, error) {
			return &adapterprotocol.DetectResponse{Detected: true, Reason: "found it"}, nil
		},
	}

	adapterruntime.RunWithArgs(cfg, []string{"detect"}, nil, &stdout)

	var resp adapterprotocol.DetectResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Detected {
		t.Error("expected detected=true")
	}
}

// --- Subagent dispatch tests ---

// sendRequestViaBuffer builds input, creates a server, registers handlers via setup, and returns the first response.
func sendRequestViaBuffer(t *testing.T, req adapterprotocol.Request, setup func(*adapterruntime.Server)) adapterprotocol.Response {
	t.Helper()

	reqBytes, _ := json.Marshal(req)
	shutReq := adapterprotocol.Request{ID: req.ID + 1, Method: "shutdown"}
	shutBytes, _ := json.Marshal(shutReq)

	var input bytes.Buffer
	input.Write(reqBytes)
	input.WriteByte('\n')
	input.Write(shutBytes)
	input.WriteByte('\n')

	var stdout bytes.Buffer
	srv := adapterruntime.NewServer(&input, &stdout)
	if setup != nil {
		setup(srv)
	}
	srv.Serve()

	sc := ndjson.NewScanner(&stdout)
	if !sc.Scan() {
		t.Fatal("expected response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// sendRawRequestViaBuffer writes a raw NDJSON line (bypassing json.Marshal
// which validates RawMessage) followed by shutdown. This allows testing
// dispatch behavior with intentionally malformed params.
func sendRawRequestViaBuffer(t *testing.T, rawLine string, setup func(*adapterruntime.Server)) adapterprotocol.Response {
	t.Helper()

	shutReq := adapterprotocol.Request{ID: 999, Method: "shutdown"}
	shutBytes, _ := json.Marshal(shutReq)

	var input bytes.Buffer
	input.WriteString(rawLine)
	input.WriteByte('\n')
	input.Write(shutBytes)
	input.WriteByte('\n')

	var stdout bytes.Buffer
	srv := adapterruntime.NewServer(&input, &stdout)
	if setup != nil {
		setup(srv)
	}
	srv.Serve()

	sc := ndjson.NewScanner(&stdout)
	if !sc.Scan() {
		t.Fatal("expected response")
	}
	var resp adapterprotocol.Response
	if err := ndjson.Decode(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestServer_SpawnSubagent(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-OxA1b2-0001",
		AgentID:  "r7f3a2-OxA1b2",
		RepoID:   "a1b2c3d4",
		RepoRoot: "/path/to/repo",
		Task:     "Add unit tests for auth module.",
	})
	req := adapterprotocol.Request{ID: 10, Method: adapterprotocol.MethodSpawnSubagent, Params: params}

	resp := sendRequestViaBuffer(t, req, func(srv *adapterruntime.Server) {
		srv.OnSpawnSubagent(func(ctx context.Context, p adapterprotocol.SpawnSubagentParams) (*adapterprotocol.SpawnSubagentResult, error) {
			return &adapterprotocol.SpawnSubagentResult{
				WorkerID: p.WorkerID,
				Status:   adapterprotocol.WorkerStatusStarting,
			}, nil
		})
	})

	if resp.ID != 10 {
		t.Errorf("ID = %d, want 10", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result adapterprotocol.SpawnSubagentResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.WorkerID != "w-OxA1b2-0001" {
		t.Errorf("WorkerID = %q, want w-OxA1b2-0001", result.WorkerID)
	}
	if result.Status != adapterprotocol.WorkerStatusStarting {
		t.Errorf("Status = %q, want %q", result.Status, adapterprotocol.WorkerStatusStarting)
	}
}

func TestServer_SubagentStatus(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.SubagentStatusParams{
		WorkerID: "w-OxA1b2-0001",
	})
	req := adapterprotocol.Request{ID: 11, Method: adapterprotocol.MethodSubagentStatus, Params: params}

	resp := sendRequestViaBuffer(t, req, func(srv *adapterruntime.Server) {
		srv.OnSubagentStatus(func(ctx context.Context, p adapterprotocol.SubagentStatusParams) (*adapterprotocol.SubagentStatusResult, error) {
			return &adapterprotocol.SubagentStatusResult{
				WorkerID:     p.WorkerID,
				Status:       adapterprotocol.WorkerStatusRunning,
				StartedAt:    "2026-04-02T10:30:00Z",
				ElapsedSec:   47,
				OutputLines:  312,
				LastActivity: "2026-04-02T10:30:47Z",
			}, nil
		})
	})

	if resp.ID != 11 {
		t.Errorf("ID = %d, want 11", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result adapterprotocol.SubagentStatusResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.WorkerID != "w-OxA1b2-0001" {
		t.Errorf("WorkerID = %q, want w-OxA1b2-0001", result.WorkerID)
	}
	if result.Status != adapterprotocol.WorkerStatusRunning {
		t.Errorf("Status = %q, want %q", result.Status, adapterprotocol.WorkerStatusRunning)
	}
	if result.ElapsedSec != 47 {
		t.Errorf("ElapsedSec = %d, want 47", result.ElapsedSec)
	}
}

func TestServer_CancelSubagent(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.CancelSubagentParams{
		WorkerID: "w-OxA1b2-0001",
		Reason:   "user_requested",
	})
	req := adapterprotocol.Request{ID: 12, Method: adapterprotocol.MethodCancelSubagent, Params: params}

	resp := sendRequestViaBuffer(t, req, func(srv *adapterruntime.Server) {
		srv.OnCancelSubagent(func(ctx context.Context, p adapterprotocol.CancelSubagentParams) (*adapterprotocol.CancelSubagentResult, error) {
			return &adapterprotocol.CancelSubagentResult{
				WorkerID: p.WorkerID,
				Status:   adapterprotocol.WorkerStatusCanceling,
			}, nil
		})
	})

	if resp.ID != 12 {
		t.Errorf("ID = %d, want 12", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result adapterprotocol.CancelSubagentResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.WorkerID != "w-OxA1b2-0001" {
		t.Errorf("WorkerID = %q, want w-OxA1b2-0001", result.WorkerID)
	}
	if result.Status != adapterprotocol.WorkerStatusCanceling {
		t.Errorf("Status = %q, want %q", result.Status, adapterprotocol.WorkerStatusCanceling)
	}
}

// --- Unregistered subagent handler tests ---
// Failure prevented: daemon sends subagent method to adapter that doesn't support it,
// gets garbage response instead of clean method_not_found error.

func TestServer_SpawnSubagent_Unregistered(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-OxA1b2-0001",
		AgentID:  "r7f3a2-OxA1b2",
		RepoID:   "a1b2c3d4",
		RepoRoot: "/path/to/repo",
		Task:     "test task",
	})
	req := adapterprotocol.Request{ID: 20, Method: adapterprotocol.MethodSpawnSubagent, Params: params}

	resp := sendRequestViaBuffer(t, req, nil)

	if resp.Error == nil {
		t.Fatal("expected error for unregistered spawn-subagent")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}
}

func TestServer_SubagentStatus_Unregistered(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.SubagentStatusParams{WorkerID: "w-OxA1b2-0001"})
	req := adapterprotocol.Request{ID: 21, Method: adapterprotocol.MethodSubagentStatus, Params: params}

	resp := sendRequestViaBuffer(t, req, nil)

	if resp.Error == nil {
		t.Fatal("expected error for unregistered subagent-status")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}
}

func TestServer_CancelSubagent_Unregistered(t *testing.T) {
	params, _ := json.Marshal(adapterprotocol.CancelSubagentParams{WorkerID: "w-OxA1b2-0001"})
	req := adapterprotocol.Request{ID: 22, Method: adapterprotocol.MethodCancelSubagent, Params: params}

	resp := sendRequestViaBuffer(t, req, nil)

	if resp.Error == nil {
		t.Fatal("expected error for unregistered cancel-subagent")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}
}

// --- Invalid params tests ---
// Failure prevented: malformed JSON params cause panic or unstructured error
// instead of clean invalid_params response.
//
// These tests use sendRawRequestViaBuffer because json.Marshal validates
// json.RawMessage content, making it impossible to embed truly invalid JSON
// params via the Request struct. We construct the NDJSON line manually.

func TestServer_SpawnSubagent_InvalidParams(t *testing.T) {
	// the outer JSON is valid but the params value cannot unmarshal to SpawnSubagentParams
	rawLine := `{"id":30,"method":"spawn-subagent","params":"not an object"}`

	resp := sendRawRequestViaBuffer(t, rawLine, func(srv *adapterruntime.Server) {
		srv.OnSpawnSubagent(func(ctx context.Context, p adapterprotocol.SpawnSubagentParams) (*adapterprotocol.SpawnSubagentResult, error) {
			t.Error("handler should not be called with invalid params")
			return nil, nil
		})
	})

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeInvalidParams {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeInvalidParams)
	}
}

func TestServer_SubagentStatus_InvalidParams(t *testing.T) {
	rawLine := fmt.Sprintf(`{"id":31,"method":"%s","params":"not an object"}`, adapterprotocol.MethodSubagentStatus)

	resp := sendRawRequestViaBuffer(t, rawLine, func(srv *adapterruntime.Server) {
		srv.OnSubagentStatus(func(ctx context.Context, p adapterprotocol.SubagentStatusParams) (*adapterprotocol.SubagentStatusResult, error) {
			t.Error("handler should not be called with invalid params")
			return nil, nil
		})
	})

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeInvalidParams {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeInvalidParams)
	}
}

// --- Rules subcommand dispatch tests ---
// Failure prevented: install-rules / check-rules / uninstall-rules one-shot
// CLI dispatch silently drops the response or calls the wrong handler.

func TestRunWithArgs_InstallRules(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{
		InstallRules: func(p adapterprotocol.RulesParams) (*adapterprotocol.InstallRulesResponse, error) {
			return &adapterprotocol.InstallRulesResponse{
				Installed:    true,
				FilesWritten: []string{"ox.md"},
			}, nil
		},
	}

	err := adapterruntime.RunWithArgs(cfg, []string{"install-rules", "--repo-root", "/tmp/test", "--version", "0.8.0"}, nil, &stdout)
	if err != nil {
		t.Fatalf("RunWithArgs returned error: %v", err)
	}

	var resp adapterprotocol.InstallRulesResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Installed {
		t.Error("expected Installed=true")
	}
	if len(resp.FilesWritten) != 1 || resp.FilesWritten[0] != "ox.md" {
		t.Errorf("FilesWritten = %v, want [ox.md]", resp.FilesWritten)
	}
}

func TestRunWithArgs_CheckRules(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{
		CheckRules: func(p adapterprotocol.RulesParams) (*adapterprotocol.CheckRulesResponse, error) {
			return &adapterprotocol.CheckRulesResponse{
				Installed: false,
				Missing:   []string{"ox.md"},
				RulesDir:  ".claude/rules",
			}, nil
		},
	}

	err := adapterruntime.RunWithArgs(cfg, []string{"check-rules", "--repo-root", "/tmp/test", "--version", "0.8.0"}, nil, &stdout)
	if err != nil {
		t.Fatalf("RunWithArgs returned error: %v", err)
	}

	var resp adapterprotocol.CheckRulesResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Installed {
		t.Error("expected Installed=false")
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != "ox.md" {
		t.Errorf("Missing = %v, want [ox.md]", resp.Missing)
	}
	if resp.RulesDir != ".claude/rules" {
		t.Errorf("RulesDir = %q, want .claude/rules", resp.RulesDir)
	}
}

func TestRunWithArgs_UninstallRules(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{
		UninstallRules: func(p adapterprotocol.RulesParams) (*adapterprotocol.UninstallRulesResponse, error) {
			return &adapterprotocol.UninstallRulesResponse{
				Uninstalled:  true,
				FilesRemoved: []string{"ox.md"},
			}, nil
		},
	}

	err := adapterruntime.RunWithArgs(cfg, []string{"uninstall-rules", "--repo-root", "/tmp/test", "--version", "0.8.0"}, nil, &stdout)
	if err != nil {
		t.Fatalf("RunWithArgs returned error: %v", err)
	}

	var resp adapterprotocol.UninstallRulesResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Uninstalled {
		t.Error("expected Uninstalled=true")
	}
	if len(resp.FilesRemoved) != 1 || resp.FilesRemoved[0] != "ox.md" {
		t.Errorf("FilesRemoved = %v, want [ox.md]", resp.FilesRemoved)
	}
}

func TestRunWithArgs_InstallRules_NotImplemented(t *testing.T) {
	var stdout bytes.Buffer
	cfg := adapterruntime.Config{} // no handler registered

	err := adapterruntime.RunWithArgs(cfg, []string{"install-rules", "--repo-root", "/tmp/test", "--version", "0.8.0"}, nil, &stdout)
	if err == nil {
		t.Fatal("expected error for unregistered install-rules handler")
	}

	// verify the error JSON was written to stdout
	var errResp map[string]string
	if jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &errResp); jsonErr != nil {
		t.Fatalf("decode error response: %v", jsonErr)
	}
	if errResp["error"] == "" {
		t.Error("expected non-empty error message in JSON response")
	}
}

func TestServer_CancelSubagent_InvalidParams(t *testing.T) {
	// valid JSON but wrong shape (array instead of object)
	rawLine := fmt.Sprintf(`{"id":32,"method":"%s","params":[1,2,3]}`, adapterprotocol.MethodCancelSubagent)

	resp := sendRawRequestViaBuffer(t, rawLine, func(srv *adapterruntime.Server) {
		srv.OnCancelSubagent(func(ctx context.Context, p adapterprotocol.CancelSubagentParams) (*adapterprotocol.CancelSubagentResult, error) {
			t.Error("handler should not be called with invalid params")
			return nil, nil
		})
	})

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != adapterprotocol.ErrCodeInvalidParams {
		t.Errorf("error code = %q, want %q", resp.Error.Code, adapterprotocol.ErrCodeInvalidParams)
	}
}
