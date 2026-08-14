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
	string(agentx.AgentTypeGemini):     true,
	string(agentx.AgentTypeAmp):        true,
	string(agentx.AgentTypePi):         true,
	string(agentx.AgentTypeGoose):      true,
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
		return string(agentx.AgentTypeGemini)
	case "amp", "amp-cli", "amp cli", "sourcegraph":
		return string(agentx.AgentTypeAmp)
	case "pi", "pi-coding-agent", "pi agent":
		return string(agentx.AgentTypePi)
	case "goose":
		return string(agentx.AgentTypeGoose)
	}

	// If the input is a display name from registry (e.g., "Cursor"), map to slug.
	for _, agent := range agentx.DefaultRegistry.List() {
		if strings.EqualFold(agent.Name(), agentType) {
			return string(agent.Type())
		}
	}

	return slug
}

// AgentTier classifies an agent by the richness of plan-enrichment guidance
// it can act on. Higher tiers get the full advisory block + IntentCommand;
// lower tiers get a lighter note that only promises what the tier can deliver.
type AgentTier int

const (
	// TierUnknown is the safe baseline for unrecognized/empty agent types:
	// full block + the active `ox plan` command. We assume capability rather
	// than under-serve an agent we simply haven't classified yet.
	TierUnknown AgentTier = iota
	// TierBronze covers known agents with no real-time lifecycle hooks
	// (amp, opencode, pi). Lighter note: `ox plan` exists + `ox plan list` to
	// browse prior plans — no promise of a nudge the tier can't deliver.
	TierBronze
	// TierSilver covers guidance-driven agents (codex, gemini): full block +
	// IntentCommand, but no real-time hook firing the nudge for them.
	TierSilver
	// TierGold covers claude-code, which has PostToolUse/Stop hooks that can
	// drive the enrichment nudge in real time. Full block + IntentCommand.
	TierGold
)

// ClassifyAgentTier maps an agent type to its plan-enrichment tier. Unknown or
// empty agent types fall back to TierUnknown (safe baseline = block + command).
func ClassifyAgentTier(agentType string) AgentTier {
	switch CanonicalAgentType(agentType) {
	case string(agentx.AgentTypeClaudeCode):
		return TierGold
	// Goose is Silver on the same basis as Codex: it installs real lifecycle
	// hooks. It is strictly ahead of Codex on SessionEnd (which Codex lacks) and
	// strictly behind everything else here on compaction — Goose fires no
	// compaction event, so primed context does not survive a Goose compaction.
	case string(agentx.AgentTypeCodex), string(agentx.AgentTypeGemini), string(agentx.AgentTypeGoose):
		return TierSilver
	case string(agentx.AgentTypeAmp), string(agentx.AgentTypeOpenCode), string(agentx.AgentTypePi):
		return TierBronze
	default:
		return TierUnknown
	}
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

	return "Codex supports hooks via .codex/hooks.json. Run `ox integrate install --codex` to install hooks. Session recording via `ox agent <id> session start` and `ox agent <id> session stop`."
}
