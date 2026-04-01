package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. TailWatcher core: adapter-agnostic file tailing ---
// These tests use a trivial parseLine that doesn't depend on any specific
// adapter format. Any new adapter that composes TailWatcher gets this
// behavior for free.

// trivialParseLine treats each line as a user message RawEntry.
func trivialParseLine(line []byte) ([]RawEntry, error) {
	return []RawEntry{{
		Role:    "user",
		Content: string(line),
	}}, nil
}

// TestTailWatcher_ReadFromOffset_Basic verifies that ReadFromOffset returns
// entries starting at a byte offset and advances the offset correctly.
// Failure prevented: reading the same entries repeatedly or missing new entries.
func TestTailWatcher_ReadFromOffset_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("line1\nline2\n"), 0644))

	tw := NewTailWatcher(path, 0, trivialParseLine)

	entries, newOffset, err := tw.ReadFromOffset(0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "line1", entries[0].Content)
	assert.Equal(t, "line2", entries[1].Content)
	assert.Greater(t, newOffset, int64(0))

	// reading again from new offset should yield nothing
	entries2, offset2, err := tw.ReadFromOffset(newOffset)
	require.NoError(t, err)
	assert.Empty(t, entries2)
	assert.Equal(t, newOffset, offset2)
}

// TestTailWatcher_ReadFromOffset_Incremental verifies that appending to a
// file after the first read only returns new entries on the second read.
// Failure prevented: re-reading the entire file on each incremental read.
func TestTailWatcher_ReadFromOffset_Incremental(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0644))

	tw := NewTailWatcher(path, 0, trivialParseLine)

	entries1, offset1, err := tw.ReadFromOffset(0)
	require.NoError(t, err)
	require.Len(t, entries1, 1)
	assert.Equal(t, "first", entries1[0].Content)

	// append
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("second\nthird\n")
	require.NoError(t, err)
	f.Close()

	entries2, offset2, err := tw.ReadFromOffset(offset1)
	require.NoError(t, err)
	require.Len(t, entries2, 2)
	assert.Equal(t, "second", entries2[0].Content)
	assert.Equal(t, "third", entries2[1].Content)
	assert.Greater(t, offset2, offset1)
}

// TestTailWatcher_ReadFromOffset_SkipsEmptyLines verifies blank lines are
// not emitted as entries.
// Failure prevented: ghost entries from blank lines in JSONL files.
func TestTailWatcher_ReadFromOffset_SkipsEmptyLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("line1\n\n\nline2\n"), 0644))

	tw := NewTailWatcher(path, 0, trivialParseLine)
	entries, _, err := tw.ReadFromOffset(0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

// TestTailWatcher_ReadFromOffset_MalformedLines verifies that parseLine
// errors don't abort the read — malformed lines are skipped.
// Failure prevented: one bad line prevents all subsequent lines from being read.
func TestTailWatcher_ReadFromOffset_MalformedLines(t *testing.T) {
	errorOnBad := func(line []byte) ([]RawEntry, error) {
		var m map[string]string
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, err
		}
		return []RawEntry{{Role: "user", Content: m["text"]}}, nil
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		"{bad json\n"+
			`{"text":"good"}`+"\n"+
			"also bad\n",
	), 0644))

	tw := NewTailWatcher(path, 0, errorOnBad)
	entries, _, err := tw.ReadFromOffset(0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "good", entries[0].Content)
}

// TestTailWatcher_ReadFromOffset_FileNotFound verifies missing file returns error.
// Failure prevented: silent data loss when session file disappears.
func TestTailWatcher_ReadFromOffset_FileNotFound(t *testing.T) {
	tw := NewTailWatcher("/nonexistent/file.jsonl", 0, trivialParseLine)
	_, _, err := tw.ReadFromOffset(0)
	require.Error(t, err)
}

// TestTailWatcher_Offset_TracksPosition verifies the Offset() accessor
// reflects the last stored position.
func TestTailWatcher_Offset_TracksPosition(t *testing.T) {
	tw := NewTailWatcher("/any/path", 42, trivialParseLine)
	assert.Equal(t, int64(42), tw.Offset())
}

// TestTailWatcher_Watch_ReceivesNewEntries verifies that Watch() delivers
// entries when the file is appended to after the watcher starts.
// Failure prevented: daemon misses new agent activity written to session file.
func TestTailWatcher_Watch_ReceivesNewEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with timing")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("initial\n"), 0644))

	// start watching from end of file
	info, err := os.Stat(path)
	require.NoError(t, err)
	tw := NewTailWatcher(path, info.Size(), trivialParseLine)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := tw.Watch(ctx)
	require.NoError(t, err)

	// append after watcher started
	time.Sleep(50 * time.Millisecond) // let watcher initialize
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("new entry\n")
	require.NoError(t, err)
	f.Close()

	// should receive the new entry
	select {
	case entry := <-ch:
		assert.Equal(t, "new entry", entry.Content)
	case <-ctx.Done():
		t.Fatal("timed out waiting for entry from Watch()")
	}
}

// TestTailWatcher_Watch_CancelStopsCleanly verifies that canceling the
// context closes the channel without panic or leak.
// Failure prevented: goroutine leak when daemon stops a watcher.
func TestTailWatcher_Watch_CancelStopsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with timing")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))

	tw := NewTailWatcher(path, 0, trivialParseLine)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tw.Watch(ctx)
	require.NoError(t, err)

	cancel()

	// channel should close
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel should be closed after cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after cancel")
	}
}

// TestTailWatcher_Watch_IgnoresPreExistingContent verifies that content
// written before Watch() starts is not emitted.
// Failure prevented: duplicate entries when daemon restarts and re-tails.
func TestTailWatcher_Watch_IgnoresPreExistingContent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with timing")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("old line 1\nold line 2\n"), 0644))

	info, err := os.Stat(path)
	require.NoError(t, err)
	tw := NewTailWatcher(path, info.Size(), trivialParseLine)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := tw.Watch(ctx)
	require.NoError(t, err)

	// append new content
	time.Sleep(50 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("new line\n")
	require.NoError(t, err)
	f.Close()

	select {
	case entry := <-ch:
		assert.Equal(t, "new line", entry.Content, "should only see new content, not pre-existing")
	case <-ctx.Done():
		t.Fatal("timed out waiting for entry")
	}
}

// --- B. Adapter-specific TailWatcher composition ---
// These verify that each adapter correctly composes TailWatcher through
// its Watch() and ReadFromOffset() methods.

// TestCodexAdapter_ReadFromOffset_ViaExportedMethod verifies the exported
// ReadFromOffset method (which composes TailWatcher) works correctly.
// Failure prevented: TailWatcher refactor breaks Codex incremental reading.
func TestCodexAdapter_ReadFromOffset_ViaExportedMethod(t *testing.T) {
	adapter := &CodexAdapter{}
	dir := t.TempDir()
	path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
		codexSessionMeta("/project", "0.106.0"),
		codexUserMsg("hello"),
		codexAssistantMsg("hi there"),
	})

	entries, newOffset, err := adapter.ReadFromOffset(path, 0)
	require.NoError(t, err)
	assert.Greater(t, newOffset, int64(0))
	require.Len(t, entries, 2) // user + assistant (session_meta skipped)
	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "assistant", entries[1].Role)
}

// TestClaudeCodeAdapter_ReadFromOffset_ViaExportedMethod verifies the exported
// ReadFromOffset method (which composes TailWatcher) works correctly.
// Failure prevented: TailWatcher refactor breaks Claude Code incremental reading.
func TestClaudeCodeAdapter_ReadFromOffset_ViaExportedMethod(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"hello"},"isMeta":false}`+"\n"+
			`{"type":"assistant","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`+"\n",
	), 0644))

	entries, newOffset, err := adapter.ReadFromOffset(path, 0)
	require.NoError(t, err)
	assert.Greater(t, newOffset, int64(0))
	require.Len(t, entries, 2)
	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "assistant", entries[1].Role)
}

// TestCodexAdapter_Watch_MergesToolEntries verifies that the Watch path
// applies mergeToolEntries to batches before sending through the channel.
// Failure prevented: tail-mode recordings produce orphaned function_call_output
// entries with ToolOutput but no ToolName — the primary use case for this PR.
func TestCodexAdapter_Watch_MergesToolEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with timing")
	}

	adapter := &CodexAdapter{}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := adapter.Watch(ctx, path)
	require.NoError(t, err)

	// append a function_call + error output pair atomically (same write)
	time.Sleep(50 * time.Millisecond)
	callLine, _ := json.Marshal(codexFunctionCallWithID("exec_command", `{"cmd":"make test"}`, "call_merge"))
	outputLine, _ := json.Marshal(codexFunctionCallOutput("call_merge", "Process exited with code 1"))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(string(callLine) + "\n" + string(outputLine) + "\n")
	require.NoError(t, err)
	f.Close()

	// should receive ONE merged entry, not two separate ones
	select {
	case entry := <-ch:
		assert.Equal(t, "tool", entry.Role)
		assert.Equal(t, "exec_command", entry.ToolName, "merged entry must have ToolName")
		assert.Equal(t, "Process exited with code 1", entry.ToolOutput, "merged entry must have error output")
		assert.True(t, entry.IsError)
	case <-ctx.Done():
		t.Fatal("timed out waiting for merged entry from Watch()")
	}
}

// TestTailWatcher_WithBatchTransform_AppliedInWatch verifies that batchTransform
// is applied to entries in the Watch path, not just ReadFromOffset.
// Failure prevented: batchTransform silently ignored in Watch, only works in ReadFromOffset.
func TestTailWatcher_WithBatchTransform_AppliedInWatch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with timing")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	// transform that uppercases content
	upper := func(entries []RawEntry) []RawEntry {
		for i := range entries {
			entries[i].Content = entries[i].Content + "_TRANSFORMED"
		}
		return entries
	}

	tw := NewTailWatcher(path, 0, trivialParseLine).WithBatchTransform(upper)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := tw.Watch(ctx)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("hello\n")
	require.NoError(t, err)
	f.Close()

	select {
	case entry := <-ch:
		assert.Equal(t, "hello_TRANSFORMED", entry.Content)
	case <-ctx.Done():
		t.Fatal("timed out waiting for transformed entry")
	}
}

// --- C. Codex adapter: IncrementalReader interface compliance ---
// Verifies Codex adapter satisfies IncrementalReader after TailWatcher refactor.

func TestCodexAdapter_IncrementalReader_InterfaceCompliance(t *testing.T) {
	var _ IncrementalReader = &CodexAdapter{}
}

// --- D. Codex adapter: error output merging with full Read pipeline ---
// These test the INTENT of the merge: tool calls and their error outputs
// should appear as a single merged entry when read through the full pipeline.

// TestCodexAdapter_Read_MergesPipeline_ErrorOutputMergedIntoToolCall verifies
// that Read() returns a single merged entry for function_call + error output.
// Failure prevented: tool errors appear as orphaned entries without tool name context.
func TestCodexAdapter_Read_MergesPipeline_ErrorOutputMergedIntoToolCall(t *testing.T) {
	adapter := &CodexAdapter{}
	dir := t.TempDir()
	path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
		codexSessionMeta("/project", "0.106.0"),
		codexUserMsg("run the build"),
		codexFunctionCallWithID("exec_command", `{"cmd":"make build"}`, "call_build"),
		codexFunctionCallOutput("call_build", "Process exited with code 2"),
		codexAssistantMsg("Build failed."),
	})

	entries, err := adapter.Read(path)
	require.NoError(t, err)
	require.Len(t, entries, 3, "user + merged_tool + assistant")

	tool := entries[1]
	assert.Equal(t, "tool", tool.Role)
	assert.Equal(t, "exec_command", tool.ToolName, "merged entry retains tool name")
	assert.Equal(t, `{"cmd":"make build"}`, tool.ToolInput, "merged entry retains tool input")
	assert.Equal(t, "Process exited with code 2", tool.ToolOutput, "merged entry has error output")
	assert.True(t, tool.IsError, "merged entry is flagged as error")
	assert.Equal(t, "call_build", tool.CallID, "merged entry retains call ID")
}

// TestCodexAdapter_Read_MergesPipeline_SuccessOutputDropped verifies that
// successful tool output is not included in the recording (lean recordings).
// Failure prevented: recordings bloated with success output that adds no value.
func TestCodexAdapter_Read_MergesPipeline_SuccessOutputDropped(t *testing.T) {
	adapter := &CodexAdapter{}
	dir := t.TempDir()
	path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
		codexFunctionCallWithID("exec_command", `{"cmd":"ls"}`, "call_ls"),
		codexFunctionCallOutput("call_ls", "Process exited with code 0"),
	})

	entries, err := adapter.Read(path)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the function_call entry, output dropped")
	assert.Empty(t, entries[0].ToolOutput)
	assert.False(t, entries[0].IsError)
}

// TestCodexAdapter_ReadFromOffset_MergesPipeline verifies that incremental
// reads also apply merge logic, not just full Read().
// Failure prevented: daemon tail mode produces unmerged entries.
func TestCodexAdapter_ReadFromOffset_MergesPipeline(t *testing.T) {
	adapter := &CodexAdapter{}
	dir := t.TempDir()
	path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
		codexFunctionCallWithID("exec_command", `{"cmd":"test"}`, "call_t"),
		codexFunctionCallOutput("call_t", "Process exited with code 1"),
	})

	entries, _, err := adapter.ReadFromOffset(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "exec_command", entries[0].ToolName)
	assert.Equal(t, "Process exited with code 1", entries[0].ToolOutput)
	assert.True(t, entries[0].IsError)
}
