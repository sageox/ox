package agentwork

import (
	"context"
	"time"
)

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
	MaxTokens       int
	TimeoutOverride time.Duration
}

// RunResult captures the outcome of an agent invocation.
type RunResult struct {
	Output    string
	Duration  time.Duration
	ExitCode  int
	TokensIn  int // input tokens (from structured output if available)
	TokensOut int // output tokens
}
