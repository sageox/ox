package agentwork

import (
	"context"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
)

// failureDetailLimit bounds how much of a failed CLI's output is carried into
// errors and logs; the CLI's own error line is short, anything longer is noise.
const failureDetailLimit = 2048

// failureDetail returns the first failureDetailLimit bytes of stderr (stdout
// when stderr is empty), redacted. The claude CLI often reports its failure
// (rate limit, not logged in) as one short line on stdout with an empty stderr.
func failureDetail(stderr, stdout string) string {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	detail = logger.RedactSecrets(detail)
	if len(detail) > failureDetailLimit {
		detail = detail[:failureDetailLimit] + "...(truncated)"
	}
	return detail
}

// Runner spawns and manages agent CLI processes.
type Runner interface {
	// Run executes an agent with the given request and returns the result.
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
	// Available reports whether the runner's backing agent CLI is installed and reachable.
	Available() bool
}

// RunRequest describes a single agent invocation.
type RunRequest struct {
	Prompt          string
	WorkDir         string
	TimeoutOverride time.Duration
	// Model pins a specific model for the runner (e.g., "claude-haiku-4-5").
	// Empty means defer to the runner's default — for ClaudeRunner that's
	// whatever the local Claude Code CLI selects, typically Sonnet. Set
	// explicitly for cost-sensitive workloads like summarization.
	Model string
	// SkipLLM bypasses the LLM runner entirely. The manager calls
	// ProcessResult directly with an empty RunResult. Use this when
	// the work item has all the information it needs to proceed without
	// generating a prompt (e.g., upload-only session recovery).
	SkipLLM bool
	// Isolated runs the CLI for untrusted input, such as an imported native
	// transcript: no tools, hooks, plugins, MCP servers, project instructions
	// or user config, no persisted session, and recording off. A CLI too old
	// to isolate fails the run; it is never retried with broader permissions.
	Isolated bool
	// IsolateIfSupported isolates the run when this CLI can, and otherwise runs
	// it as the daemon always has, so a CLI too old to isolate keeps working.
	IsolateIfSupported bool
}

// RunResult captures the outcome of an agent invocation.
type RunResult struct {
	Output    string
	Duration  time.Duration
	ExitCode  int
	TokensIn  int    // input tokens (from structured output if available)
	TokensOut int    // output tokens
	ModelUsed string // model identifier the runner reports (for attribution in logs and judge verdicts)
}

// TelemetryRecorder is the minimal surface agentwork needs to emit telemetry.
// Implemented by *daemon.TelemetryCollector. The interface lives here so the
// agentwork package doesn't import daemon (which would create a cycle: daemon
// already imports agentwork).
//
// Record must be safe for concurrent use and non-blocking — handlers fire
// telemetry from the LLM completion path and cannot afford to wait on a
// network round-trip.
type TelemetryRecorder interface {
	Record(event string, props map[string]any)
}
