package nativeimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an import replays months of history to a Ledger that can
// be public. A credential printed in a past session must be redacted the same
// way live capture redacts it, in tool output as well as in message text.
func TestWriteRawRedactsAndCarriesIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 3) + "f6G7h8"
	start := time.Date(2026, 9, 12, 14, 3, 10, 0, time.UTC)
	raw := []adapters.RawEntry{
		{Timestamp: start, Role: "user", Content: "why is the push rejected?"},
		{Timestamp: start.Add(time.Second), Role: "tool", ToolName: "Bash", ToolInput: `{"command":"gh auth token"}`, CallID: "toolu_1"},
		{Timestamp: start.Add(2 * time.Second), Role: "tool", ToolOutput: "not-a-token-shaped-password-123", CallID: "toolu_1"},
		{Timestamp: start.Add(3 * time.Second), Role: "tool", ToolName: "Bash", ToolInput: `{"command":"echo ` + token + `"}`, ToolOutput: token, CallID: "toolu_2"},
		{Timestamp: start.Add(4 * time.Second), Role: "assistant", Content: "The credential helper returned " + token},
	}
	h := RawHeader{
		SessionID: "ses_1106b069-bd55-538d-9919-65adbb9337f2", AgentType: "claude-code", RepoID: "repo_x",
		Username: "devon", NativeID: claudeID, StartedAt: start, StoppedAt: start.Add(time.Hour),
	}
	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")

	n, err := WriteRaw(rawPath, "", h, raw)
	require.NoError(t, err)
	assert.Equal(t, len(raw), n)

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	text := string(data)
	assert.NotContains(t, text, token, "token-shaped secrets are redacted in text, tool input and tool output")
	assert.NotContains(t, text, "not-a-token-shaped-password-123", "a credential command's output is replaced whole")
	assert.Contains(t, text, "[REDACTED:credential-output:")
	assert.True(t, session.HasSubstantiveEntries(rawPath))

	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	require.NotNil(t, stored.Meta)
	assert.Equal(t, h.SessionID, stored.Meta.SessionID)
	require.Len(t, stored.Meta.NativeSessions, 1)
	assert.Equal(t, NativeSourceImport, stored.Meta.NativeSessions[0].Source)
	assert.Equal(t, claudeID, stored.Meta.NativeSessions[0].ID)
	assert.Len(t, stored.Entries, len(raw))
}

func TestWriteRawRefusesAnInvalidPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte("```redact\nregex \"ACME-[\" -> [X]\n```\n"), 0o644))
	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")

	_, err := WriteRaw(rawPath, root, RawHeader{NativeID: claudeID}, []adapters.RawEntry{{Role: "user", Content: "hi"}})
	require.ErrorContains(t, err, "invalid redaction policy")
	assert.NoFileExists(t, rawPath, "nothing is written under a policy the writer rejects")
}
