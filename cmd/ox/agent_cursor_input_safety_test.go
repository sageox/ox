package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCursorHookInputRejectsMalformedNativeClaims(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"empty", nil, "payload is required"},
		{"oversized", bytes.Repeat([]byte(" "), (1<<20)+1), "hook-input-limit"},
		{"incomplete JSON", []byte(`{"conversation_id":`), "invalid Cursor hook payload"},
		{"array", []byte(`[]`), "invalid Cursor hook payload"},
		{"null", []byte(`null`), "conversation_id is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := normalizeCursorHookInput(test.raw, t.TempDir(), t.TempDir())
			require.ErrorContains(t, err, test.want)
			require.Nil(t, input, "rejected claims must not expose a normalized identity")
		})
	}
	for _, test := range []struct {
		name, key string
		value     any
		remove    bool
		want      string
	}{
		{"background true", "is_background_agent", true, false, "unsupported-scope"},
		{"background null", "is_background_agent", nil, false, "unsupported-scope"},
		{"background string", "is_background_agent", "false", false, "unsupported-scope"},
		{"conversation missing", "conversation_id", nil, true, "conversation_id is required"},
		{"conversation null", "conversation_id", nil, false, "conversation_id is required"},
		{"conversation number", "conversation_id", 7, false, "conversation_id is required"},
		{"conversation empty", "conversation_id", "", false, "conversation_id is required"},
		{"conversation not UUID", "conversation_id", "../other-session", false, "missing-native-identity"},
		{"session null", "session_id", nil, false, "session_id must be a string"},
		{"session number", "session_id", 7, false, "session_id must be a string"},
		{"generation null", "generation_id", nil, false, "generation_id must be a string"},
		{"generation object", "generation_id", map[string]any{}, false, "generation_id must be a string"},
		{"workspace missing", "workspace_roots", nil, true, "workspace_roots is required"},
		{"workspace null", "workspace_roots", nil, false, "exactly one workspace root"},
		{"workspace wrong type", "workspace_roots", "workspace", false, "exactly one workspace root"},
		{"workspace empty", "workspace_roots", []string{}, false, "exactly one workspace root"},
		{"workspace blank", "workspace_roots", []string{""}, false, "exactly one workspace root"},
		{"workspace relative", "workspace_roots", []string{"relative-workspace"}, false, "workspace does not match"},
		{"source number", "transcript_path", 7, false, "transcript_path must be a string"},
		{"source relative", "transcript_path", "relative.jsonl", false, "invalid-source-path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, root, home, _ := cursorInputFixture(t, "")
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			if test.remove {
				delete(fields, test.key)
			} else {
				fields[test.key] = test.value
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			input, err := normalizeCursorHookInput(raw, root, home)
			require.ErrorContains(t, err, test.want)
			require.Nil(t, input)
		})
	}
}

func TestNormalizeCursorHookInputRejectsUnavailableOrDifferentWorkspace(t *testing.T) {
	for _, location := range []string{"relative", "missing", "file", "different workspace", "unrelated source", "source symlink", "relative home", "file home", "file source parent"} {
		t.Run(location, func(t *testing.T) {
			raw, root, home, source := cursorInputFixture(t, "")
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			want := "workspace-mismatch"
			switch location {
			case "relative":
				root = "relative"
			case "missing":
				root = filepath.Join(root, "missing")
			case "file":
				root = filepath.Join(root, "file")
				require.NoError(t, os.WriteFile(root, []byte("not a directory"), 0o600))
			case "different workspace":
				fields["workspace_roots"] = []string{t.TempDir()}
			case "unrelated source":
				fields["transcript_path"] = filepath.Join(t.TempDir(), "other.jsonl")
				want = "invalid-source-path"
			case "relative home":
				home = "relative-home"
				want = "invalid-source-path"
			case "file home":
				home = filepath.Join(home, "file")
				require.NoError(t, os.WriteFile(home, []byte("not a directory"), 0o600))
				want = "invalid-source-path"
			case "file source parent":
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Dir(source)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Dir(source), []byte("not a directory"), 0o600))
				want = "invalid-source-path"
			case "source symlink":
				outside := filepath.Join(t.TempDir(), "other.jsonl")
				require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
				if err := os.Symlink(outside, source); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				want = "invalid-source-path"
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			input, err := normalizeCursorHookInput(raw, root, home)
			require.ErrorContains(t, err, want)
			require.Nil(t, input)
		})
	}
}

func TestNormalizeCursorHookInputAllowsAbsentOptionalNativeFields(t *testing.T) {
	raw, root, home, source := cursorInputFixture(t, "")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	for _, key := range []string{"session_id", "generation_id", "transcript_path"} {
		delete(fields, key)
	}
	fields["is_background_agent"] = false
	fields["session_file_hint"] = filepath.Join(t.TempDir(), "stale.jsonl")
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	input, err := normalizeCursorHookInput(raw, root, home)
	require.NoError(t, err)
	require.Equal(t, cursorConversationID, input.ConversationID)
	require.Equal(t, source, input.SourcePath)
	require.True(t, input.SourcePending)
	require.Empty(t, input.GenerationID)
	require.NoError(t, json.Unmarshal(input.NormalizedRaw, &fields))
	require.Equal(t, cursorConversationID, fields["session_id"])
	require.NotContains(t, string(input.NormalizedRaw), "session_file_hint")
}

func TestCursorHookBoundaryRejectsUnverifiableExports(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
	}{
		{"incomplete record", `{"role":"user"}`, "export is incomplete"},
		{"malformed complete record", "{\n", "invalid complete row"},
		{"oversized record", `{"text":"` + strings.Repeat("x", 10<<20) + "\"}\n", "record exceeds boundary limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, root, home, source := cursorInputFixture(t, test.content)
			input, err := normalizeCursorHookInput(raw, root, home)
			require.NoError(t, err)
			boundary, err := cursorBoundaryForHook(input, "sessionStart")
			require.ErrorContains(t, err, test.want)
			require.Equal(t, CursorSourceBoundary{}, boundary, "invalid exports cannot establish a durable source boundary")
			after, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, test.content, string(after), "rejected native exports must remain untouched")
		})
	}
	for _, input := range []*cursorNativeInput{nil, {}} {
		_, err := cursorBoundaryForHook(input, "sessionStart")
		require.ErrorContains(t, err, "missing-native-identity")
	}
	_, err := cursorBoundaryForHook(&cursorNativeInput{ConversationID: cursorConversationID}, "beforeSubmitPrompt")
	require.ErrorContains(t, err, "generation_id is required")
}

func TestCursorBoundaryRejectsMissingNonregularAndOversizedSources(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large.jsonl")
	file, err := os.Create(large)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(cursorBoundaryMaxBytes+1))
	require.NoError(t, file.Close())
	for _, test := range []struct{ path, want string }{
		{filepath.Join(root, "missing.jsonl"), "open Cursor export boundary"},
		{root, "export is not regular"},
		{large, "source-too-large"},
	} {
		_, _, err := cursorReadBoundary(test.path, "sessionStart")
		require.ErrorContains(t, err, test.want)
	}
	for _, test := range []struct {
		path   string
		offset int64
	}{
		{filepath.Join(root, "missing.jsonl"), 0},
		{root, 0},
		{large, -1},
		{large, cursorBoundaryMaxBytes + 1},
		{large, cursorBoundaryMaxBytes + 2},
	} {
		hash, err := cursorPrefixSHA256(test.path, test.offset)
		require.ErrorContains(t, err, "validate Cursor transcript boundary")
		require.Empty(t, hash)
	}
}

func TestCursorPrefixRejectsSourceThatBecomesUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions without root privileges")
	}
	_, _, _, source := cursorInputFixture(t, "{}\n")
	require.NoError(t, os.Chmod(source, 0))
	t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
	hash, err := cursorPrefixSHA256(source, 3)
	require.ErrorContains(t, err, "open Cursor transcript")
	require.Empty(t, hash)
}
