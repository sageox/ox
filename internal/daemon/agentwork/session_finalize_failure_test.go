package agentwork

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failed summarization must remain diagnosable without publishing failed output
// or replacing the saved session with a failure stub.
func TestFinalizeFailedSummaryPreservesSessionAndRedactsDiagnostic(t *testing.T) {
	var logs bytes.Buffer
	handler := NewSessionFinalizeHandlerForTest(slog.New(slog.NewJSONHandler(&logs, nil)))
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "failed-summary")
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	raw := []byte("saved session content\n")
	require.NoError(t, os.WriteFile(rawPath, raw, 0o600))
	const token = "glpat-abcdef1234567890XYZ"
	result := &RunResult{ExitCode: 1, Output: "usage limit reached " + token + " " + strings.Repeat("x", failureDetailLimit*3)}
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
		SessionDir: sessionDir, RawPath: rawPath, LedgerPath: ledgerPath,
		Missing: []string{artifactSummaryMD, artifactSummJSON, artifactSessionMD},
	}}
	require.NoError(t, handler.ProcessResult(item, result))
	saved, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, raw, saved)
	entries, err := os.ReadDir(sessionDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed output must not become summary artifacts")
	var record struct {
		Message  string `json:"msg"`
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record))
	require.Equal(t, "summarization agent exited with error, discarding output", record.Message)
	require.Equal(t, 1, record.ExitCode)
	require.Contains(t, record.Output, "usage limit reached")
	require.Contains(t, record.Output, "[REDACTED]")
	require.NotContains(t, logs.String(), token)
	require.LessOrEqual(t, len(record.Output), failureDetailLimit+len("...(truncated)"))
	require.True(t, strings.HasSuffix(record.Output, "...(truncated)"))
}
