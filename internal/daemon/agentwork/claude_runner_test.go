package agentwork

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

func TestClaudeRunner_Run_TimeoutBoundsInheritedOutputPipes(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "claude")
	// The background child inherits stdout/stderr and outlives the shell when
	// CommandContext kills it. A runner that waits for EOF before Wait will
	// block for the full child sleep despite the 100ms timeout.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0o755))
	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

	start := time.Now()
	_, err := r.Run(context.Background(), RunRequest{
		Prompt:          "test",
		TimeoutOverride: 100 * time.Millisecond,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 1500*time.Millisecond,
		"an inherited output descriptor must not defeat TimeoutOverride")
}

func TestProcessCancellationKillsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group semantics")
	}
	tmp := t.TempDir()
	script := filepath.Join(tmp, "tree")
	childPIDFile := filepath.Join(tmp, "child.pid")
	body := "#!/bin/sh\nsleep 60 &\necho $! > " + childPIDFile + "\nwait\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, script)
	setProcAttr(cmd)
	require.NoError(t, cmd.Start())
	var childPID int
	require.Eventually(t, func() bool {
		rawPID, err := os.ReadFile(childPIDFile)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(rawPID)))
		return err == nil && childPID > 0
	}, 5*time.Second, 10*time.Millisecond, "process tree must become ready")

	cancel()
	require.Error(t, cmd.Wait())
	assert.Eventually(t, func() bool {
		process, findErr := os.FindProcess(childPID)
		if findErr != nil {
			return true
		}
		return process.Signal(syscall.Signal(0)) != nil
	}, 5*time.Second, 10*time.Millisecond, "cancellation must kill descendants")
}

func TestCappedBufferDrainsAfterLimit(t *testing.T) {
	buffer := cappedBuffer{limit: 4}

	n, err := buffer.Write([]byte("abcdefgh"))
	require.NoError(t, err)
	assert.Equal(t, 8, n, "writer must report the full input consumed")
	assert.Equal(t, "abcd", buffer.String())

	n, err = buffer.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "abcd", buffer.String())
}

func TestTailBufferRetainsFinalResultBeyondLimit(t *testing.T) {
	buffer := tailBuffer{limit: 128}
	_, err := buffer.Write([]byte(strings.Repeat("verbose prelude", 100)))
	require.NoError(t, err)
	_, err = buffer.Write([]byte("\n" + `{"type":"result","result":"kept","usage":{"input_tokens":1,"output_tokens":2}}` + "\n"))
	require.NoError(t, err)

	msg, err := parseClaudeOutput(bytes.NewReader(buffer.Bytes()))
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "kept", msg.Result)
}

func TestTailBufferWithZeroLimitStillDrains(t *testing.T) {
	buffer := tailBuffer{}
	n, err := buffer.Write([]byte("discarded"))
	require.NoError(t, err)
	assert.Equal(t, len("discarded"), n)
	assert.Empty(t, buffer.Bytes())
}

func TestClaudeRunner_Run_DrainsStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' 'diagnostic' >&2\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"ok\"}'\n"), 0o755))
	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

	result, err := r.Run(context.Background(), RunRequest{Prompt: "test", TimeoutOverride: 30 * time.Second})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "ok", result.Output)
}

func TestClaudeRunner_Run_MissingResultIsError(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"assistant\"}'\n"), 0o755))
	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

	// The full race+coverage suite runs eight packages concurrently and can
	// starve process scheduling on CI. Keep the fake deterministic while giving
	// it the same realistic startup budget as a normal runner invocation.
	result, err := r.Run(context.Background(), RunRequest{Prompt: "test", TimeoutOverride: 30 * time.Second})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "result message not found")
	require.NotNil(t, result)
	assert.Equal(t, "claude", result.ModelUsed)
}

// Failure prevented: a failing claude CLI (rate limit, not logged in) printed
// one short line and exited non-zero, and the daemon discarded it, leaving 38
// undiagnosable failures in two hours.
func TestClaudeRunner_Run_NonZeroExitKeepsCLIErrorText(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
		absent string
	}{
		{
			name:   "stdout when stderr is empty",
			script: "#!/bin/sh\nprintf '%s\\n' 'Claude usage limit reached'\nexit 1\n",
			want:   "Claude usage limit reached",
		},
		{
			name:   "stderr preferred and redacted",
			script: "#!/bin/sh\nprintf '%s\\n' 'not logged in glpat-abcdef1234567890XYZ' >&2\nprintf '%s\\n' 'ignored stdout'\nexit 1\n",
			want:   "not logged in",
			absent: "glpat-abcdef1234567890XYZ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := filepath.Join(t.TempDir(), "claude")
			require.NoError(t, os.WriteFile(script, []byte(tt.script), 0o755))
			r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

			result, err := r.Run(context.Background(), RunRequest{Prompt: "test", TimeoutOverride: 30 * time.Second})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			if tt.absent != "" {
				assert.NotContains(t, err.Error(), tt.absent)
			}
			require.NotNil(t, result)
			assert.Equal(t, 1, result.ExitCode)
		})
	}
}

func TestFailureDetail_CapsLength(t *testing.T) {
	t.Parallel()
	got := failureDetail("", strings.Repeat("x", failureDetailLimit*3))
	assert.LessOrEqual(t, len(got), failureDetailLimit+len("...(truncated)"))
	assert.True(t, strings.HasSuffix(got, "...(truncated)"))
}

func TestClaudeRunner_Run_ExitCode(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"partial\"}'\nexit 7\n"), 0o755))
	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

	result, err := r.Run(context.Background(), RunRequest{
		Prompt:          "test",
		TimeoutOverride: 5 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 7, result.ExitCode)
	assert.Equal(t, "partial", result.Output)
}

// TestClaudeRunner_Run_ModelFlag verifies that req.Model becomes a
// `--model <id>` pair in the argv passed to claude, and that an empty
// req.Model omits the flag entirely. Failure prevented: the cost-saving
// Haiku pin silently no-ops because the runner ignored the model field;
// or the rollback path (empty Model = user's default) regresses by
// always passing some flag.
func TestClaudeRunner_Run_ModelFlag(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	script := filepath.Join(tmp, "claude")
	// Fake claude: dump argv (one arg per line) to argsFile, then emit a
	// minimal valid stream-json result so Run() succeeds normally. It exits
	// immediately to exercise the runner's pipe-drain-before-Wait contract.
	body := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "` + argsFile + `"; done
printf '%s\n' '{"type":"result","result":"ok","usage":{"input_tokens":1,"output_tokens":1}}'
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

	cases := []struct {
		name      string
		model     string
		wantFlag  bool
		wantValue string
	}{
		{"haiku pinned", "claude-haiku-4-5", true, "claude-haiku-4-5"},
		{"empty defers to local default", "", false, ""},
		{"sonnet override", "claude-sonnet-4-6", true, "claude-sonnet-4-6"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Truncate (or create) the args file before each subtest run.
			require.NoError(t, os.WriteFile(argsFile, nil, 0o644))

			_, err := r.Run(context.Background(), RunRequest{
				Prompt: "x", Model: c.model, TimeoutOverride: 30 * time.Second,
			})
			require.NoError(t, err)

			data, err := os.ReadFile(argsFile)
			require.NoError(t, err)
			argv := strings.Split(strings.TrimSpace(string(data)), "\n")

			if c.wantFlag {
				idx := -1
				for i, a := range argv {
					if a == "--model" {
						idx = i
						break
					}
				}
				require.GreaterOrEqual(t, idx, 0, "--model flag missing; argv=%v", argv)
				require.Less(t, idx+1, len(argv), "--model has no value; argv=%v", argv)
				assert.Equal(t, c.wantValue, argv[idx+1])
			} else {
				for _, a := range argv {
					assert.NotEqual(t, "--model", a, "expected no --model flag; argv=%v", argv)
				}
			}
		})
	}
}

// TestClaudeRunner_Run_PromptViaStdin verifies the summarization prompt is
// delivered to the claude subprocess via stdin and NEVER appears as an argv
// element. argv is world-readable to same-UID processes (ps,
// /proc/<pid>/cmdline, sysctl kern.procargs2); leaking the session transcript
// there is security finding #10.
func TestClaudeRunner_Run_PromptViaStdin(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	script := filepath.Join(tmp, "claude")
	// Fake claude: dump argv (one per line) and stdin to files, then emit a
	// minimal valid stream-json result before exiting immediately.
	body := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "` + argsFile + `"; done
cat > "` + stdinFile + `"
printf '%s\n' '{"type":"result","result":"ok","usage":{"input_tokens":1,"output_tokens":1}}'
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}

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
	// -p (print mode) flag must remain
	assert.Contains(t, strings.Split(strings.TrimSpace(string(argv)), "\n"), "-p")
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
