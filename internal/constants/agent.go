package constants

import "strings"

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
// which (for zsh) reads ~/.zshenv but never ~/.zshrc, so a PATH export
// added only to an interactive rc file is invisible here even though `ox`
// works fine at the terminal. Non-interactive, non-login bash/fish/other
// shells read no rc file at all by default, so the guidance branches on
// `$SHELL` (see internal/doctor/checks/ox_in_path.go's explanationFor /
// shellRCFor for the canonical per-shell wording this mirrors) rather than
// naming a file that shell never reads.
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
// Uses `_ox_p`/`_ox_gp` (not `p`/`gp`) because InstallGitHooks appends the
// byte-identical fallback (cmd/ox/hooks_git.go's oxGitHookNotOnPathFallback)
// into a pre-existing hook file's shell scope, where plain `p`/`gp` could
// collide with a variable the surrounding hook already defines.
//
// Kept to a single semicolon-joined line: this is embedded verbatim as a
// JSON hook "command" value, and every ox/word pair outside single quotes
// is scanned by cmd/ox's doctor hook-command validator — see
// cmd/ox/doctor_hooks.go's singleQuotedString stripping and
// knownOxSubcommands. Natural-language text stays single-quoted so it gets
// stripped before that scan; only bare variable references (no literal
// "ox" text) sit outside quotes. This is also why the "unknown shell"
// branch says "the startup file for your shell" rather than "your shell's
// startup file": singleQuotedString is a naive `'[^']*'` regex with no
// double-quote awareness, so a bare apostrophe anywhere in this string —
// even inside a double-quoted echo — desyncs its quote-pairing for
// everything that follows and can expose a later "ox ..." echo as an
// unquoted (mis-flagged) command. Verified with a false-positive repro
// before landing this wording; see PR #909 review thread on this file.
//
// The fish branch emits `fish_add_path -- "$_ox_p"` — double quotes, not
// single. The path must survive a space (bare, fish reads
// `/Users/a/Go Tools/bin` as three arguments and adds two wrong
// directories), and a literal apostrophe emitted here would desync the
// quote-pairing described above.
//
// The printed PATH line escapes the directory for the inside of a
// double-quoted string ($, a backtick, " and \ for POSIX shells; fish leaves
// backticks alone): the person pastes it into a startup file that runs on
// every new shell, so a directory named with those must read as text there,
// never run. Lines that carry the directory go out through printf, not echo:
// an XSI echo (macOS /bin/sh) would turn the escaped \\ back into \. If sed
// fails the PATH line is left out rather than printed as `$PATH:`, which would
// put the current directory on PATH. The sed scripts sit inside single quotes
// and hold no apostrophe, so the doctor scan above still pairs every quote.
// bash names the file a new terminal's bash reads: on macOS a login shell,
// which reads the first existing of ~/.bash_profile, ~/.bash_login,
// ~/.profile and not ~/.bashrc.
//
// The not-installed branch names the fully-qualified Homebrew formula and the
// versioned release download page. Linking the README would still steer users
// to its mutable `main`-branch `curl … | bash` route. `ox doctor` writes this
// string into each consumer's tracked .claude/settings.json and repairs any
// divergence, so whatever it recommends propagates into other repositories
// and cannot be edited there. See issue #937.
const oxNotOnPathFallback = `else _ox_p=""; _ox_home="${HOME:-}"; [ -n "${GOBIN:-}" ] && [ -x "${GOBIN:-}/ox" ] && _ox_p="${GOBIN:-}"; [ -z "$_ox_p" ] && if command -v go >/dev/null 2>&1; then _ox_gb="$(go env GOBIN 2>/dev/null)"; [ -n "$_ox_gb" ] && [ -x "$_ox_gb/ox" ] && _ox_p="$_ox_gb"; if [ -z "$_ox_p" ]; then _ox_gp="$(go env GOPATH 2>/dev/null)/bin"; [ -x "$_ox_gp/ox" ] && _ox_p="$_ox_gp"; fi; fi; [ -n "$_ox_home" ] && [ -z "$_ox_p" ] && [ -x "$_ox_home/go/bin/ox" ] && _ox_p="$_ox_home/go/bin"; [ -n "$_ox_home" ] && [ -z "$_ox_p" ] && [ -x "$_ox_home/.local/bin/ox" ] && _ox_p="$_ox_home/.local/bin"; [ -z "$_ox_p" ] && [ -x "/usr/local/bin/ox" ] && _ox_p="/usr/local/bin"; [ -z "$_ox_p" ] && [ -x "/opt/homebrew/bin/ox" ] && _ox_p="/opt/homebrew/bin"; if [ -n "$_ox_p" ]; then printf '%s\n' 'ox is installed at '"$_ox_p"'/ox but is not on PATH for non-interactive shells.'; _ox_sh="${SHELL:-}"; _ox_e='s/[\\"$` + "`" + `]/\\&/g'; if [ "${_ox_sh##*/}" = fish ]; then _ox_e='s/[\\"$]/\\&/g'; fi; _ox_q="$(printf '%s' "$_ox_p" | sed "$_ox_e" 2>/dev/null)" || _ox_q=""; case "${_ox_sh##*/}" in zsh) echo 'AI coding tools run hooks in a non-interactive shell, which reads ~/.zshenv but not ~/.zshrc.'; echo 'Add this line to ~/.zshenv:'; if [ -n "$_ox_q" ]; then printf '%s\n' '    export PATH="$PATH:'"$_ox_q"'"'; fi;; bash) _ox_rc='~/.bashrc'; if [ "$(uname -s 2>/dev/null)" = Darwin ]; then _ox_rc='~/.bash_profile'; for _ox_f in .bash_profile .bash_login .profile; do if [ -n "$_ox_home" ] && [ -f "$_ox_home/$_ox_f" ]; then _ox_rc="~/$_ox_f"; break; fi; done; fi; echo 'AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched.'; echo 'Add this line to '"$_ox_rc"':'; if [ -n "$_ox_q" ]; then printf '%s\n' '    export PATH="$PATH:'"$_ox_q"'"'; fi; echo 'Then restart your AI coding tool from a new terminal so it picks up the change.';; fish) echo 'AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched.'; echo 'Add this line to ~/.config/fish/config.fish:'; if [ -n "$_ox_q" ]; then printf '%s\n' '    fish_add_path -- "'"$_ox_q"'"'; fi; echo 'Then restart your AI coding tool from a new terminal so it picks up the change.';; *) echo 'AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched.'; echo 'Add this line to the startup file for your shell:'; if [ -n "$_ox_q" ]; then printf '%s\n' '    export PATH="$PATH:'"$_ox_q"'"'; fi; echo 'Then restart your AI coding tool from a new terminal so it picks up the change.';; esac; else echo 'ox is not installed. Install a release: brew install sageox/tap/ox, or download from https://github.com/sageox/ox/releases/latest'; fi; fi`

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
// Fill them with HookCommand, never fmt.Sprintf: the off-PATH fallback they
// end with runs printf '%s' itself, so a template holds more than one %s.
const (
	// OxHookCommandClaudeCode is the template for Claude Code lifecycle hooks.
	// The first %s placeholder is replaced with the native event name (e.g., SessionStart).
	OxHookCommandClaudeCodeTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback

	// OxHookCommandCodexTemplate is the template for Codex CLI lifecycle hooks.
	// The first %s placeholder is replaced with the native event name (e.g., SessionStart).
	OxHookCommandCodexTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=codex ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback

	// OxHookCommandGeminiTemplate is the template for Gemini CLI lifecycle hooks.
	// The first %s placeholder is replaced with the native event name (e.g., SessionStart, BeforeAgent).
	OxHookCommandGeminiTemplate = "if command -v ox >/dev/null 2>&1; then AGENT_ENV=gemini ox agent hook %s 2>&1 || true; " + oxNotOnPathFallback
)

// HookCommand fills an OxHookCommand*Template with the native event name.
// Only the first %s is the event placeholder; the rest belong to the shell.
func HookCommand(template, event string) string {
	return strings.Replace(template, "%s", event, 1)
}
