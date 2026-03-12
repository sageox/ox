package agentcli

import (
	"context"
	"errors"
	"fmt"
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

	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "text", "--tools", "Read")
	cmd.Stdin = strings.NewReader(prompt)
	if c.WorkDir != "" {
		cmd.Dir = c.WorkDir
	}

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("claude exited %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("claude: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
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
