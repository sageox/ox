package agentwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CodexRunner implements Runner using the OpenAI Codex CLI.
// It spawns Codex in non-interactive mode through codex exec with read-only tools.
// CodexRunner is safe for concurrent use — each Run() call is independent.
type CodexRunner struct {
	binaryPath string
	logger     *slog.Logger
}

// NewCodexRunner creates a CodexRunner by resolving the `codex` binary.
// If the binary is not found, the runner is still created but Available() returns false.
func NewCodexRunner(logger *slog.Logger) *CodexRunner {
	path, err := exec.LookPath("codex")
	if err != nil {
		logger.Debug("codex binary not found in PATH", "error", err)
		path = ""
	}
	return &CodexRunner{
		binaryPath: path,
		logger:     logger,
	}
}

// Available reports whether the codex binary exists on disk.
func (r *CodexRunner) Available() bool {
	if r.binaryPath == "" {
		return false
	}
	_, err := os.Stat(r.binaryPath)
	return err == nil
}

// Run executes a codex invocation with the given request.
func (r *CodexRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if !r.Available() {
		return nil, fmt.Errorf("codex binary not available")
	}

	timeout := defaultTimeout
	if req.TimeoutOverride > 0 {
		timeout = req.TimeoutOverride
	}

	// The probe spends the caller's budget too: TimeoutOverride bounds the whole
	// Run, not only the prompt-bearing child.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := r.checkCapabilities(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codex timed out after %s: %w", timeout, ctx.Err())
		}
		return nil, err
	}

	// The prompt is fed via stdin rather than as an argv element so the
	// (potentially sensitive) session transcript does not appear in
	// ps / /proc/<pid>/cmdline / sysctl kern.procargs2 (security finding #10).
	// `-` as the positional prompt tells codex to read the prompt from stdin.
	args := []string{"exec", "--sandbox", "read-only", "--ephemeral", "--color", "never", "-c", "features.hooks=false", "-"}

	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	// Hooks are disabled above; recording is disabled independently because
	// repository instructions may still ask the worker to run ox agent prime.
	cmd.Env = append(os.Environ(), "OX_SESSION_RECORDING=disabled", "SAGEOX_DAEMON=false")
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	setProcAttr(cmd)

	// Drain both streams even after their limits: stopping a pipe reader can
	// deadlock a verbose child. Reject truncated summaries rather than publish them.
	stdout := &boundedCodexOutput{limit: 8 * 1024 * 1024}
	stderr := &boundedCodexOutput{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	start := time.Now()
	waitErr := cmd.Run()
	elapsed := time.Since(start)
	stderrBuf := stderr.buf.Bytes()
	output := strings.TrimSpace(stdout.buf.String())

	if ctx.Err() != nil {
		return nil, fmt.Errorf("codex timed out after %s: %w", timeout, ctx.Err())
	}

	if stdout.overflow {
		return nil, fmt.Errorf("codex output exceeds %d bytes", stdout.limit)
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			r.logger.Warn("codex exited with non-zero status", "exit_code", exitCode, "stderr", string(stderrBuf))
		} else {
			return nil, fmt.Errorf("wait codex: %w", waitErr)
		}
	}

	// Codex CLI does not emit the model it used in a parseable channel today.
	// Attribution stays empty; if/when codex exposes the model in its output
	// format, populate ModelUsed here (mirrors claude_runner.go which reads
	// it from the stream-json result message).
	return &RunResult{
		Output:    output,
		Duration:  elapsed,
		ExitCode:  exitCode,
		ModelUsed: "codex", // best-effort — coarse family attribution pending real CLI signal
	}, nil
}

// codexProbeTimeout bounds `codex exec --help` on its own, in addition to the
// caller's budget.
//
// It was 2s, which is ample for a help screen on an idle machine and a false
// negative on a busy one: during a full `make test-release` this package runs
// beside cmd/ox and ledger, process spawn alone exceeded it, and the probe was
// killed. A probe that fails under load tells the user their Codex install is
// broken when nothing is wrong with it. A variable so tests can shrink it.
var codexProbeTimeout = 10 * time.Second

// Probe without session content before sending a prompt. Never retry with broader
// permissions when an older installation lacks the isolation flags we require.
func (r *CodexRunner) checkCapabilities(ctx context.Context) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, codexProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.binaryPath, "exec", "--help")
	output := &boundedCodexOutput{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = time.Second
	setProcAttr(cmd)
	if err := cmd.Run(); err != nil {
		// "We could not ASK" is not "Codex answered and lacks the flag".
		// exec.CommandContext kills the child when the probe's own deadline
		// expires, and cmd.Run then returns "signal: killed" — which wraps
		// nothing. Reporting that as a capability failure told a user on a
		// loaded machine to go update a Codex that was perfectly fine, and hid
		// the real cause. The parent's own cancellation/timeout is left to the
		// caller, which already wraps ctx.Err().
		if parent.Err() == nil && ctx.Err() != nil {
			return fmt.Errorf("could not verify Codex worker isolation within %s (is the machine under heavy load?): %w",
				codexProbeTimeout, ctx.Err())
		}
		return fmt.Errorf("cannot verify Codex worker isolation; update Codex and retry: %w", err)
	}
	if output.overflow {
		return fmt.Errorf("cannot verify Codex worker isolation: help output exceeds limit")
	}
	help := output.buf.String()
	for _, flag := range []string{"--sandbox", "--ephemeral", "--color", "--config"} {
		if !strings.Contains(help, flag) {
			return fmt.Errorf("codex worker requires %s; update Codex and retry", flag)
		}
	}
	return nil
}

// boundedCodexOutput keeps memory bounded while continuing to drain child output.
type boundedCodexOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedCodexOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.buf.Len()
	if len(p) > remaining {
		w.overflow = true
		p = p[:remaining]
	}
	_, _ = w.buf.Write(p)
	return n, nil
}
