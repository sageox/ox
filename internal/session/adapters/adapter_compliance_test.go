package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Adapter compliance tests ---
//
// These tests verify behaviors that ALL adapters must satisfy.
// When adding a new adapter, add it to the test cases below.
// This prevents each adapter from silently diverging on core contracts.

// adapterTestCase describes a minimal session file for compliance testing.
// Each adapter provides its own session fixture and expected entry counts.
type adapterTestCase struct {
	name        string
	adapter     Adapter
	writeFile   func(t *testing.T, dir string) string // writes session file, returns path
	wantEntries int                                    // expected entry count from Read()
}

func codexComplianceFixture() adapterTestCase {
	return adapterTestCase{
		name:    "codex",
		adapter: &CodexAdapter{},
		writeFile: func(t *testing.T, dir string) string {
			return writeCodexSession(t, dir, "session.jsonl", []map[string]any{
				codexSessionMeta("/project", "0.106.0"),
				codexTurnContext("gpt-5"),
				codexUserMsg("hello"),
				codexFunctionCallWithID("exec_command", `{"cmd":"ls"}`, "call_1"),
				codexFunctionCallOutput("call_1", "Process exited with code 1"),
				codexAssistantMsg("done"),
			})
		},
		wantEntries: 3, // user + merged_tool_error + assistant
	}
}

// claudeCodeComplianceFixture creates a temporary Claude Code JSONL session file
// with representative entries: user message, assistant message with text and tool_use,
// and a user turn containing a tool_result (which should be skipped).
func claudeCodeComplianceFixture() adapterTestCase {
	return adapterTestCase{
		name:    "claude-code",
		adapter: &ClaudeCodeAdapter{},
		writeFile: func(t *testing.T, dir string) string {
			t.Helper()
			path := filepath.Join(dir, "session.jsonl")
			f, err := os.Create(path)
			require.NoError(t, err)
			defer f.Close()

			enc := json.NewEncoder(f)

			// user message (simple string content)
			require.NoError(t, enc.Encode(map[string]any{
				"type":      "user",
				"timestamp": "2026-03-15T10:00:00Z",
				"version":   "1.0.3",
				"message": map[string]any{
					"role":    "user",
					"content": "explain the adapter pattern",
				},
			}))

			// assistant message with text + tool_use blocks
			require.NoError(t, enc.Encode(map[string]any{
				"type":      "assistant",
				"timestamp": "2026-03-15T10:00:01Z",
				"message": map[string]any{
					"role":  "assistant",
					"model": "claude-sonnet-4-20250514",
					"content": []map[string]any{
						{"type": "text", "text": "Let me read the file first."},
						{
							"type":  "tool_use",
							"id":    "tool_001",
							"name":  "Read",
							"input": map[string]any{"file_path": "/src/adapter.go"},
						},
					},
				},
			}))

			// user message containing tool_result (should be skipped entirely)
			require.NoError(t, enc.Encode(map[string]any{
				"type":      "user",
				"timestamp": "2026-03-15T10:00:02Z",
				"message": map[string]any{
					"role": "user",
					"content": []map[string]any{
						{
							"type":       "tool_result",
							"tool_use_id": "tool_001",
							"content":    "package adapters\n\ntype Adapter interface{...}",
						},
					},
				},
			}))

			// assistant follow-up with tool_use that has an error result
			require.NoError(t, enc.Encode(map[string]any{
				"type":      "assistant",
				"timestamp": "2026-03-15T10:00:03Z",
				"message": map[string]any{
					"role":  "assistant",
					"model": "claude-sonnet-4-20250514",
					"content": []map[string]any{
						{"type": "text", "text": "The adapter pattern provides a unified interface."},
					},
				},
			}))

			return path
		},
		// expected: user + assistant_text + tool_use + assistant_text = 4
		// the tool_result user turn is skipped (pure tool_result with no text)
		wantEntries: 4,
	}
}

// TestAdapter_Read_ReturnsExpectedEntryCount verifies each adapter's Read()
// returns the correct number of entries for a representative session.
// Failure prevented: adapter changes silently drop or duplicate entries.
func TestAdapter_Read_ReturnsExpectedEntryCount(t *testing.T) {
	cases := []adapterTestCase{
		codexComplianceFixture(),
		claudeCodeComplianceFixture(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := tc.writeFile(t, dir)

			entries, err := tc.adapter.Read(path)
			require.NoError(t, err)
			assert.Len(t, entries, tc.wantEntries)
		})
	}
}

// TestAdapter_Read_RolesAreValid verifies all entries have recognized roles.
// Failure prevented: typos in role strings break downstream classification.
func TestAdapter_Read_RolesAreValid(t *testing.T) {
	validRoles := map[string]bool{
		"user": true, "assistant": true, "system": true, "tool": true,
	}

	cases := []adapterTestCase{
		codexComplianceFixture(),
		claudeCodeComplianceFixture(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := tc.writeFile(t, dir)

			entries, err := tc.adapter.Read(path)
			require.NoError(t, err)
			for i, e := range entries {
				assert.True(t, validRoles[e.Role], "entry %d has invalid role %q", i, e.Role)
			}
		})
	}
}

// TestAdapter_Read_ToolEntriesHaveName verifies all tool entries have a ToolName.
// Entries with ToolOutput only (orphaned function_call_output) must be merged
// or filtered — they should never appear standalone.
// Failure prevented: orphaned error-only entries without tool context.
func TestAdapter_Read_ToolEntriesHaveName(t *testing.T) {
	cases := []adapterTestCase{
		codexComplianceFixture(),
		claudeCodeComplianceFixture(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := tc.writeFile(t, dir)

			entries, err := tc.adapter.Read(path)
			require.NoError(t, err)
			for i, e := range entries {
				if e.Role == "tool" {
					assert.NotEmpty(t, e.ToolName, "tool entry %d has empty ToolName", i)
				}
			}
		})
	}
}

// TestAdapter_Read_TimestampsAreSet verifies entries have non-zero timestamps.
// Failure prevented: entries without timestamps break chronological ordering.
func TestAdapter_Read_TimestampsAreSet(t *testing.T) {
	cases := []adapterTestCase{
		codexComplianceFixture(),
		claudeCodeComplianceFixture(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := tc.writeFile(t, dir)

			entries, err := tc.adapter.Read(path)
			require.NoError(t, err)
			for i, e := range entries {
				assert.False(t, e.Timestamp.IsZero(), "entry %d has zero timestamp", i)
			}
		})
	}
}

// --- Reusable error detection test harness ---
// Use this pattern when adding error detection for a new adapter.

// toolErrorCase describes a tool output string and whether it should be classified as an error.
type toolErrorCase struct {
	name    string
	output  string
	isError bool
}

// codexToolErrorCases defines the expected error classification for Codex tool output.
// Reuse this pattern for other adapters by creating analogous case slices.
var codexToolErrorCases = []toolErrorCase{
	{name: "exit code 0 is success", output: "Process exited with code 0", isError: false},
	{name: "exit code 1 is error", output: "Process exited with code 1", isError: true},
	{name: "exit code 2 is error", output: "Process exited with code 2", isError: true},
	{name: "exit code 127 is error", output: "Process exited with code 127", isError: true},
	{name: "empty output is not error", output: "", isError: false},
	{name: "regular output is not error", output: "file.txt\ndir/", isError: false},
	{name: "partial match is not error", output: "Process exited", isError: false},
}

// TestIsCodexToolError verifies error classification against the full case matrix.
// When adding error detection for a new adapter, create an analogous test with
// its own case slice following this pattern.
func TestIsCodexToolError(t *testing.T) {
	for _, tc := range codexToolErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			got := isCodexToolError(tc.output)
			assert.Equal(t, tc.isError, got)
		})
	}
}

// --- Reusable mergeToolEntries contract tests ---
// These verify the merge contract that any adapter using call_id correlation must satisfy.

// TestMergeToolEntries_Contract tests the core merge invariants that any
// adapter using call_id-based merging must uphold.
// When adding merge support to a new adapter, call mergeToolEntries on its
// output and verify these same invariants.
func TestMergeToolEntries_Contract(t *testing.T) {
	ts := time.Date(2026, 2, 27, 10, 0, 0, 0, time.UTC)

	// Invariant 1: merged entry has BOTH name+input AND output
	t.Run("merged entry has both name and output", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "cmd", ToolInput: "args", CallID: "c1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "error", IsError: true, CallID: "c1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 1)
		assert.NotEmpty(t, merged[0].ToolName, "merged entry must have ToolName")
		assert.NotEmpty(t, merged[0].ToolOutput, "merged entry must have ToolOutput")
	})

	// Invariant 2: non-matching CallIDs are never merged
	t.Run("different CallIDs remain separate", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "cmd", CallID: "c1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "err", IsError: true, CallID: "c2"},
		}
		merged := mergeToolEntries(entries)
		assert.Len(t, merged, 2)
	})

	// Invariant 3: merge only happens on adjacent entries
	t.Run("non-adjacent matching CallIDs are not merged", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "cmd", CallID: "c1"},
			{Timestamp: ts, Role: "assistant", Content: "intervening"},
			{Timestamp: ts, Role: "tool", ToolOutput: "err", IsError: true, CallID: "c1"},
		}
		merged := mergeToolEntries(entries)
		assert.Len(t, merged, 3, "intervening entry prevents merge")
	})

	// Invariant 4: non-tool entries pass through unchanged
	t.Run("non-tool entries unaffected", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "user", Content: "hello"},
			{Timestamp: ts, Role: "assistant", Content: "hi"},
		}
		merged := mergeToolEntries(entries)
		assert.Equal(t, entries, merged)
	})

	// Invariant 5: empty/nil input returns empty/nil
	t.Run("nil returns nil", func(t *testing.T) {
		assert.Nil(t, mergeToolEntries(nil))
	})

	// Invariant 6: single entry passes through
	t.Run("single entry unchanged", func(t *testing.T) {
		entries := []RawEntry{{Role: "tool", ToolName: "cmd"}}
		assert.Equal(t, entries, mergeToolEntries(entries))
	})
}
