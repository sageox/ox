package agentwork

import "log/slog"

// agentPriority defines auto-detection order: claude preferred over codex.
var agentPriority = []string{"claude", "codex"}

// ResolveAgent resolves the effective agent from a config value.
// If configured is "" (unconfigured), auto-detects by checking which agent CLIs
// are installed AND authenticated, in priority order: claude > codex.
// Returns "none" if no usable agent found.
// If configured is a specific value ("none", "claude", "codex"), returns it as-is.
func ResolveAgent(configured string) string {
	if configured != "" {
		return configured
	}
	for _, agent := range agentPriority {
		u := CheckAgentUsability(agent)
		if u.Installed && u.Authenticated {
			return agent
		}
	}
	return "none"
}

// NewRunner creates a Runner for the given agent type.
// Returns a ClaudeRunner for "claude" (or unknown/fallback), CodexRunner for "codex".
// The caller should check Available() before invoking Run().
func NewRunner(agentType string, logger *slog.Logger) Runner {
	switch agentType {
	case "codex":
		return NewCodexRunner(logger)
	default:
		return NewClaudeRunner(logger)
	}
}
