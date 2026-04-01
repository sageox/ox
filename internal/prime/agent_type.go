package prime

import (
	"fmt"
	"strings"

	"github.com/sageox/agentx"
)

// SupportedAgents lists officially supported coding agents for MVP.
// Other agents may work but quality of guidance is not guaranteed.
var SupportedAgents = map[string]bool{
	string(agentx.AgentTypeClaudeCode): true,
	string(agentx.AgentTypeCodex):      true,
	"gemini":                           true, // agentx.AgentTypeGemini pending
	string(agentx.AgentTypeAmp):        true,
}

// CanonicalAgentType normalizes display names and legacy aliases to canonical agent type slugs.
func CanonicalAgentType(agentType string) string {
	slug := strings.ToLower(strings.TrimSpace(agentType))
	switch slug {
	case "":
		return ""
	case "claude-code", "claudecode", "claude code":
		return string(agentx.AgentTypeClaudeCode)
	case "codex":
		return string(agentx.AgentTypeCodex)
	case "gemini", "gemini-cli", "gemini cli":
		return "gemini"
	case "amp", "amp-cli", "amp cli", "sourcegraph":
		return string(agentx.AgentTypeAmp)
	}

	// If the input is a display name from registry (e.g., "Cursor"), map to slug.
	for _, agent := range agentx.DefaultRegistry.List() {
		if strings.EqualFold(agent.Name(), agentType) {
			return string(agent.Type())
		}
	}

	return slug
}

// IsAgentSupported returns true if the agent is officially supported.
func IsAgentSupported(agentType string) bool {
	normalized := CanonicalAgentType(agentType)
	if normalized == "" {
		return false // unknown agent is not supported
	}
	return SupportedAgents[normalized]
}

// GetAgentSupportNotice returns a notice for unsupported agents, or empty string for supported ones.
func GetAgentSupportNotice(agentType string) string {
	normalized := CanonicalAgentType(agentType)

	if IsAgentSupported(agentType) {
		return ""
	}

	if normalized == "" {
		return "SageOx is explicitly designed for use with Claude Code. It is unknown if this agent will appropriately interpret and effectively apply team context. You should review plans deeply to ensure this agent has produced an insightful plan."
	}

	// get display name from registry (e.g., "cursor" -> "Cursor")
	displayName := normalized
	if agent, ok := agentx.DefaultRegistry.Get(agentx.AgentType(normalized)); ok {
		displayName = agent.Name()
	}

	return fmt.Sprintf("SageOx is explicitly designed for use with Claude Code. It is unknown if %s will appropriately interpret and effectively apply team context. You should review plans deeply to ensure %s has produced an insightful plan.", displayName, displayName)
}

// CodexLifecycleNotification returns Codex-specific workflow guidance.
func CodexLifecycleNotification(agentType string) string {
	if CanonicalAgentType(agentType) != string(agentx.AgentTypeCodex) {
		return ""
	}

	return "Codex supports hooks via .codex/hooks.json (enable with `codex features enable codex_hooks`). Run `ox integrate install --codex` to install hooks. Session recording via `ox agent <id> session start` and `ox agent <id> session stop`."
}
