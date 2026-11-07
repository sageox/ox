package agentwork

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GeminiRunner implements Runner using `gemini` with the prompt delivered via stdin.
// It spawns Gemini CLI in non-interactive (headless) mode.
// GeminiRunner is safe for concurrent use — each Run() call is independent.
type GeminiRunner struct {
	binaryPath string
	logger     *slog.Logger
}

// NewGeminiRunner creates a GeminiRunner by resolving the `gemini` binary.
// If the binary is not found, the runner is still created but Available() returns false.
func NewGeminiRunner(logger *slog.Logger) *GeminiRunner {
	path, err := exec.LookPath("gemini")
	if err != nil {
		logger.Debug("gemini binary not found in PATH", "error", err)
		path = ""
	}
	return &GeminiRunner{
		binaryPath: path,
		logger:     logger,
	}
}

// Available reports whether the gemini binary exists on disk.
func (r *GeminiRunner) Available() bool {
	if r.binaryPath == "" {
		return false
	}
	_, err := os.Stat(r.binaryPath)
	return err == nil
}

// Run executes a gemini invocation with the given request.
// Gemini CLI outputs plain text (not JSONL), so parsing is simpler than Claude.
func (r *GeminiRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if !r.Available() {
		return nil, fmt.Errorf("gemini binary not available")
	}

	timeout := defaultTimeout
	if req.TimeoutOverride > 0 {
		timeout = req.TimeoutOverride
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var args []string
	workDir, env := req.WorkDir, workerEnv("GEMINI_", "GOOGLE_")
	if req.Isolated || req.IsolateIfSupported {
		isolation, dir, err := geminiIsolation(ctx, r.binaryPath)
		switch {
		case err == nil:
			// --skip-trust gets Gemini past its headless trust check, but only
			// this makes it load the worker directory's own settings.
			args, workDir, env = isolation, dir, append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
		case req.Isolated || !errors.As(err, new(isolationUnsupported)):
			return nil, err
		default:
			r.logger.Warn("running gemini without isolation; update Gemini CLI", "error", err)
		}
	}

	// Gemini runs non-interactively (headless) when its stdin is not a TTY and
	// reads the prompt from stdin. The prompt is fed via stdin rather than the
	// `-p` argv value so the (potentially sensitive) session transcript does not
	// appear in ps / /proc/<pid>/cmdline / sysctl kern.procargs2 (security
	// finding #10).
	if workDir == "" {
		// ox's own empty directory holds no settings to trust, and headless
		// Gemini refuses an untrusted one.
		env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
	}
	dir, cleanup, err := workerDir(workDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = env
	cmd.Dir = dir
	setProcAttr(cmd)

	// Let os/exec own the pipes: Wait closes a pipe the caller reads, so
	// reading one concurrently with Wait loses a fast process's output.
	// WaitDelay closes them if an orphaned descendant inherits them.
	stdout := cappedBuffer{limit: 1024 * 1024}
	stderr := tailBuffer{limit: 64 * 1024}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = claudePipeWait

	start := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	r.logger.Debug("gemini process started", "pid", cmd.Process.Pid)

	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	stderrBuf, output := stderr.Bytes(), stdout.Bytes()

	if len(stderrBuf) > 0 {
		r.logger.Debug("gemini stderr", "output", string(stderrBuf))
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("gemini timed out after %s: %w", timeout, ctx.Err())
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			r.logger.Warn("gemini exited with non-zero status", "exit_code", exitCode, "stderr", failureTail(string(stderrBuf)))
		} else {
			return nil, fmt.Errorf("wait gemini: %w", waitErr)
		}
	}

	// Gemini CLI doesn't yet emit the concrete model version in a parseable
	// channel. Coarse family attribution for now; refine when the CLI
	// output format exposes it (same TODO pattern as codex_runner.go).
	return &RunResult{
		Output:    string(output),
		Duration:  elapsed,
		ExitCode:  exitCode,
		ModelUsed: "gemini",
	}, nil
}
