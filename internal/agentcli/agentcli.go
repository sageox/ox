package agentcli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Backend represents an AI agent CLI that can process prompts.
type Backend interface {
	// Name returns the backend identifier (e.g., "claude").
	Name() string
	// Available checks if the CLI exists in PATH.
	Available() bool
	// Run sends a prompt and returns the text output.
	Run(ctx context.Context, prompt string) (string, error)
}

// Claude implements Backend using the claude CLI in pipe mode.
type Claude struct {
	// Timeout for the claude process. Zero means no timeout (uses ctx).
	Timeout time.Duration
	// WorkDir sets the working directory for the claude process.
	// When set, relative file paths in prompts resolve from this directory.
	WorkDir string
	// Model overrides the default model (e.g., "sonnet" or "claude-sonnet-4-6").
	// Empty string uses the claude CLI default.
	Model string
}

func (c *Claude) Name() string { return "claude" }

func (c *Claude) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (c *Claude) Run(ctx context.Context, prompt string) (string, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	args := []string{"-p", "--output-format", "text", "--tools", "Read"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(prompt)
	if c.WorkDir != "" {
		cmd.Dir = c.WorkDir
	}

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			stdout := strings.TrimSpace(string(out))
			slog.Warn("claude process failed",
				"exit_code", exitErr.ExitCode(),
				"stderr", stderr,
				"stdout_len", len(stdout),
				"prompt_len", len(prompt),
				"timeout", c.Timeout,
				"workdir", c.WorkDir,
			)
			if stderr != "" {
				return "", fmt.Errorf("claude exited %d: %s", exitErr.ExitCode(), stderr)
			}
			if stdout != "" {
				return "", fmt.Errorf("claude exited %d (no stderr, stdout=%d bytes): %s", exitErr.ExitCode(), len(stdout), truncate(stdout, 200))
			}
			return "", fmt.Errorf("claude exited %d (no output, prompt was %d bytes)", exitErr.ExitCode(), len(prompt))
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out after %s (prompt was %d bytes)", c.Timeout, len(prompt))
		}
		return "", fmt.Errorf("claude: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// Detect returns the first available backend, or an error.
func Detect() (Backend, error) {
	backends := []Backend{&Claude{Timeout: 5 * time.Minute}}
	for _, b := range backends {
		if b.Available() {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no supported AI coworker CLI found (looked for: claude)")
}
