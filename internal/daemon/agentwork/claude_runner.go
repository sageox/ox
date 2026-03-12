package agentwork

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

const defaultTimeout = 5 * time.Minute

// claudeMessage represents a single JSONL message from `claude --jsonl`.
type claudeMessage struct {
	Type       string       `json:"type"`
	Subtype    string       `json:"subtype,omitempty"`
	Result     string       `json:"result,omitempty"`
	DurationMS int64        `json:"duration_ms,omitempty"`
	Usage      *claudeUsage `json:"usage,omitempty"`
}

// claudeUsage captures token counts from Claude's result message.
type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ClaudeRunner implements Runner using `claude --jsonl`.
// It spawns Claude Code CLI in non-interactive mode.
// ClaudeRunner is safe for concurrent use — each Run() call is independent.
type ClaudeRunner struct {
	binaryPath string
	logger     *slog.Logger
}

// NewClaudeRunner creates a ClaudeRunner by resolving the `claude` binary.
// If the binary is not found, the runner is still created but Available() returns false.
func NewClaudeRunner(logger *slog.Logger) *ClaudeRunner {
	path, err := exec.LookPath("claude")
	if err != nil {
		logger.Debug("claude binary not found in PATH", "error", err)
		path = ""
	}
	return &ClaudeRunner{
		binaryPath: path,
		logger:     logger,
	}
}

// Available reports whether the claude binary exists on disk.
func (r *ClaudeRunner) Available() bool {
	if r.binaryPath == "" {
		return false
	}
	_, err := os.Stat(r.binaryPath)
	return err == nil
}

// parseResult holds the output of the async JSONL parser.
type parseResult struct {
	msg *claudeMessage
	err error
}

// Run executes a claude invocation with the given request.
func (r *ClaudeRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if !r.Available() {
		return nil, fmt.Errorf("claude binary not available")
	}

	timeout := defaultTimeout
	if req.TimeoutOverride > 0 {
		timeout = req.TimeoutOverride
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"--jsonl", "-p", req.Prompt}
	if req.MaxTokens > 0 {
		args = append(args, "--max-tokens", fmt.Sprintf("%d", req.MaxTokens))
	}

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
		return nil, fmt.Errorf("start claude: %w", err)
	}

	r.logger.Debug("claude process started", "pid", cmd.Process.Pid)

	// read stderr in background
	var stderrBuf []byte
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf, _ = io.ReadAll(io.LimitReader(stderrPipe, 64*1024))
	}()

	// parse stdout JSONL in background so pipe reads don't block Wait
	parseCh := make(chan parseResult, 1)
	go func() {
		msg, parseErr := parseClaudeOutput(stdoutPipe)
		parseCh <- parseResult{msg: msg, err: parseErr}
	}()

	// wait for process to exit (also closes pipes, unblocking readers)
	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	// collect background readers
	<-stderrDone
	pr := <-parseCh

	if len(stderrBuf) > 0 {
		r.logger.Debug("claude stderr", "output", string(stderrBuf))
	}

	// context cancellation or timeout
	// note: exec.CommandContext already kills the process on ctx cancellation,
	// so by the time Wait() returns the process is already reaped and the PID
	// is released. Calling killProcessGroup here would risk killing an unrelated
	// process that recycled the PID.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("claude timed out after %s: %w", timeout, ctx.Err())
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			r.logger.Warn("claude exited with non-zero status", "exit_code", exitCode, "stderr", string(stderrBuf))
		} else {
			return nil, fmt.Errorf("wait claude: %w", waitErr)
		}
	}

	if pr.err != nil && pr.msg == nil {
		return &RunResult{
			Duration: elapsed,
			ExitCode: exitCode,
		}, fmt.Errorf("parse claude output: %w", pr.err)
	}

	res := &RunResult{
		Duration: elapsed,
		ExitCode: exitCode,
	}
	if pr.msg != nil {
		res.Output = pr.msg.Result
		if pr.msg.Usage != nil {
			res.TokensIn = pr.msg.Usage.InputTokens
			res.TokensOut = pr.msg.Usage.OutputTokens
		}
	}

	return res, nil
}

// parseClaudeOutput reads JSONL from stdout and extracts the result message.
func parseClaudeOutput(r io.Reader) (*claudeMessage, error) {
	scanner := bufio.NewScanner(r)
	// allow large lines (Claude can produce long outputs)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var result *claudeMessage
	var lastErr error

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			lastErr = fmt.Errorf("unmarshal jsonl line: %w", err)
			continue
		}

		if msg.Type == "result" {
			result = &msg
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan stdout: %w", err)
	}

	// only surface parse errors when no result was found;
	// malformed non-result lines are expected and not actionable
	if result != nil {
		return result, nil
	}
	return nil, lastErr
}
