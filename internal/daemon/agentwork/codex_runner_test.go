//go:build !windows

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

// Every test here executes an extensionless #!/bin/sh fixture as the codex
// binary. Windows has no execution path for one, so the file is excluded there
// rather than ported blind (.claude/rules/testing.md, "Prefer an honest skip").
// Platform-independent runner tests live in codex_output_test.go.

// TestCodexRunner_Run_PromptViaStdin verifies the prompt is delivered to the
// codex subprocess via stdin and never appears as an argv element (security
// finding #10 — argv is world-readable to same-UID processes).
func TestCodexRunner_Run_PromptViaStdin(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	script := filepath.Join(tmp, "codex")
	body := `#!/bin/sh
if [ "$2" = "--help" ]; then echo "--sandbox --ephemeral --color --config"; exit; fi
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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$2\" = \"--help\" ]; then echo \"--sandbox --ephemeral --color --config\"; exit; fi\nprintf '%s\\n' \"$@\"\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	result, err := r.Run(context.Background(), RunRequest{Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "exec\n--sandbox\nread-only\n--ephemeral\n--color\nnever\n-c\nfeatures.hooks=false\n-", result.Output)
}

func TestCodexRunnerDrainsLargeStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$2\" = \"--help\" ]; then echo \"--sandbox --ephemeral --color --config\"; exit; fi\ni=0; while [ $i -lt 10000 ]; do printf 'diagnostic progress line\\n' >&2; i=$((i+1)); done\nprintf 'complete'\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	result, err := r.Run(context.Background(), RunRequest{TimeoutOverride: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, "complete", result.Output)
}

// TestCodexRunnerCancellation: canceling Run kills a prompt-bearing child that
// is already running. The child announces itself before it is canceled, because
// the probe shares the run's deadline: a short timeout alone can expire during
// the probe and pass without ever reaching the worker.
func TestCodexRunnerCancellation(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	started := filepath.Join(dir, "worker-started")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$2\" = \"--help\" ]; then echo \"--sandbox --ephemeral --color --config\"; exit; fi\n: > \""+started+"\"\nsleep 30\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	start := time.Now()
	// The override only bounds a broken run; cancellation is what ends this one.
	_, err := r.Run(ctx, RunRequest{TimeoutOverride: 10 * time.Second})
	require.ErrorIs(t, err, context.Canceled)
	require.FileExists(t, started)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// TestCodexRunnerTimeoutBoundsCapabilityProbe: a slow `codex exec --help` must
// spend the caller's TimeoutOverride, not run ahead of it.
// Failure prevented: daemon work overruns its requested limit by the probe's
// own two-second allowance before the bounded child even starts.
func TestCodexRunnerTimeoutBoundsCapabilityProbe(t *testing.T) {
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$2\" = \"--help\" ]; then sleep 1; echo \"--sandbox --ephemeral --color --config\"; exit; fi\nprintf 'complete'\n"), 0700))
	r := &CodexRunner{binaryPath: script, logger: slog.Default()}
	start := time.Now()
	_, err := r.Run(context.Background(), RunRequest{TimeoutOverride: 50 * time.Millisecond})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "codex timed out after 50ms")
	assert.Less(t, time.Since(start), 900*time.Millisecond)
}

func TestCodexWorkerOverridesInheritedRecording(t *testing.T) {
	t.Setenv("OX_SESSION_RECORDING", "enabled")
	t.Setenv("SAGEOX_DAEMON", "true")
	script := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
if [ "$2" = "--help" ]; then echo '--sandbox --ephemeral --color --config'; exit; fi
printf '%s\n%s' "$OX_SESSION_RECORDING" "$SAGEOX_DAEMON"
`), 0700))
	runner := &CodexRunner{binaryPath: script, logger: slog.Default()}
	result, err := runner.Run(context.Background(), RunRequest{Prompt: "private"})
	require.NoError(t, err)
	assert.Equal(t, "disabled\nfalse", result.Output)
}

func TestCodexWorkerRejectsUnsupportedInstallationBeforePrompt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	marker := filepath.Join(dir, "prompt-received")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
if [ "$2" = "--help" ]; then echo '--sandbox --color --config'; exit; fi
cat > "`+marker+`"
`), 0700))
	runner := &CodexRunner{binaryPath: script, logger: slog.Default()}
	_, err := runner.Run(context.Background(), RunRequest{Prompt: "private"})
	require.ErrorContains(t, err, "requires --ephemeral")
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}
