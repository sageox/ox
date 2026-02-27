package constants

// SageOxGitEmail is the canonical email for SageOx git identity.
// Used in commit attribution, fallback git config, etc.
const SageOxGitEmail = "ox@sageox.ai"

// SageOxGitName is the canonical name for SageOx git identity.
const SageOxGitName = "SageOx"

const (
	// OxPrimeCommand is the legacy command without AGENT_ENV prefix.
	// Kept for backwards compatibility detection of existing hooks.
	// New installations should use agent-specific commands.
	OxPrimeCommand = "if command -v ox >/dev/null 2>&1; then ox agent prime 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi"

	// OxPrimeCommandClaudeCode is the command for Claude Code hooks.
	//
	// Why AGENT_ENV is required: Claude Code runs SessionStart/PreCompact hooks
	// BEFORE setting CLAUDECODE=1 in the subprocess environment. This means
	// agent detection fails during hook execution. Setting AGENT_ENV explicitly
	// ensures detection works reliably. See pkg/agentx/agents/claudecode.go for details.
	OxPrimeCommandClaudeCode = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi"

	// OxPrimeCommandClaudeCodeIdempotent is the idempotent version for startup/resume hooks.
	// Uses --idempotent flag to skip priming if session already primed (saves ~1k tokens).
	OxPrimeCommandClaudeCodeIdempotent = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi"

	// OxPrimeCommandGemini is the command for Gemini CLI hooks.
	OxPrimeCommandGemini = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent prime 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi"
)

// OxHookCommand templates for lifecycle hook installation.
// These replace the per-event ox agent prime commands with a single generalized handler.
const (
	// OxHookCommandClaudeCode is the template for Claude Code lifecycle hooks.
	// The %s placeholder is replaced with the native event name (e.g., SessionStart).
	OxHookCommandClaudeCodeTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent hook %s 2>&1 || true; else echo 'This repo uses SageOx: install https://github.com/sageox/ox for optimized team context.'; fi"
)
