//go:build integration

package benchmark

import (
	"strings"
)

// BenchmarkQuery defines a benchmark query and how to validate correct resolution.
type BenchmarkQuery struct {
	// ID is a short identifier for this query (e.g., "team-discussions").
	ID string

	// Texts contains multiple prompt phrasings for the same query.
	// Iterations cycle through them: iteration i uses Texts[i % len(Texts)].
	// Must have at least one entry.
	Texts []string

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
// Each query has 4-5 prompt variants that iterations cycle through.
func DefaultQueries() []BenchmarkQuery {
	return []BenchmarkQuery{
		{
			ID: "team-discussions",
			Texts: []string{
				"Based on recent SageOx team discussions, what should we implement next?",
				"What has the team been talking about lately?",
				"Catch me up on team discussions — what priorities came out of recent meetings?",
				"What are the current team priorities I should know about before I start coding?",
				"Pull up the latest team context so I know what direction we're heading.",
			},
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
				return strings.Contains(lower, "implement") ||
					strings.Contains(lower, "priority") ||
					strings.Contains(lower, "next")
			},
		},
		{
			ID: "project-guidance",
			Texts: []string{
				"What project-specific instructions should I follow in this repo?",
				"Is there an AGENTS.md or project guidance file I should read?",
				"What does this project expect from AI coworkers?",
				"Before I start working, what project-level rules apply here?",
				"Show me the project's agent instructions.",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: reads project AGENTS.md
				if strings.Contains(input, "agents.md") {
					return true
				}
				// acceptable: reads .sageox/ config or project guidance
				if strings.Contains(input, ".sageox/") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				return strings.Contains(lower, "agent") ||
					strings.Contains(lower, "instruction") ||
					strings.Contains(lower, "guidance") ||
					strings.Contains(lower, "command")
			},
		},
		{
			ID: "session-history",
			Texts: []string{
				"Show me the session history for this project.",
				"What has the AI been working on in this repo recently?",
				"Give me a summary of recent coding sessions.",
				"What was worked on in the last few sessions here?",
				"Can you show me what previous AI coworkers did in this project?",
			},
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
				// acceptable: reads session data or ledger session paths
				if strings.Contains(input, ".sageox/sessions") ||
					strings.Contains(input, "ledger/sessions") {
					return true
				}
				return false
			},
		},
		{
			ID: "team-conventions",
			Texts: []string{
				"What team conventions should I follow for this codebase?",
				"How does this team write code? What are the style rules?",
				"Before I write any code, what conventions and standards apply here?",
				"What would a code review catch me on if I don't follow team norms?",
				"Tell me the rules: naming, patterns, what to avoid in this codebase.",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				if strings.Contains(input, "claude.md") ||
					strings.Contains(input, "agents.md") {
					return true
				}
				if strings.Contains(input, "team-context") ||
					strings.Contains(input, "conventions") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				return strings.Contains(lower, "convention") ||
					strings.Contains(lower, "style") ||
					strings.Contains(lower, "pattern") ||
					strings.Contains(lower, "standard")
			},
		},
		{
			ID: "sageox-issues",
			Texts: []string{
				"Are there any known issues with the SageOx setup?",
				"Is ox configured correctly for this project?",
				"Run a health check on the ox integration.",
				"Something seems off with ox — can you diagnose it?",
				"Check if there are any problems with how ox is set up here.",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				// ideal: ox doctor
				if strings.Contains(input, "ox doctor") {
					return true
				}
				// acceptable: ox status (weaker signal but still relevant)
				if strings.Contains(input, "ox status") {
					return true
				}
				return false
			},
		},
		{
			ID: "sageox-sync",
			Texts: []string{
				"Has SageOx synchronized?",
				"Is the team context up to date?",
				"When did ox last sync?",
				"Are we seeing the latest team discussions or is the sync stale?",
				"Check whether ox is pulling in the latest team context.",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				return strings.Contains(input, "ox status") ||
					strings.Contains(input, "ox sync")
			},
		},
		{
			ID: "attribution-guidance",
			Texts: []string{
				"How should I attribute ox when I make commits based on its guidance?",
				"What do I put in commit messages when SageOx influenced my work?",
				"Are there attribution requirements for commits in this project?",
				"What footer should I add to plans that were guided by SageOx?",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				if strings.Contains(input, "attribution") {
					return true
				}
				if strings.Contains(input, "claude.md") ||
					strings.Contains(input, "agents.md") {
					return true
				}
				if strings.Contains(input, "ox agent prime") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				return strings.Contains(lower, "attribution") ||
					strings.Contains(lower, "footer") ||
					strings.Contains(lower, "commit")
			},
		},
		{
			ID: "subagent-discovery",
			Texts: []string{
				"What specialist subagents are available on this team?",
				"Are there any Claude agents configured for this project I should know about?",
				"Show me what AI coworkers or subagents this team has set up.",
				"Can I delegate work to a subagent? What's available?",
			},
			Validate: func(call ToolCall) bool {
				input := strings.ToLower(call.Input)
				if strings.Contains(input, "agents/") ||
					strings.Contains(input, "coworkers/ai") {
					return true
				}
				if strings.Contains(input, "team-ctx") ||
					strings.Contains(input, "ox agent team-ctx") {
					return true
				}
				return false
			},
			ValidateResponse: func(output string) bool {
				lower := strings.ToLower(output)
				return strings.Contains(lower, "agent") ||
					strings.Contains(lower, "subagent") ||
					strings.Contains(lower, "specialist")
			},
		},
	}
}
