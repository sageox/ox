package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLogTest creates a project with an active recording and changes cwd.
// Returns the project root and recording state.
func setupLogTest(t *testing.T) (string, *session.RecordingState) {
	t.Helper()

	cfg = &config.Config{}

	projectRoot := setupSessionTestProject(t)

	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     "OxLog1",
		AdapterName: "generic",
	})
	require.NoError(t, err)

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { require.NoError(t, os.Chdir(origDir)) })

	return projectRoot, state
}

// readJSONLEntries reads all JSONL entries from a file.
func readJSONLEntries(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var entries []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}
	return entries
}

func TestSessionLog_UserEntry(t *testing.T) {
	_, state := setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "user", "--content", "Fix the login bug"})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 1)

	assert.Equal(t, "user", entries[0]["type"])
	assert.Equal(t, "Fix the login bug", entries[0]["content"])
	assert.Equal(t, float64(0), entries[0]["seq"])
	assert.NotEmpty(t, entries[0]["timestamp"])
}

func TestSessionLog_AssistantEntry(t *testing.T) {
	_, state := setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "assistant", "--content", "I'll investigate the issue."})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 1)

	assert.Equal(t, "assistant", entries[0]["type"])
	assert.Equal(t, "I'll investigate the issue.", entries[0]["content"])
}

func TestSessionLog_ToolEntry(t *testing.T) {
	_, state := setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{
		"--role", "tool",
		"--tool-name", "bash",
		"--tool-input", "go test ./...",
		"--content", "PASS",
	})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 1)

	assert.Equal(t, "tool", entries[0]["type"])
	assert.Equal(t, "bash", entries[0]["tool_name"])
	assert.Equal(t, "go test ./...", entries[0]["tool_input"])
	assert.Equal(t, "PASS", entries[0]["content"])
}

func TestSessionLog_Stdin(t *testing.T) {
	_, state := setupLogTest(t)

	// replace stdin with a pipe
	r, w, err := os.Pipe()
	require.NoError(t, err)

	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	// write content to pipe and close writer
	go func() {
		w.WriteString("Multi-line\ncontent from stdin")
		w.Close()
	}()

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err = runAgentSessionLog(inst, []string{"--role", "assistant", "--stdin"})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 1)

	assert.Equal(t, "assistant", entries[0]["type"])
	assert.Equal(t, "Multi-line\ncontent from stdin", entries[0]["content"])
}

func TestSessionLog_BothContentAndStdin_Error(t *testing.T) {
	setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "user", "--content", "hello", "--stdin"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use both --content and --stdin")
}

func TestSessionLog_NeitherContentNorStdin_Error(t *testing.T) {
	setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "user"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one of --content or --stdin is required")
}

func TestSessionLog_InvalidRole_Error(t *testing.T) {
	setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "observer", "--content", "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestSessionLog_MissingRole_Error(t *testing.T) {
	setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--content", "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role is required")
}

func TestSessionLog_NoActiveRecording_Error(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	defer os.Chdir(origDir)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "user", "--content", "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

func TestSessionLog_SeqIncrement(t *testing.T) {
	_, state := setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}

	// first entry
	err := runAgentSessionLog(inst, []string{"--role", "user", "--content", "first"})
	require.NoError(t, err)

	// second entry
	err = runAgentSessionLog(inst, []string{"--role", "assistant", "--content", "second"})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 2)

	assert.Equal(t, float64(0), entries[0]["seq"])
	assert.Equal(t, float64(1), entries[1]["seq"])
}

func TestSessionLog_TextOutput(t *testing.T) {
	setupLogTest(t)

	cfg = &config.Config{Text: true}
	t.Cleanup(func() { cfg = &config.Config{} })

	// capture stdout
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	logErr := runAgentSessionLog(inst, []string{"--role", "user", "--content", "hello"})
	w.Close()
	os.Stdout = oldStdout

	require.NoError(t, logErr)

	out := make([]byte, 4096)
	n, _ := r.Read(out)
	output := string(out[:n])

	assert.Contains(t, output, "Logged entry #0")
	assert.Contains(t, output, "user")
	assert.Contains(t, output, "5 chars")
}

func TestSessionLog_ToolFlagsWithNonToolRole_Error(t *testing.T) {
	setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{
		"--role", "user",
		"--tool-name", "bash",
		"--content", "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid with --role tool")
}

func TestSessionLog_GenericAdapterUsesInputJsonl(t *testing.T) {
	_, state := setupLogTest(t)

	// generic adapter: SessionFile is empty, so log should create input.jsonl
	assert.Empty(t, state.SessionFile, "generic adapter should start with empty SessionFile")

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role", "user", "--content", "hello"})
	require.NoError(t, err)

	expectedPath := filepath.Join(state.SessionPath, "input.jsonl")
	_, statErr := os.Stat(expectedPath)
	assert.NoError(t, statErr, "input.jsonl should exist after log with generic adapter")
}

func TestSessionLog_JSONOutput(t *testing.T) {
	setupLogTest(t)

	cfg = &config.Config{}

	// capture stdout
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	logErr := runAgentSessionLog(inst, []string{"--role", "user", "--content", "test"})
	w.Close()
	os.Stdout = oldStdout

	require.NoError(t, logErr)

	out := make([]byte, 4096)
	n, _ := r.Read(out)

	var output sessionLogOutput
	require.NoError(t, json.Unmarshal(out[:n], &output))

	assert.True(t, output.Success)
	assert.Equal(t, 0, output.Seq)
}

func TestSessionLog_EqualsStyleFlags(t *testing.T) {
	_, state := setupLogTest(t)

	inst := &agentinstance.Instance{AgentID: "OxLog1"}
	err := runAgentSessionLog(inst, []string{"--role=assistant", "--content=equals style"})
	require.NoError(t, err)

	targetFile := sessionLogTargetFile(state)
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, 1)

	assert.Equal(t, "assistant", entries[0]["type"])
	assert.Equal(t, "equals style", entries[0]["content"])
}

func TestSessionLog_ConcurrentWrites(t *testing.T) {
	_, state := setupLogTest(t)

	targetFile := sessionLogTargetFile(state)
	const numWriters = 10
	const entriesPerWriter = 5

	var wg sync.WaitGroup
	wg.Add(numWriters)
	errs := make(chan error, numWriters*entriesPerWriter)

	for w := 0; w < numWriters; w++ {
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < entriesPerWriter; i++ {
				content := fmt.Sprintf("writer-%d-entry-%d", writerID, i)
				_, err := appendSessionLogEntry(targetFile, "user", content, "", "")
				if err != nil {
					errs <- fmt.Errorf("writer %d entry %d: %w", writerID, i, err)
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)

	// no errors during concurrent writes
	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}

	// all entries present
	entries := readJSONLEntries(t, targetFile)
	require.Len(t, entries, numWriters*entriesPerWriter)

	// seq numbers are unique and contiguous 0..N-1
	seqs := make([]int, 0, len(entries))
	for _, e := range entries {
		seq, ok := e["seq"].(float64)
		require.True(t, ok, "entry missing seq field")
		seqs = append(seqs, int(seq))
	}
	sort.Ints(seqs)
	for i, seq := range seqs {
		assert.Equal(t, i, seq, "seq numbers should be contiguous, gap at index %d", i)
	}

	// no partial/corrupt lines (readJSONLEntries would fail on parse)
	for i, e := range entries {
		assert.NotEmpty(t, e["type"], "entry %d missing type", i)
		assert.NotEmpty(t, e["content"], "entry %d missing content", i)
	}
}
