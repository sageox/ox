package sessionsummary

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

const invokeTimeout = 10 * time.Minute

// claudeResultMessage is the minimal structure for parsing Claude's stream-json output.
type claudeResultMessage struct {
	Type   string `json:"type"`
	Result string `json:"result,omitempty"`
}

// InvokeClaude runs `claude --output-format stream-json -p <prompt>` and returns
// the result text. Uses a 10-minute timeout. Returns an error if the claude binary
// is not found, the invocation times out, or no result message appears in output.
func InvokeClaude(ctx context.Context, prompt, workDir string) (string, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude binary not found in PATH — ensure Claude Code CLI is installed")
	}

	ctx, cancel := context.WithTimeout(ctx, invokeTimeout)
	defer cancel()

	// --permission-mode bypassPermissions: in `-p` non-interactive mode there
	// is no human to approve any tool gated on a permission prompt, so the
	// LLM stalls and narrates the block instead of doing the work.
	// Summary regen has tightly-scoped writes (one /tmp file + one
	// `ox session push-summary` exec) — the permission prompt adds no
	// safety in this context, only failure modes. Filed as bd ox-5cc9.
	cmd := exec.CommandContext(ctx, claudePath, "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "-p", prompt)
	if workDir != "" {
		cmd.Dir = workDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	// discard stderr to avoid blocking
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// scan JSONL stdout for the result message
	var resultText string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg claudeResultMessage
		if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Type == "result" {
			resultText = msg.Result
		}
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out after %s", invokeTimeout)
		}
		// non-zero exit is acceptable if we got a result
		if resultText == "" {
			return "", fmt.Errorf("claude exited with error: %w", waitErr)
		}
	}

	if resultText == "" {
		return "", fmt.Errorf("no result message in claude output")
	}

	return resultText, nil
}
