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

// TestCodexRunner_Run_PromptViaStdin verifies the prompt is delivered to the
// codex subprocess via stdin and never appears as an argv element (security
// finding #10 — argv is world-readable to same-UID processes).
func TestCodexRunner_Run_PromptViaStdin(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	script := filepath.Join(tmp, "codex")
	body := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "` + argsFile + `"; done
cat > "` + stdinFile + `"
sleep 0.1
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r := &CodexRunner{binaryPath: script, logger: slog.Default()}

	const secret = "SENSITIVE-TRANSCRIPT-CONTENT-do-not-leak"
	_, err := r.Run(context.Background(), RunRequest{
		Prompt: secret, TimeoutOverride: 5 * time.Second,
	})
	require.NoError(t, err)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)

	assert.NotContains(t, string(argv), secret, "prompt leaked into argv")
	assert.Equal(t, secret, string(stdin), "prompt not delivered via stdin")
	// `-` positional must remain so codex reads the prompt from stdin
	assert.Contains(t, strings.Split(strings.TrimSpace(string(argv)), "\n"), "-")
}

func TestCodexRunnerExecContract(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	result, err := r.Run(context.Background(), RunRequest{Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "exec\n--sandbox\nread-only\n--ephemeral\n--color\nnever\n-c\nfeatures.hooks=false\n-", result.Output)
}

func TestCodexRunnerDrainsLargeStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ni=0; while [ $i -lt 10000 ]; do printf 'diagnostic progress line\\n' >&2; i=$((i+1)); done\nprintf 'complete'\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	result, err := r.Run(context.Background(), RunRequest{TimeoutOverride: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, "complete", result.Output)
}

func TestCodexRunnerCancellation(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	start := time.Now()
	_, err := r.Run(context.Background(), RunRequest{TimeoutOverride: 50 * time.Millisecond})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestBoundedCodexOutput(t *testing.T) {
	w := &boundedCodexOutput{limit: 3}
	n, err := w.Write([]byte("abcdef"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, "abc", w.buf.String())
	assert.True(t, w.overflow)
	n, err = w.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "abc", w.buf.String())
}

func TestCodexLoginStatusStreams(t *testing.T) {
	for _, tt := range []struct {
		name, script  string
		authenticated bool
	}{
		{"stdout", "printf 'Logged in using ChatGPT\\n'", true},
		{"stderr", "printf 'Logged in using ChatGPT\\n' >&2", true},
		{"failed with positive text", "printf 'Logged in using ChatGPT\\n' >&2; exit 1", false},
		{"negative", "printf 'Not logged in\\n'", false},
		{"unknown", "printf 'unrecognized status\\n'", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"+tt.script+"\n"), 0700))
			t.Setenv("PATH", dir)
			t.Setenv("OPENAI_API_KEY", "")
			got := checkCodexUsability()
			assert.True(t, got.Installed)
			assert.Equal(t, tt.authenticated, got.Authenticated)
		})
	}
}
