package agentwork

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultTimeout = 5 * time.Minute

const (
	claudeStdoutLimit = 1024 * 1024
	claudeStderrLimit = 64 * 1024
	claudePipeWait    = 500 * time.Millisecond
)

// claudeMessage represents a single JSONL message from `claude --output-format stream-json`.
type claudeMessage struct {
	Type       string       `json:"type"`
	Subtype    string       `json:"subtype,omitempty"`
	Result     string       `json:"result,omitempty"`
	Model      string       `json:"model,omitempty"` // e.g. "claude-sonnet-4-6" — preserved for attribution
	DurationMS int64        `json:"duration_ms,omitempty"`
	Usage      *claudeUsage `json:"usage,omitempty"`
}

// claudeUsage captures token counts from Claude's result message.
type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ClaudeRunner implements Runner using `claude --output-format stream-json`.
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

// cappedBuffer drains all subprocess output while retaining only the prefix
// needed for parsing or diagnostics. Returning len(p) even after the cap keeps
// a verbose child from blocking on a full pipe.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return written, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (b *cappedBuffer) String() string { return b.buf.String() }

// tailBuffer drains all writes while retaining the most recent bytes. Claude's
// stream-json result is the final line, so a prefix cap would discard the only
// message the runner needs after a verbose invocation.
type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if b.limit <= 0 {
		return written, nil
	}
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return written, nil
	}
	overflow := len(b.buf) + len(p) - b.limit
	if overflow > 0 {
		copy(b.buf, b.buf[overflow:])
		b.buf = b.buf[:len(b.buf)-overflow]
	}
	b.buf = append(b.buf, p...)
	return written, nil
}

func (b *tailBuffer) Bytes() []byte { return b.buf }

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

	// --permission-mode bypassPermissions: see the equivalent comment in
	// pkg/sessionsummary/claude.go. In `-p` mode a permission prompt has no
	// one to answer it, so the LLM narrates the block instead of answering;
	// that produced the failure-marker stubs that clobbered 31 Phase 2
	// sessions on 2026-04-25 (bd ox-5cc9, ox-91sl). An isolated run has no
	// tools, so it never reaches a prompt and does not get the bypass.
	args := []string{"--output-format", "stream-json", "--verbose"}
	isolated := false
	if req.Isolated || req.IsolateIfSupported {
		err := checkClaudeIsolation(ctx, r.binaryPath)
		switch {
		case err == nil:
			isolated = true
		case req.Isolated || !errors.As(err, new(isolationUnsupported)):
			return nil, err
		default:
			r.logger.Warn("running claude without isolation; update Claude Code", "error", err)
		}
	}
	if isolated {
		args = append(args, claudeIsolatedArgs()...)
	} else {
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	// `-p`/`--print` enables non-interactive (print) mode. The prompt itself is
	// NOT passed as the flag value — claude reads it from stdin when -p has no
	// argument. Keeping the (potentially sensitive) session transcript out of
	// argv prevents same-UID processes from reading it via ps / /proc/<pid>/cmdline
	// / sysctl kern.procargs2 (security finding #10).
	args = append(args, "-p")

	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	if isolated {
		cmd.Env = append(os.Environ(), isolatedEnv...)
	}
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	setProcAttr(cmd)
	// Let os/exec own the pipes and copy into always-draining capped writers.
	// WaitDelay closes those pipes if an orphaned descendant inherits them,
	// keeping TimeoutOverride a real upper bound instead of waiting for EOF.
	stdoutBuf := tailBuffer{limit: claudeStdoutLimit}
	stderrBuf := cappedBuffer{limit: claudeStderrLimit}
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.WaitDelay = claudePipeWait

	start := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	r.logger.Debug("claude process started", "pid", cmd.Process.Pid)

	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	if len(stderrBuf.Bytes()) > 0 {
		r.logger.Debug("claude stderr", "output", stderrBuf.String())
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
	failure := ""
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			failure = failureDetail(stderrBuf.String(), string(stdoutBuf.Bytes()))
			r.logger.Warn("claude exited with non-zero status", "exit_code", exitCode, "output", failure)
		} else {
			return nil, fmt.Errorf("wait claude: %w", waitErr)
		}
	}

	msg, parseErr := parseClaudeOutput(bytes.NewReader(stdoutBuf.Bytes()))
	if msg == nil && parseErr == nil {
		parseErr = fmt.Errorf("result message not found")
	}
	pr := struct {
		msg *claudeMessage
		err error
	}{msg: msg, err: parseErr}

	// Family-level attribution is the floor. The stream-json result
	// message carries the concrete model id (e.g., claude-sonnet-4-6);
	// when present, we overwrite below. Either way, ModelUsed is never
	// empty on any return path from this runner.
	const claudeFamily = "claude"

	if pr.err != nil && pr.msg == nil {
		result := &RunResult{
			Duration:  elapsed,
			ExitCode:  exitCode,
			ModelUsed: claudeFamily,
		}
		if exitCode != 0 {
			return result, fmt.Errorf("parse claude output (exit_code=%d output=%q): %w", exitCode, failure, pr.err)
		}
		return result, fmt.Errorf("parse claude output: %w", pr.err)
	}

	res := &RunResult{
		Duration:  elapsed,
		ExitCode:  exitCode,
		ModelUsed: claudeFamily,
	}
	if pr.msg != nil {
		res.Output = pr.msg.Result
		if pr.msg.Model != "" {
			res.ModelUsed = pr.msg.Model // concrete model id from stream-json
		}
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
