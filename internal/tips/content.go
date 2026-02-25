// internal/tips/content.go
package tips

import (
	"fmt"
	"math/rand" //nolint:gosec // G404: tip selection doesn't need crypto-secure randomness
	"strings"
)

// Tips are organized into two audiences:
//
// 1. Human Tips (humanContextualTips, humanGeneralTips)
//    - Shown when humans run ox commands interactively in their terminal
//    - Focus on CLI ergonomics, shortcuts, and discoverability
//    - Use TUI-friendly language and mention clipboard, flags, etc.
//
// 2. Agent Tips (agentContextualTips, agentGeneralTips)
//    - Shown when `ox agent` subcommands are run (typically by AI coding agents)
//    - Focus on progressive disclosure and context efficiency
//    - Use machine-friendly language and emphasize token efficiency
//
// The tip system automatically selects the appropriate pool based on command context.

// humanContextualTips maps human-facing command names to their relevant tips.
// These tips help humans discover CLI features and shortcuts.
var humanContextualTips = map[string][]string{
	"login": {
		"Next: `ox status` — verify your authentication and project setup",
		"Set up project hooks with `ox init` in your repo root",
		"Run `ox doctor` to check your installation health",
	},
	"status": {
		"Run `ox doctor` for detailed diagnostics if something looks off",
		"Use `ox logout` to clear credentials when switching accounts",
	},
	"init": {
		"`ox hooks install` sets up Claude Code integration automatically",
		"Run `ox status` to see your new project configuration",
	},
	"doctor": {
		"Use `--fix` to automatically resolve common issues",
		"`ox hooks install` sets up Claude Code integration",
	},
	"logout": {
		"Run `ox login` to authenticate with a different account",
		"Your local project config in `.sageox/` is preserved after logout",
	},
	"hooks": {
		"Hooks run `ox agent prime` automatically in Claude Code sessions",
		"Use `ox hooks install --user` for global integration",
	},
}

// humanGeneralTips are shown for discovery or unknown commands (human context).
// Focus on CLI features humans would find useful.
var humanGeneralTips = []string{
	"`ox doctor --verbose` shows detailed check information",
	"Use `--json` on most commands for machine-readable output",
	"Run `ox doctor --fix` to auto-resolve common issues",
	"Use `ox doctor` to verify your installation health",
	"`ox release-notes` shows what's new in your version",
	"The `--quiet` flag suppresses tips and non-essential output",
}

// agentContextualTips maps agent-facing command names to their relevant tips.
// These tips help AI coding agents use ox effectively.
var agentContextualTips = map[string][]string{
	"prime": {
		"Run `ox doctor` to verify your setup and check for issues",
		"Use `ox session start` to begin recording your work",
		"Check `ox status` to see your project configuration",
	},
}

// agentGeneralTips are shown for discovery or unknown commands (agent context).
// Focus on progressive disclosure and context efficiency.
var agentGeneralTips = []string{
	"Use `ox doctor` to check project health and configuration",
	"Sessions are recorded to the repo-specific ledger (not team context)",
	"Run `ox status` to check daemon sync and project state",
	"Use `ox config` to view and manage settings",
}

// primeUserTips are product tips shown to the user via the agent after ox agent prime.
// Tips containing %s will have the agent name substituted (e.g., "Claude Code").
var primeUserTips = []string{
	`Try asking %s: "Tell me recent major decisions from our SageOx team discussions"`,
	"Sessions are auto-recorded and shared with your team. To disable: `ox config set session_recording disabled`",
	"View your team's knowledge base in the browser with `ox view team`",
	"SageOx team context updates automatically — decisions from one session inform every future session",
	"Record team discussions at sageox.ai to give all AI coworkers shared context",
}

// agentNameMap maps agent type strings to friendly display names.
var agentNameMap = map[string]string{
	"Claude Code": "Claude Code",
	"claude-code": "Claude Code",
	"claude":      "Claude Code",
	"Cursor":      "Cursor",
	"cursor":      "Cursor",
	"Windsurf":    "Windsurf",
	"windsurf":    "Windsurf",
	"Copilot":     "Copilot",
	"copilot":     "Copilot",
	"Amp":         "Amp",
	"amp":         "Amp",
	"OpenCode":    "OpenCode",
	"opencode":    "OpenCode",
	"Gemini":      "Gemini",
	"gemini":      "Gemini",
	"Conductor":   "Conductor",
	"conductor":   "Conductor",
}

// friendlyAgentName returns a human-readable agent name for tip text.
func friendlyAgentName(agentType string) string {
	if name, ok := agentNameMap[agentType]; ok {
		return name
	}
	// capitalize first letter if non-empty
	if agentType != "" {
		return strings.ToUpper(agentType[:1]) + agentType[1:]
	}
	return "your AI coworker"
}

// GetPrimeUserTip returns a random product tip for the user, with agent name substituted.
func GetPrimeUserTip(agentType string) string {
	tip := primeUserTips[rand.Intn(len(primeUserTips))]
	if strings.Contains(tip, "%s") {
		return fmt.Sprintf(tip, friendlyAgentName(agentType))
	}
	return tip
}

// isAgentCommand returns true if the command is part of the agent UX.
func isAgentCommand(command string) bool {
	switch command {
	case "prime", "agent":
		return true
	default:
		return false
	}
}

// getContextualTips returns the appropriate contextual tips map based on command.
func getContextualTips(command string) map[string][]string {
	if isAgentCommand(command) {
		return agentContextualTips
	}
	return humanContextualTips
}

// getGeneralTips returns the appropriate general tips slice based on command.
func getGeneralTips(command string) []string {
	if isAgentCommand(command) {
		return agentGeneralTips
	}
	return humanGeneralTips
}
