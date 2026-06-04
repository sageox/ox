//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- readSettingsFileRaw ---

func TestReadSettingsFileRaw_NonexistentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	settings, rawMap, err := readSettingsFileRaw(path)
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, settings.Hooks)
	assert.NotNil(t, rawMap)
	assert.Empty(t, rawMap)
}

func TestReadSettingsFileRaw_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))

	settings, rawMap, err := readSettingsFileRaw(path)
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, settings.Hooks)
	assert.NotNil(t, rawMap)
}

func TestReadSettingsFileRaw_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json}"), 0644))

	_, _, err := readSettingsFileRaw(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse settings file")
}

func TestReadSettingsFileRaw_PreservesNonHookKeys(t *testing.T) {
	content := `{
		"permissions": {"allow": ["Read"]},
		"hooks": {
			"SessionStart": [{"matcher": "", "hooks": [{"command": "echo hi", "type": "command"}]}]
		},
		"custom_key": 42
	}`
	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	settings, rawMap, err := readSettingsFileRaw(path)
	require.NoError(t, err)

	// hooks parsed into typed struct
	assert.Len(t, settings.Hooks["SessionStart"], 1)
	assert.Equal(t, "echo hi", settings.Hooks["SessionStart"][0].Hooks[0].Command)

	// non-hook keys preserved in rawMap
	assert.Contains(t, rawMap, "permissions")
	assert.Contains(t, rawMap, "custom_key")
}

func TestReadSettingsFileRaw_NoHooksKey(t *testing.T) {
	content := `{"permissions": {"allow": ["Read"]}}`
	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	settings, rawMap, err := readSettingsFileRaw(path)
	require.NoError(t, err)
	assert.NotNil(t, settings.Hooks)
	assert.Empty(t, settings.Hooks)
	assert.Contains(t, rawMap, "permissions")
}

// --- writeSettingsFileRaw ---

func TestWriteSettingsFileRaw_CreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "settings.json")

	settings := &ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{
			"SessionStart": {{
				Matcher: "",
				Hooks:   []ClaudeHook{{Command: "test", Type: "command"}},
			}},
		},
	}

	err := writeSettingsFileRaw(path, settings, nil, 0644)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "test")
}

func TestWriteSettingsFileRaw_PreservesNonHookKeys(t *testing.T) {
	rawMap := map[string]json.RawMessage{
		"permissions": json.RawMessage(`{"allow":["Read","Write"]}`),
	}
	settings := &ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{
			"SessionStart": {{
				Matcher: "",
				Hooks:   []ClaudeHook{{Command: "ox hook", Type: "command"}},
			}},
		},
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	err := writeSettingsFileRaw(path, settings, rawMap, 0644)
	require.NoError(t, err)

	// read back and verify both keys present
	readSettings, readRaw, err := readSettingsFileRaw(path)
	require.NoError(t, err)
	assert.Len(t, readSettings.Hooks["SessionStart"], 1)
	assert.Contains(t, readRaw, "permissions")
}

func TestWriteSettingsFileRaw_EmptyHooksDeletesKey(t *testing.T) {
	rawMap := map[string]json.RawMessage{
		"permissions": json.RawMessage(`{"allow":["Read"]}`),
		"hooks":       json.RawMessage(`{}`),
	}
	settings := &ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{}, // empty
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	err := writeSettingsFileRaw(path, settings, rawMap, 0644)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// "hooks" key should be removed from output
	assert.NotContains(t, string(data), `"hooks"`)
	// but permissions should remain
	assert.Contains(t, string(data), `"permissions"`)
}

// --- readSettingsFileRaw + writeSettingsFileRaw roundtrip ---

func TestSettingsFileRaw_Roundtrip(t *testing.T) {
	original := `{
  "permissions": {"allow": ["Read", "Write"]},
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"command": "echo start", "type": "command"}]},
      {"matcher": "test", "hooks": [{"command": "echo test", "type": "command"}]}
    ]
  },
  "other": "value"
}`
	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	settings, rawMap, err := readSettingsFileRaw(path)
	require.NoError(t, err)

	// write back without modifications
	outPath := filepath.Join(t.TempDir(), "out.json")
	err = writeSettingsFileRaw(outPath, settings, rawMap, 0644)
	require.NoError(t, err)

	// re-read and verify structural equivalence
	settings2, rawMap2, err := readSettingsFileRaw(outPath)
	require.NoError(t, err)
	assert.Equal(t, len(settings.Hooks), len(settings2.Hooks))
	assert.Equal(t, len(rawMap), len(rawMap2))
	assert.Contains(t, rawMap2, "other")
}

// --- isOxPrimeCommand ---

func TestIsOxPrimeCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{oxPrimeCommand, true},
		{oxPrimeLegacy, true},
		{"AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true", true},
		{"if command -v ox; then ox agent prime; fi", true},
		{"echo hello", false},
		{"ox agent hook SessionStart", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			assert.Equal(t, tt.want, isOxPrimeCommand(tt.cmd))
		})
	}
}

// --- isOxHookCommand ---

func TestIsOxHookCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"ox agent hook SessionStart", true},
		{"if command -v ox; then ox agent hook Stop 2>&1 || true; fi", true},
		{"AGENT_ENV=claude-code ox agent hook PreCompact", true},
		{"ox agent prime", false},
		{"echo hello", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			assert.Equal(t, tt.want, isOxHookCommand(tt.cmd))
		})
	}
}

// --- isAnyOxCommand ---

func TestIsAnyOxCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{oxPrimeCommand, true},
		{"ox agent hook SessionStart", true},
		{"echo hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			assert.Equal(t, tt.want, isAnyOxCommand(tt.cmd))
		})
	}
}

// --- hasOxPrimeHook / hasAnyOxHook ---

func TestHasOxPrimeHook(t *testing.T) {
	tests := []struct {
		name  string
		entry ClaudeHookEntry
		want  bool
	}{
		{
			"has prime hook",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: oxPrimeCommand, Type: hookType},
			}},
			true,
		},
		{
			"has legacy prime hook",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: oxPrimeLegacy, Type: hookType},
			}},
			true,
		},
		{
			"has hook command but not prime",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: "ox agent hook SessionStart", Type: hookType},
			}},
			false,
		},
		{
			"wrong type ignored",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: oxPrimeCommand, Type: "notification"},
			}},
			false,
		},
		{
			"empty hooks",
			ClaudeHookEntry{Hooks: []ClaudeHook{}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasOxPrimeHook(tt.entry))
		})
	}
}

func TestHasAnyOxHook(t *testing.T) {
	tests := []struct {
		name  string
		entry ClaudeHookEntry
		want  bool
	}{
		{
			"prime hook counts",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: oxPrimeCommand, Type: hookType},
			}},
			true,
		},
		{
			"hook command counts",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: "ox agent hook Stop", Type: hookType},
			}},
			true,
		},
		{
			"non-ox command",
			ClaudeHookEntry{Hooks: []ClaudeHook{
				{Command: "echo hello", Type: hookType},
			}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasAnyOxHook(tt.entry))
		})
	}
}

// --- removeOxPrimeHook / removeAnyOxHook ---

func TestRemoveOxPrimeHook(t *testing.T) {
	entry := ClaudeHookEntry{
		Hooks: []ClaudeHook{
			{Command: "echo before", Type: hookType},
			{Command: oxPrimeCommand, Type: hookType},
			{Command: oxPrimeLegacy, Type: hookType},
			{Command: "echo after", Type: hookType},
		},
	}
	removeOxPrimeHook(&entry)

	// only non-prime hooks remain
	assert.Len(t, entry.Hooks, 2)
	assert.Equal(t, "echo before", entry.Hooks[0].Command)
	assert.Equal(t, "echo after", entry.Hooks[1].Command)
}

func TestRemoveOxPrimeHook_PreservesNonCommandType(t *testing.T) {
	// a prime command with type "notification" should NOT be removed
	entry := ClaudeHookEntry{
		Hooks: []ClaudeHook{
			{Command: oxPrimeCommand, Type: "notification"},
		},
	}
	removeOxPrimeHook(&entry)
	assert.Len(t, entry.Hooks, 1)
}

func TestRemoveAnyOxHook(t *testing.T) {
	entry := ClaudeHookEntry{
		Hooks: []ClaudeHook{
			{Command: "echo user-hook", Type: hookType},
			{Command: oxPrimeCommand, Type: hookType},
			{Command: "ox agent hook SessionStart", Type: hookType},
			{Command: "echo another", Type: hookType},
		},
	}
	removeAnyOxHook(&entry)

	assert.Len(t, entry.Hooks, 2)
	assert.Equal(t, "echo user-hook", entry.Hooks[0].Command)
	assert.Equal(t, "echo another", entry.Hooks[1].Command)
}

// --- mergeHookEntries ---

func TestMergeHookEntries_EmptyExisting(t *testing.T) {
	newEntries := []ClaudeHookEntry{{
		Matcher: "",
		Hooks:   []ClaudeHook{{Command: "ox agent hook SessionStart", Type: hookType}},
	}}

	result := mergeHookEntries(nil, newEntries)
	require.Len(t, result, 1)
	assert.Equal(t, "", result[0].Matcher)
	assert.Len(t, result[0].Hooks, 1)
}

func TestMergeHookEntries_PreservesUserHooksSameMatcher(t *testing.T) {
	existing := []ClaudeHookEntry{{
		Matcher: "",
		Hooks: []ClaudeHook{
			{Command: "echo user-hook", Type: hookType},
			{Command: oxPrimeLegacy, Type: hookType}, // old ox hook to be replaced
		},
	}}
	newEntries := []ClaudeHookEntry{{
		Matcher: "",
		Hooks:   []ClaudeHook{{Command: "ox agent hook SessionStart", Type: hookType}},
	}}

	result := mergeHookEntries(existing, newEntries)
	require.Len(t, result, 1)

	// user hook preserved, old ox stripped, new ox added
	commands := make([]string, len(result[0].Hooks))
	for i, h := range result[0].Hooks {
		commands[i] = h.Command
	}
	assert.Contains(t, commands, "echo user-hook")
	assert.Contains(t, commands, "ox agent hook SessionStart")
	assert.NotContains(t, commands, oxPrimeLegacy)
}

func TestMergeHookEntries_DropsOldPureOxEntryWithDifferentMatcher(t *testing.T) {
	// old format used specific matchers like "startup" for ox hooks
	existing := []ClaudeHookEntry{
		{
			Matcher: "startup",
			Hooks:   []ClaudeHook{{Command: oxPrimeLegacy, Type: hookType}},
		},
		{
			Matcher: "user-matcher",
			Hooks:   []ClaudeHook{{Command: "echo user-stuff", Type: hookType}},
		},
	}
	newEntries := []ClaudeHookEntry{{
		Matcher: "",
		Hooks:   []ClaudeHook{{Command: "ox agent hook SessionStart", Type: hookType}},
	}}

	result := mergeHookEntries(existing, newEntries)

	// "startup" entry (pure ox) should be dropped
	// "user-matcher" entry should be preserved
	// "" entry (new) should be added
	assert.Len(t, result, 2)

	matchers := make([]string, len(result))
	for i, e := range result {
		matchers[i] = e.Matcher
	}
	assert.Contains(t, matchers, "user-matcher")
	assert.Contains(t, matchers, "")
	assert.NotContains(t, matchers, "startup")
}

func TestMergeHookEntries_MixedOldMatcherWithUserHooksKept(t *testing.T) {
	// old matcher entry has both ox and user hooks — keep user hooks
	existing := []ClaudeHookEntry{{
		Matcher: "compact",
		Hooks: []ClaudeHook{
			{Command: oxPrimeLegacy, Type: hookType},
			{Command: "echo user-compact-hook", Type: hookType},
		},
	}}
	newEntries := []ClaudeHookEntry{{
		Matcher: "",
		Hooks:   []ClaudeHook{{Command: "ox agent hook PreCompact", Type: hookType}},
	}}

	result := mergeHookEntries(existing, newEntries)

	// "compact" entry kept (has user hooks), new "" entry added
	assert.Len(t, result, 2)
}

// --- oxHookCommandForEvent ---

func TestOxHookCommandForEvent(t *testing.T) {
	for _, event := range claudeLifecycleEvents {
		cmd := oxHookCommandForEvent(event)
		assert.Contains(t, cmd, event, "command should contain the event name")
		assert.Contains(t, cmd, "ox agent hook", "command should invoke ox agent hook")
		assert.Contains(t, cmd, "AGENT_ENV=claude-code", "command should set AGENT_ENV")
		assert.Contains(t, cmd, "command -v ox", "command should guard against missing ox")
	}
}

// --- appendRedactedEntries ---

func TestAppendRedactedEntries_ToolFields(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:       session.EntryTypeUser,
			Content:    "run a test",
			Timestamp:  time.Now(),
			ToolName:   "Bash",
			ToolInput:  "make test",
			ToolOutput: "PASS",
		},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "Bash", parsed["tool_name"])
	assert.Equal(t, "make test", parsed["tool_input"])
	assert.Equal(t, "PASS", parsed["tool_output"])
}

func TestAppendRedactedEntries_OmitsEmptyToolFields(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:      session.EntryTypeAssistant,
			Content:   "here is the answer",
			Timestamp: time.Now(),
			// no tool fields
		},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "tool_name")
	assert.NotContains(t, string(data), "tool_input")
	assert.NotContains(t, string(data), "tool_output")
	assert.NotContains(t, string(data), "is_error")
}

func TestAppendRedactedEntries_WritesIsError(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:       session.EntryTypeTool,
			ToolName:   "Bash",
			ToolInput:  `{"command":"make build"}`,
			ToolOutput: "exit code 1: compilation failed",
			IsError:    true,
			Timestamp:  time.Now(),
		},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "Bash", parsed["tool_name"])
	assert.Equal(t, "exit code 1: compilation failed", parsed["tool_output"])
	assert.Equal(t, true, parsed["is_error"])
}

func TestAppendRedactedEntries_OmitsIsErrorWhenFalse(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:      session.EntryTypeTool,
			ToolName:  "Read",
			ToolInput: "/path/to/file",
			Timestamp: time.Now(),
		},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "is_error")
}

func TestAppendRedactedEntries_MultipleEntriesAppend(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	batch1 := []session.Entry{
		{Type: session.EntryTypeUser, Content: "first", Timestamp: time.Now()},
	}
	batch2 := []session.Entry{
		{Type: session.EntryTypeAssistant, Content: "second", Timestamp: time.Now()},
		{Type: session.EntryTypeUser, Content: "third", Timestamp: time.Now()},
	}

	require.NoError(t, appendRedactedEntries(rawPath, batch1))
	require.NoError(t, appendRedactedEntries(rawPath, batch2))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 3)
}

func TestAppendRedactedEntries_EmptySliceNoops(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	require.NoError(t, appendRedactedEntries(rawPath, []session.Entry{}))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	// file created but empty (only fsync'd, no content written)
	assert.Empty(t, strings.TrimSpace(string(data)))
}

// --- cleanupLocalSettingsOxHooks ---

func TestCleanupLocalSettingsOxHooks_RemovesOxKeepsUserHooks(t *testing.T) {
	gitRoot := t.TempDir()
	localPath := getLocalClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0755))

	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"command": oxPrimeLegacy, "type": hookType},
						{"command": "echo user-hook", "type": hookType},
					},
				},
			},
		},
		"permissions": map[string]any{"allow": []string{"Read"}},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(localPath, data, 0644))

	err = cleanupLocalSettingsOxHooks(gitRoot)
	require.NoError(t, err)

	// file should still exist (has user hooks + permissions)
	readSettings, _, err := readSettingsFileRaw(localPath)
	require.NoError(t, err)
	require.Len(t, readSettings.Hooks["SessionStart"], 1)
	assert.Equal(t, "echo user-hook", readSettings.Hooks["SessionStart"][0].Hooks[0].Command)
}

func TestCleanupLocalSettingsOxHooks_DeletesFileWhenEmpty(t *testing.T) {
	gitRoot := t.TempDir()
	localPath := getLocalClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0755))

	// file with only ox hooks, no other keys
	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"command": oxPrimeLegacy, "type": hookType},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(localPath, data, 0644))

	err = cleanupLocalSettingsOxHooks(gitRoot)
	require.NoError(t, err)

	// file should be deleted
	_, statErr := os.Stat(localPath)
	assert.True(t, os.IsNotExist(statErr), "file should be deleted when only ox hooks existed")
}

func TestCleanupLocalSettingsOxHooks_NoopsWhenNoHooks(t *testing.T) {
	gitRoot := t.TempDir()
	localPath := getLocalClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0755))

	// file with no hooks at all
	require.NoError(t, os.WriteFile(localPath, []byte(`{"permissions":{}}`), 0644))

	err := cleanupLocalSettingsOxHooks(gitRoot)
	require.NoError(t, err)

	// file should still exist unchanged
	_, statErr := os.Stat(localPath)
	assert.NoError(t, statErr)
}

// --- hasLocalSettingsOxHooks ---

func TestHasLocalSettingsOxHooks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			"has ox hook",
			`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"command":"` + oxPrimeLegacy + `","type":"command"}]}]}}`,
			true,
		},
		{
			"no ox hooks",
			`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"command":"echo hi","type":"command"}]}]}}`,
			false,
		},
		{
			"empty hooks",
			`{"hooks":{}}`,
			false,
		},
		{
			"no file",
			"", // will use nonexistent path
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRoot := t.TempDir()
			if tt.content != "" {
				localPath := getLocalClaudeSettingsPath(gitRoot)
				require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0755))
				require.NoError(t, os.WriteFile(localPath, []byte(tt.content), 0644))
			}
			assert.Equal(t, tt.want, hasLocalSettingsOxHooks(gitRoot))
		})
	}
}

// --- InstallProjectClaudeHooks + HasProjectClaudeHooks ---

func TestInstallAndHasProjectClaudeHooks(t *testing.T) {
	gitRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(gitRoot, ".claude"), 0755))

	// before install
	assert.False(t, HasProjectClaudeHooks(gitRoot))

	// install
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.True(t, HasProjectClaudeHooks(gitRoot))

	// verify all lifecycle events have hooks
	status := listProjectClaudeHooks(gitRoot)
	for _, event := range claudeLifecycleEvents {
		assert.True(t, status[event], "event %s should have hook installed", event)
	}
}

func TestInstallProjectClaudeHooks_PreservesExistingPermissions(t *testing.T) {
	gitRoot := t.TempDir()
	claudeDir := filepath.Join(gitRoot, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// write settings with permissions key
	original := `{
		"permissions": {"allow": ["Read", "Write", "Bash"]},
		"hooks": {}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(original), 0644))

	require.NoError(t, InstallProjectClaudeHooks(gitRoot))

	// read back raw to verify permissions survived
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "permissions", "permissions key must survive hook installation")

	var perms map[string]any
	require.NoError(t, json.Unmarshal(raw["permissions"], &perms))
	allow, ok := perms["allow"].([]any)
	require.True(t, ok)
	assert.Len(t, allow, 3)
}

// TestPostToolUseHook_ManagedForPlanExitNudge documents and locks in the
// dependency the plan-exit enrichment nudge relies on: the PostToolUse event
// MUST be part of the managed ox hook install (that is the event Claude Code
// fires after ExitPlanMode), MUST be idempotent (re-install adds no duplicate),
// and MUST be removable via the managed uninstall path.
// Failure prevented: someone drops PostToolUse from claudeLifecycleEvents and
// the plan-exit nudge silently stops firing, or a non-removable hook leaks.
func TestPostToolUseHook_ManagedForPlanExitNudge(t *testing.T) {
	gitRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(gitRoot, ".claude"), 0o755))

	// install — PostToolUse must be present (carrier of the plan-exit signal)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.True(t, listProjectClaudeHooks(gitRoot)["PostToolUse"],
		"PostToolUse hook must be installed — it carries the ExitPlanMode plan-exit nudge")

	// idempotent — re-install adds no duplicate ox hook on the event
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	settings, err := readProjectClaudeSettings(gitRoot)
	require.NoError(t, err)
	oxCount := 0
	for _, entry := range settings.Hooks["PostToolUse"] {
		for _, h := range entry.Hooks {
			if isAnyOxCommand(h.Command) {
				oxCount++
			}
		}
	}
	assert.Equal(t, 1, oxCount, "re-install must not duplicate the PostToolUse ox hook")

	// the installed hook routes to `ox agent hook PostToolUse`, where the
	// plan-exit branch lives
	assert.Contains(t, settings.Hooks["PostToolUse"][0].Hooks[0].Command, "hook PostToolUse")

	// removable — the managed remove primitive strips the PostToolUse ox hook,
	// leaving the event clean. This is the same primitive doctor cleanup uses.
	for i := range settings.Hooks["PostToolUse"] {
		removeAnyOxHook(&settings.Hooks["PostToolUse"][i])
	}
	remaining := 0
	for _, entry := range settings.Hooks["PostToolUse"] {
		for _, h := range entry.Hooks {
			if isAnyOxCommand(h.Command) {
				remaining++
			}
		}
	}
	assert.Equal(t, 0, remaining, "PostToolUse ox hook must be removable via the managed remove primitive")
}

// --- getSharedClaudeSettingsPath / getLocalClaudeSettingsPath ---

func TestSettingsPathHelpers(t *testing.T) {
	gitRoot := "/tmp/myrepo"
	assert.Equal(t, "/tmp/myrepo/.claude/settings.json", getSharedClaudeSettingsPath(gitRoot))
	assert.Equal(t, "/tmp/myrepo/.claude/settings.local.json", getLocalClaudeSettingsPath(gitRoot))
}
