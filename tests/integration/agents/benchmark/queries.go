//go:build integration

package benchmark

import (
	"strings"
)

// BenchmarkQuery defines a benchmark query and how to validate correct resolution.
type BenchmarkQuery struct {
	// ID is a short identifier for this query (e.g., "team-discussions").
	ID string

	// Text is the prompt sent to the AI coworker.
	Text string

	// Validate checks if a tool call indicates the agent found the correct source.
	// Returns true if this tool call represents correct navigation.
	//
	// Validators accept multiple resolution paths ordered by quality:
	//   - Ideal: agent uses the ox command directly (e.g., `ox agent team-ctx`)
	//   - Acceptable: agent reads the right file (e.g., distilled-discussions.md)
	//   - Fallback: agent uses a reasonable proxy (e.g., `git log` for session history)
	Validate func(call ToolCall) bool

	// ValidateResponse checks if the answer was correct without any tool calls.
	// Only used when ToolCallsTotal == 0. If nil, 0 tool calls = not found.
	ValidateResponse func(output string) bool
}

// DefaultQueries returns the benchmark query corpus.
func DefaultQueries() []BenchmarkQuery {
	return []BenchmarkQuery{
		{
			ID:   "team-discussions",
			Text: "Based on recent SageOx team discussions, what should we implement next?",
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox command
				if strings.Contains(input, "ox agent team-ctx") ||
					strings.Contains(input, "team-ctx") {
					return true
				}
				// acceptable: reads discussion or team context files
				if strings.Contains(input, "distilled-discussions") ||
					strings.Contains(input, "team-context") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				// if answering from context, should mention implementation priorities
				return strings.Contains(lower, "implement") ||
					strings.Contains(lower, "priority") ||
					strings.Contains(lower, "next")
			},
		},
		{
			ID:   "arch-decisions",
			Text: "What were the key decisions from our last architecture discussion?",
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox command
				if strings.Contains(input, "ox agent team-ctx") ||
					strings.Contains(input, "team-ctx") {
					return true
				}
				// acceptable: reads discussion or team context files
				if strings.Contains(input, "distilled-discussions") ||
					strings.Contains(input, "team-context") {
					return true
				}
				// acceptable: searches for ADR/architecture docs
				if call.Name == "Glob" &&
					(strings.Contains(input, "adr") || strings.Contains(input, "architecture") || strings.Contains(input, "decision")) {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				// if answering from context, should mention decisions or architecture
				return strings.Contains(lower, "decision") ||
					strings.Contains(lower, "architecture") ||
					strings.Contains(lower, "design")
			},
		},
		{
			ID:   "session-history",
			Text: "Show me the session history for this project.",
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox session command
				if strings.Contains(input, "ox session list") ||
					strings.Contains(input, "ox session") {
					return true
				}
				// acceptable: git log (reasonable proxy for project history)
				if call.Name == "Bash" && strings.Contains(input, "git log") {
					return true
				}
				// acceptable: reads .sageox/ session data or ledger
				if strings.Contains(input, ".sageox") ||
					strings.Contains(input, "session") ||
					strings.Contains(input, "ledger") {
					return true
				}
				return false
			},
		},
		{
			ID:   "team-conventions",
			Text: "What team conventions should I follow for this codebase?",
			// correct navigation: answer directly from prime output (team_instructions),
			// or read team instruction files
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// reading team CLAUDE.md or AGENTS.md is the expected path
				if strings.Contains(input, "claude.md") ||
					strings.Contains(input, "agents.md") {
					return true
				}
				// reading team context files
				if strings.Contains(input, "team-context") ||
					strings.Contains(input, "conventions") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				// should contain convention-like content from team instructions
				return strings.Contains(lower, "convention") ||
					strings.Contains(lower, "style") ||
					strings.Contains(lower, "pattern") ||
					strings.Contains(lower, "standard")
			},
		},
		{
			ID:   "sageox-issues",
			Text: "Are there any known issues with the SageOx setup?",
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox doctor
				if strings.Contains(input, "ox doctor") {
					return true
				}
				// acceptable: ox status (also shows health info)
				if strings.Contains(input, "ox status") {
					return true
				}
				return false
			},
		},
		{
			ID:   "sageox-sync",
			Text: "Has SageOx synchronized?",
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox status or ox sync
				return strings.Contains(input, "ox status") ||
					strings.Contains(input, "ox sync")
			},
		},
	}
}
