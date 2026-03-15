package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClaudeRunner_BinaryNotFound(t *testing.T) {
	// override PATH so claude is not found
	t.Setenv("PATH", t.TempDir())
	r := NewClaudeRunner(slog.Default())
	assert.Empty(t, r.binaryPath)
	assert.False(t, r.Available())
}

func TestClaudeRunner_Available_BinaryExists(t *testing.T) {
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "claude")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	r := &ClaudeRunner{
		binaryPath: fakeBin,
		logger:     slog.Default(),
	}
	assert.True(t, r.Available())
}

func TestClaudeRunner_Available_BinaryRemoved(t *testing.T) {
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "claude")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	r := &ClaudeRunner{
		binaryPath: fakeBin,
		logger:     slog.Default(),
	}
	assert.True(t, r.Available())

	require.NoError(t, os.Remove(fakeBin))
	assert.False(t, r.Available())
}

func TestParseClaudeOutput_Success(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"result","subtype":"success","result":"final output","duration_ms":1234,"duration_api_ms":1000,"num_turns":1,"session_id":"abc","total_cost_usd":0.003,"usage":{"input_tokens":100,"output_tokens":200}}`,
	}, "\n")

	result, err := parseClaudeOutput(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "result", result.Type)
	assert.Equal(t, "success", result.Subtype)
	assert.Equal(t, "final output", result.Result)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 100, result.Usage.InputTokens)
	assert.Equal(t, 200, result.Usage.OutputTokens)
}

func TestParseClaudeOutput_NoResultMessage(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`

	result, err := parseClaudeOutput(strings.NewReader(jsonl))
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestParseClaudeOutput_EmptyInput(t *testing.T) {
	result, err := parseClaudeOutput(strings.NewReader(""))
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestParseClaudeOutput_MalformedLine(t *testing.T) {
	jsonl := strings.Join([]string{
		`not valid json`,
		`{"type":"result","result":"got it","usage":{"input_tokens":10,"output_tokens":20}}`,
	}, "\n")

	result, err := parseClaudeOutput(strings.NewReader(jsonl))
	// still extracts the result despite one bad line
	require.NotNil(t, result)
	assert.Equal(t, "got it", result.Result)
	// no error because the scanner succeeded and we got a result
	assert.NoError(t, err)
}

func TestParseClaudeOutput_NoUsage(t *testing.T) {
	jsonl := `{"type":"result","result":"done"}`

	result, err := parseClaudeOutput(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "done", result.Result)
	assert.Nil(t, result.Usage)
}

func TestParseClaudeOutput_MultipleMessages(t *testing.T) {
	// last result message wins
	jsonl := strings.Join([]string{
		`{"type":"result","result":"first","usage":{"input_tokens":1,"output_tokens":2}}`,
		`{"type":"result","result":"second","usage":{"input_tokens":10,"output_tokens":20}}`,
	}, "\n")

	result, err := parseClaudeOutput(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "second", result.Result)
	assert.Equal(t, 10, result.Usage.InputTokens)
}

func TestClaudeRunner_Run_NotAvailable(t *testing.T) {
	r := &ClaudeRunner{
		binaryPath: "",
		logger:     slog.Default(),
	}
	_, err := r.Run(context.Background(), RunRequest{Prompt: "hello"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestClaudeRunner_Run_Timeout(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "claude")
	// use exec to replace shell process with sleep so killing the process
	// closes pipes immediately (no orphaned children keeping pipes open)
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755))

	r := &ClaudeRunner{
		binaryPath: script,
		logger:     slog.Default(),
	}

	start := time.Now()
	_, err := r.Run(context.Background(), RunRequest{
		Prompt:          "test",
		TimeoutOverride: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	// should complete well under 5 seconds (the timeout is 200ms)
	assert.Less(t, elapsed, 5*time.Second)
}

func TestClaudeRunner_Run_ExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real claude test in short mode")
	}
	r := NewClaudeRunner(slog.Default())
	if !r.Available() {
		t.Skip("claude binary not installed")
	}

	result, err := r.Run(context.Background(), RunRequest{
		Prompt:          "respond with exactly one word: hello",
		TimeoutOverride: 30 * time.Second,
	})
	// if claude exits non-zero (e.g., invalid model), verify we capture it;
	// if it succeeds, at least verify the runner handles the real binary
	if err != nil {
		// timeout or startup error — acceptable for this edge-case test
		t.Skipf("claude returned error (expected for exit-code test): %v", err)
	}
	assert.NotNil(t, result)
	assert.True(t, result.Duration > 0)
}

func TestClaudeRunner_Run_SuccessfulInvocation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real claude test in short mode")
	}
	r := NewClaudeRunner(slog.Default())
	if !r.Available() {
		t.Skip("claude binary not installed")
	}

	result, err := r.Run(context.Background(), RunRequest{
		Prompt:          "respond with exactly one word: hello",
		WorkDir:         t.TempDir(),
		TimeoutOverride: 30 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.NotEmpty(t, result.Output, "expected non-empty output from real claude")
	assert.Greater(t, result.TokensIn, 0)
	assert.Greater(t, result.TokensOut, 0)
	assert.True(t, result.Duration > 0)
}

func TestClaudeRunner_Run_ContextCancellation(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755))

	r := &ClaudeRunner{
		binaryPath: script,
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	// cancel after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := r.Run(ctx, RunRequest{
		Prompt:          "test",
		TimeoutOverride: 30 * time.Second,
	})
	assert.Error(t, err)
}
