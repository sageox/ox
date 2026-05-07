package config

import (
	"log/slog"
	"os"
)

// AgentSummarizer modes — see ADR-016.
//
// inline    — the CLI hands a SummaryPrompt back to the calling agent, which
//
//	runs the LLM in its already-warm conversation context. Cheap
//	(input tokens are mostly cache reads); blocks the user in the
//	foreground for ~30–120s while the agent finishes. **Default.**
//
// delegated — the daemon spawns a fresh `claude`/`codex`/`gemini` subprocess
//
//	against the cached raw.jsonl. Every call is a cold prompt, so
//	input tokens are paid in full — roughly 10× the cost of inline
//	on the same session. The only thing this buys the user is
//	returning the terminal immediately at session-stop.
//
// off       — disables LLM summarization entirely. The session is uploaded
//
//	without a summary; downstream features that depend on the
//	summary (team-context surfacing, session search) will be
//	degraded for that recording.
//
// cloud     — RESERVED for future SageOx cloud-side summarization. Not
//
//	implemented; rejected at validation.
const (
	AgentSummarizerInline    = "inline"
	AgentSummarizerDelegated = "delegated"
	AgentSummarizerOff       = "off"
	// AgentSummarizerCloud is reserved for future SageOx cloud-side summarization.
	// It is intentionally NOT in ValidAgentSummarizerModes — attempts to set it
	// via `ox config set` are rejected with a clear "not yet implemented" error.
	// The constant exists so callers can pattern-match on the eventual value
	// without typo risk.
	AgentSummarizerCloud = "cloud"
)

// ValidAgentSummarizerModes lists the modes accepted by user configuration.
// `cloud` is intentionally absent until the cloud path is implemented.
var ValidAgentSummarizerModes = []string{AgentSummarizerInline, AgentSummarizerDelegated, AgentSummarizerOff}

// IsValidAgentSummarizer returns true if the mode is a recognized + currently-
// implementable value. Returns true for "" (callers can leave unset to take
// the default) but false for the reserved `cloud` value.
func IsValidAgentSummarizer(mode string) bool {
	switch mode {
	case AgentSummarizerInline, AgentSummarizerDelegated, AgentSummarizerOff, "":
		return true
	}
	return false
}

// NormalizeAgentSummarizer returns the canonical mode string. Unrecognized
// values fall back to the default (inline). Empty input returns inline so
// the resolver can short-circuit on no-config-set without a separate "" case.
func NormalizeAgentSummarizer(mode string) string {
	switch mode {
	case AgentSummarizerInline, AgentSummarizerDelegated, AgentSummarizerOff:
		return mode
	default:
		return AgentSummarizerInline
	}
}

// AgentSummarizerSource indicates where the resolved value came from.
type AgentSummarizerSource string

const (
	// AgentSummarizerSourceDefault — no config set, returning the built-in default (inline).
	AgentSummarizerSourceDefault AgentSummarizerSource = "default"
	// AgentSummarizerSourceUserConfig — set via `ox config set agent.summarizer ...`.
	AgentSummarizerSourceUserConfig AgentSummarizerSource = "user"
	// AgentSummarizerSourceEnvDeprecated — set via the legacy SAGEOX_ASYNC_SESSION_UPLOAD
	// or OX_SESSION_INLINE_SUMMARY env vars. These remain as one-release
	// compatibility shims; the resolver logs a deprecation warning.
	AgentSummarizerSourceEnvDeprecated AgentSummarizerSource = "env-deprecated"
)

// ResolvedAgentSummarizer is the effective mode plus its provenance.
type ResolvedAgentSummarizer struct {
	Mode   string
	Source AgentSummarizerSource
}

// ResolveAgentSummarizer determines the effective summarizer mode for the
// given project. Resolution order:
//
//  1. Legacy env vars (SAGEOX_ASYNC_SESSION_UPLOAD=1 → delegated;
//     OX_SESSION_INLINE_SUMMARY=1 → inline). Logs a deprecation warning when
//     either is set, pointing at `ox config set agent.summarizer`. These
//     shims will be removed one release after the config knob ships.
//  2. User config (agent.summarizer key in user config.yaml).
//  3. Built-in default: inline.
//
// Default is inline because the cost asymmetry between inline (warm cache)
// and delegated (cold prompt every call) is too large to default to the
// expensive path. Users who want non-blocking session-stop and are OK paying
// the token cost can opt into delegated explicitly.
func ResolveAgentSummarizer(projectRoot string) *ResolvedAgentSummarizer {
	// 1. legacy env vars — short-lived compat shim
	if os.Getenv("OX_SESSION_INLINE_SUMMARY") == "1" {
		slog.Warn("OX_SESSION_INLINE_SUMMARY is deprecated; use 'ox config set agent.summarizer inline'")
		return &ResolvedAgentSummarizer{Mode: AgentSummarizerInline, Source: AgentSummarizerSourceEnvDeprecated}
	}
	if os.Getenv("SAGEOX_ASYNC_SESSION_UPLOAD") == "1" {
		slog.Warn("SAGEOX_ASYNC_SESSION_UPLOAD is deprecated; use 'ox config set agent.summarizer delegated'")
		return &ResolvedAgentSummarizer{Mode: AgentSummarizerDelegated, Source: AgentSummarizerSourceEnvDeprecated}
	}

	// 2. user config
	if userCfg, err := LoadUserConfig(); err == nil && userCfg != nil && userCfg.AgentSummarizer != "" {
		mode := NormalizeAgentSummarizer(userCfg.AgentSummarizer)
		return &ResolvedAgentSummarizer{Mode: mode, Source: AgentSummarizerSourceUserConfig}
	}

	// 3. default
	return &ResolvedAgentSummarizer{Mode: AgentSummarizerInline, Source: AgentSummarizerSourceDefault}
}

// GetAgentSummarizer is a convenience wrapper that returns just the resolved mode.
func GetAgentSummarizer(projectRoot string) string {
	return ResolveAgentSummarizer(projectRoot).Mode
}
