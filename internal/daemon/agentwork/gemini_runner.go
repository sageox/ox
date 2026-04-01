package agentwork

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// GeminiRunner implements Runner using `gemini -p`.
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

	args := []string{"-p", req.Prompt}

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
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	r.logger.Debug("gemini process started", "pid", cmd.Process.Pid)

	// read stderr in background
	var stderrBuf []byte
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf, _ = io.ReadAll(io.LimitReader(stderrPipe, 64*1024))
	}()

	// read stdout (gemini outputs plain text)
	outputDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(stdoutPipe, 1024*1024))
		outputDone <- data
	}()

	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	<-stderrDone
	output := <-outputDone

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
			r.logger.Warn("gemini exited with non-zero status", "exit_code", exitCode, "stderr", string(stderrBuf))
		} else {
			return nil, fmt.Errorf("wait gemini: %w", waitErr)
		}
	}

	return &RunResult{
		Output:   string(output),
		Duration: elapsed,
		ExitCode: exitCode,
	}, nil
}
