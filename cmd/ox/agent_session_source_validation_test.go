package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

type appendDuringReadClaudeAdapter struct {
	testClaudeCodeAdapter
	appendTurn func()
}

func (a *appendDuringReadClaudeAdapter) Read(path string) ([]adapters.RawEntry, error) {
	a.appendTurn()
	return a.testClaudeCodeAdapter.Read(path)
}

func TestRecoverViaNormalStopQuarantinesNewForeignTurn(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":\"session\",\"cwd\":%q}\n", filepath.Dir(projectRoot))
	f, err := os.OpenFile(sourceFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(foreign); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverViaNormalStop(&agentinstance.Instance{AgentID: agentID}, projectRoot, state); err == nil {
		t.Fatal("normal recovery must not publish newly foreign turns")
	}
	persisted, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || persisted == nil || !persisted.SourceRejected {
		t.Fatalf("normal recovery must preserve quarantine, state=%v error=%v", persisted, err)
	}
}

func TestRecoverPreservesUndiscoveredClaudeHookSource(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	if err := session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = ""
		s.WatchMode = "hook"
	}); err != nil {
		t.Fatal(err)
	}
	if err := runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID}); err == nil {
		t.Fatal("recover must defer when the native source was never verified")
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil {
		t.Fatalf("uncertain source must retain recording marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, "raw.jsonl")); err != nil {
		t.Fatalf("uncertain source must retain cached header: %v", err)
	}
}

func TestRecoverAndProcessPreserveQuarantinedClaudeSession(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.MarkSourceRejected(projectRoot, agentID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourceFile); err != nil {
		t.Fatal(err)
	}
	if err := runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID}); err == nil {
		t.Fatal("recover must not publish cached data after native file disappears")
	}
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || !state.SourceRejected {
		t.Fatalf("quarantined marker must survive recovery: state=%v error=%v", state, err)
	}
	if _, err := processAgentSession(projectRoot, state); err == nil {
		t.Fatal("internal stop must not process quarantined data")
	}
	after, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("recovery must leave cached prefix untouched: %v", err)
	}
}

func TestProcessAgentSession_RejectsSourceThatMovedToAnotherRepo(t *testing.T) {
	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	data += fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &session.RecordingState{AdapterName: "claude-code", AgentSessionID: id, WorkspacePath: repo, SessionFile: path, SessionPath: t.TempDir()}
	if _, err := processAgentSession(repo, state); err == nil || !strings.Contains(err.Error(), "no longer belongs") {
		t.Fatalf("final drain accepted a Claude session after it left the repo: %v", err)
	}
}

func TestProcessAgentSession_RejectsTurnAppendedDuringRead(t *testing.T) {
	repo := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &appendDuringReadClaudeAdapter{appendTurn: func() {
		foreign := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(foreign); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}}
	adapters.Register(adapter)
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	state := &session.RecordingState{
		AdapterName: "claude-code", AgentSessionID: id, WorkspacePath: repo,
		SessionFile: path, SessionPath: t.TempDir(),
	}
	if _, err := processAgentSession(repo, state); err == nil || !strings.Contains(err.Error(), "no longer belongs") {
		t.Fatalf("foreign turn appended during Read must block capture, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, "raw.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("read must not produce raw data before validating, stat error: %v", err)
	}
}
