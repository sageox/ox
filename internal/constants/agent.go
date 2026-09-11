package constants

// SageOxGitEmail is the canonical email for SageOx git identity.
// Used in commit attribution, fallback git config, etc.
const SageOxGitEmail = "ox@sageox.ai"

// SageOxGitName is the canonical name for SageOx git identity.
const SageOxGitName = "SageOx"

// RecordingDocsURL is the short learn-more link shown on the FIRST recording
// reminder. Redirects to the full /docs/cli/recording page (what's captured,
// who sees it, how to stop). Kept short on purpose — it is echoed verbatim by
// the agent into an 80-col terminal, where a long path wraps and truncates.
const RecordingDocsURL = "sageox.ai/rec"

// oxNotOnPathFallback is the shared else-branch for every hook command
// below. `command -v ox` failing does NOT mean ox isn't installed — the
// hook shell may simply lack PATH for non-interactive execution. That's
// the common case: AI coding tools run hooks in a non-interactive shell,
// which reads ~/.zshenv but never ~/.zshrc, so a PATH export added only to
// an interactive rc file is invisible here even though `ox` works fine at
// the terminal.
//
// Probes the usual install locations — $GOBIN (when set), $(go env
// GOPATH)/bin, ~/go/bin, ~/.local/bin, /usr/local/bin, /opt/homebrew/bin —
// and names the real fix (off-PATH) instead of misdiagnosing it as "not
// installed". $GOBIN is checked first since, when set, it's authoritative
// for where `go install` actually places binaries (overriding GOPATH/bin),
// and it's a plain env var read with no subprocess cost. Only recommends a
// fresh install when none of those locations has an ox binary — that
// still leaves nix installs, non-default --prefix builds, etc.
// undetected, which is an acceptable gap: the alternative is an unbounded
// probe list on a path that runs on every hook invocation.
//
// Kept to a single semicolon-joined line: this is embedded verbatim as a
// JSON hook "command" value, and every ox/word pair outside single quotes
// is scanned by cmd/ox's doctor hook-command validator — see
// cmd/ox/doctor_hooks.go's singleQuotedString stripping and
// knownOxSubcommands. Natural-language text stays single-quoted so it gets
// stripped before that scan; only bare variable references (no literal
// "ox" text) sit outside quotes.
const oxNotOnPathFallback = `else p=""; [ -n "$GOBIN" ] && [ -x "$GOBIN/ox" ] && p="$GOBIN"; [ -z "$p" ] && if command -v go >/dev/null 2>&1; then gp="$(go env GOPATH 2>/dev/null)/bin"; [ -x "$gp/ox" ] && p="$gp"; fi; [ -z "$p" ] && [ -x "$HOME/go/bin/ox" ] && p="$HOME/go/bin"; [ -z "$p" ] && [ -x "$HOME/.local/bin/ox" ] && p="$HOME/.local/bin"; [ -z "$p" ] && [ -x "/usr/local/bin/ox" ] && p="/usr/local/bin"; [ -z "$p" ] && [ -x "/opt/homebrew/bin/ox" ] && p="/opt/homebrew/bin"; if [ -n "$p" ]; then echo 'ox is installed at '"$p"'/ox but is not on PATH for non-interactive shells.'; echo 'AI coding tools run hooks in a non-interactive shell, which reads ~/.zshenv but not ~/.zshrc.'; echo 'Add this line to ~/.zshenv:'; echo '    export PATH="$PATH:'"$p"'"'; else echo 'ox is not installed. Install a release: brew tap sageox/tap && brew install ox, or curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash'; fi; fi`

const (
	// OxPrimeCommand is the legacy command without AGENT_ENV prefix.
	// Kept for backwards compatibility detection of existing hooks.
	// New installations should use agent-specific commands.
	OxPrimeCommand = "if command -v ox >/dev/null 2>&1; then ox agent prime 2>&1 || true; " + oxNotOnPathFallback

	// OxPrimeCommandClaudeCode is the command for Claude Code hooks.
	//
	// Why AGENT_ENV is required: Claude Code runs SessionStart/PreCompact hooks
	// BEFORE setting CLAUDECODE=1 in the subprocess environment. This means
	// agent detection fails during hook execution. Setting AGENT_ENV explicitly
	// ensures detection works reliably. See pkg/agentx/agents/claudecode.go for details.
	OxPrimeCommandClaudeCode = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; " + oxNotOnPathFallback

	// OxPrimeCommandClaudeCodeIdempotent is the idempotent version for startup/resume hooks.
	// Uses --idempotent flag to skip priming if session already primed (saves ~1k tokens).
	OxPrimeCommandClaudeCodeIdempotent = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; " + oxNotOnPathFallback

	// OxPrimeCommandGemini is the command for Gemini CLI hooks.
	OxPrimeCommandGemini = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent prime 2>&1 || true; " + oxNotOnPathFallback

	// OxPrimeCommandAmp is the command for Amp CLI hooks.
	OxPrimeCommandAmp = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=amp ox agent prime 2>&1 || true; " + oxNotOnPathFallback

	// OxPrimeCommandPi is the command for Pi hooks.
	OxPrimeCommandPi = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=pi ox agent prime 2>&1 || true; " + oxNotOnPathFallback
)

// OxHookCommand templates for lifecycle hook installation.
// These replace the per-event ox agent prime commands with a single generalized handler.
const (
	// OxHookCommandClaudeCode is the template for Claude Code lifecycle hooks.
	// The %s placeholder is replaced with the native event name (e.g., SessionStart).
	OxHookCommandClaudeCodeTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback

	// OxHookCommandCodexTemplate is the template for Codex CLI lifecycle hooks.
	// The %s placeholder is replaced with the native event name (e.g., SessionStart).
	OxHookCommandCodexTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=codex ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback

	// OxHookCommandGeminiTemplate is the template for Gemini CLI lifecycle hooks.
	// The %s placeholder is replaced with the native event name (e.g., SessionStart, BeforeAgent).
	OxHookCommandGeminiTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback
)
