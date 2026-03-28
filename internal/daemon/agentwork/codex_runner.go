package agentwork

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// CodexRunner implements Runner using the OpenAI Codex CLI.
// It spawns Codex in non-interactive mode with --full-auto.
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

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"--full-auto", "--quiet", "-p", req.Prompt}

	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	setProcAttr(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	start := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	r.logger.Debug("codex process started", "pid", cmd.Process.Pid)

	// read stderr in background
	var stderrBuf []byte
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf, _ = io.ReadAll(io.LimitReader(stderrPipe, 64*1024))
	}()

	// read stdout in background
	outputCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var lines []byte
		for scanner.Scan() {
			if len(lines) > 0 {
				lines = append(lines, '\n')
			}
			lines = append(lines, scanner.Bytes()...)
		}
		outputCh <- string(lines)
	}()

	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	<-stderrDone
	output := <-outputCh

	if len(stderrBuf) > 0 {
		r.logger.Debug("codex stderr", "output", string(stderrBuf))
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("codex timed out after %s: %w", timeout, ctx.Err())
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

	return &RunResult{
		Output:   output,
		Duration: elapsed,
		ExitCode: exitCode,
	}, nil
}
