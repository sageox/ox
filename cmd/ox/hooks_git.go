package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	prepareCommitMsgHookName = "prepare-commit-msg"
	postCommitHookName       = "post-commit"
	postRewriteHookName      = "post-rewrite"
	prePushHookName          = "pre-push"

	// shared end marker — all ox-managed hook sections close with this.
	// Mirrors the patterns in internal/uninstall/hooks.go sageoxHookMarkers.
	oxHookMarkerEnd = "# end ox hook"

	// prepare-commit-msg
	oxPrepareCommitMsgMarkerStart = "# ox prepare-commit-msg hook"
	oxPrepareCommitMsgSection     = `# ox prepare-commit-msg hook
# appends configured trailers (Co-Authored-By, SageOx-Session) to commits
if command -v ox >/dev/null 2>&1; then
  ox hooks commit-msg --msg-file "$1" --source "${2:-}" 2>/dev/null || true
` + oxGitHookNotOnPathFallback + `
# end ox hook`
	oxPrepareCommitMsgProbe = "ox hooks commit-msg"

	// post-commit: appends HEAD SHA to RecordingState.ProducedCommits while
	// a recording is active. No-op when no recording. Best-effort, never blocks.
	oxPostCommitMarkerStart = "# ox post-commit hook"
	oxPostCommitSection     = `# ox post-commit hook
# appends HEAD SHA to active recording's ProducedCommits index
if command -v ox >/dev/null 2>&1; then
  ox hooks post-commit 2>/dev/null || true
` + oxGitHookNotOnPathFallback + `
# end ox hook`
	oxPostCommitProbe = "ox hooks post-commit"

	// post-rewrite: consumes git's stdin SHA mapping and rewrites entries
	// in RecordingState.ProducedCommits. Passes $1 (amend|rebase) so the
	// handler can branch if needed. Best-effort, never blocks.
	oxPostRewriteMarkerStart = "# ox post-rewrite hook"
	oxPostRewriteSection     = `# ox post-rewrite hook
# rewrites SHAs in active recording's ProducedCommits on amend/rebase
if command -v ox >/dev/null 2>&1; then
  ox hooks post-rewrite --mode "${1:-}" 2>/dev/null || true
` + oxGitHookNotOnPathFallback + `
# end ox hook`
	oxPostRewriteProbe = "ox hooks post-rewrite"

	// pre-push: parses the pushed commit range from stdin, resolves the PR
	// for the branch and any Fixes #N issue refs, and records them on the
	// active recording's LinkedPRs/LinkedIssues. git pipes the ref lines on
	// stdin, so the hook reads stdin and forwards it to the handler.
	// Best-effort, never blocks the push.
	oxPrePushMarkerStart = "# ox pre-push hook"
	oxPrePushSection     = `# ox pre-push hook
# records PR + issue linkage for the active recording from the pushed range
if command -v ox >/dev/null 2>&1; then
  ox hooks pre-push --remote "${1:-}" --url "${2:-}" 2>/dev/null || true
` + oxGitHookNotOnPathFallback + `
# end ox hook`
	oxPrePushProbe = "ox hooks pre-push"

	// Legacy marker name from the single-hook era. Some code paths
	// (internal/uninstall/hooks.go) may still reference this; keep it as
	// an alias for the prepare-commit-msg marker so external readers don't
	// silently break.
	oxHookMarkerStart = oxPrepareCommitMsgMarkerStart

	// oxGitHookNotOnPathFallback is the shared else-branch for every
	// ox-managed git hook above. Previously these hooks had no else at all:
	// `command -v ox` failing meant total silence, every commit, forever —
	// even though the real cause is usually that ox IS installed but the
	// hook shell (a non-interactive `sh`, which reads none of a user's
	// interactive rc files) can't see it on PATH.
	//
	// Probe the usual install locations — $GOBIN (when set) first, since
	// it overrides GOPATH/bin for `go install` and costs nothing but an
	// env var read — and name the real fix instead of staying silent.
	// Only speak on the failure path — the success path (ox found, `then`
	// branch) stays exactly as quiet as before, since these hooks fire on
	// every commit/push.
	//
	// Guidance branches on $SHELL: only zsh has a startup file a
	// non-interactive shell always reads (~/.zshenv); bash/fish/other read
	// no rc file by default, so they get an honest explanation instead of
	// the zsh-specific one, plus a restart reminder. Mirrors
	// internal/constants/agent.go's oxNotOnPathFallback (the AI-tool-hook
	// flavor of this same message) and
	// internal/doctor/checks/ox_in_path.go's explanationFor/shellRCFor —
	// keep the three wordings identical.
	//
	// Uses `_ox_p`/`_ox_gp` (not `p`/`gp`): installHookSection appends this
	// section into an existing hook file's shell scope, where plain
	// `p`/`gp` could collide with a variable the surrounding hook already
	// defines.
	oxGitHookNotOnPathFallback = `else
  _ox_p=""
  _ox_home="${HOME:-}"
  [ -n "${GOBIN:-}" ] && [ -x "${GOBIN:-}/ox" ] && _ox_p="${GOBIN:-}"
  if [ -z "$_ox_p" ] && command -v go >/dev/null 2>&1; then
    _ox_gb="$(go env GOBIN 2>/dev/null)"
    [ -n "$_ox_gb" ] && [ -x "$_ox_gb/ox" ] && _ox_p="$_ox_gb"
    if [ -z "$_ox_p" ]; then
      _ox_gp="$(go env GOPATH 2>/dev/null)/bin"
      [ -x "$_ox_gp/ox" ] && _ox_p="$_ox_gp"
    fi
  fi
  [ -n "$_ox_home" ] && [ -z "$_ox_p" ] && [ -x "$_ox_home/go/bin/ox" ] && _ox_p="$_ox_home/go/bin"
  [ -n "$_ox_home" ] && [ -z "$_ox_p" ] && [ -x "$_ox_home/.local/bin/ox" ] && _ox_p="$_ox_home/.local/bin"
  [ -z "$_ox_p" ] && [ -x "/usr/local/bin/ox" ] && _ox_p="/usr/local/bin"
  [ -z "$_ox_p" ] && [ -x "/opt/homebrew/bin/ox" ] && _ox_p="/opt/homebrew/bin"
  if [ -n "$_ox_p" ]; then
    echo "ox is installed at $_ox_p/ox but is not on PATH for non-interactive shells." >&2
    _ox_sh="${SHELL:-}"
    case "${_ox_sh##*/}" in
      zsh)
        echo "AI coding tools run hooks in a non-interactive shell, which reads ~/.zshenv but not ~/.zshrc." >&2
        echo "Add this line to ~/.zshenv:" >&2
        echo "    export PATH=\"\$PATH:$_ox_p\"" >&2
        ;;
      bash)
        echo "AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched." >&2
        echo "Add this line to ~/.bashrc:" >&2
        echo "    export PATH=\"\$PATH:$_ox_p\"" >&2
        echo "Then restart your AI coding tool from a new terminal so it picks up the change." >&2
        ;;
      fish)
        echo "AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched." >&2
        echo "Add this line to ~/.config/fish/config.fish:" >&2
        echo "    fish_add_path $_ox_p" >&2
        echo "Then restart your AI coding tool from a new terminal so it picks up the change." >&2
        ;;
      *)
        echo "AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched." >&2
        echo "Add this line to the startup file for your shell:" >&2
        echo "    export PATH=\"\$PATH:$_ox_p\"" >&2
        echo "Then restart your AI coding tool from a new terminal so it picks up the change." >&2
        ;;
    esac
  else
    echo "ox is not installed. Install a release: brew tap sageox/tap && brew install ox, or curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash" >&2
  fi
fi`
)

// hookSpec describes one ox-managed git hook section.
type hookSpec struct {
	name        string
	markerStart string
	section     string
	probe       string // command-string fragment that must appear in a "complete" install
}

// allHookSpecs returns the set of ox-managed git hooks. Order is stable for
// deterministic test output.
func allHookSpecs() []hookSpec {
	return []hookSpec{
		{
			name:        prepareCommitMsgHookName,
			markerStart: oxPrepareCommitMsgMarkerStart,
			section:     oxPrepareCommitMsgSection,
			probe:       oxPrepareCommitMsgProbe,
		},
		{
			name:        postCommitHookName,
			markerStart: oxPostCommitMarkerStart,
			section:     oxPostCommitSection,
			probe:       oxPostCommitProbe,
		},
		{
			name:        postRewriteHookName,
			markerStart: oxPostRewriteMarkerStart,
			section:     oxPostRewriteSection,
			probe:       oxPostRewriteProbe,
		},
		{
			name:        prePushHookName,
			markerStart: oxPrePushMarkerStart,
			section:     oxPrePushSection,
			probe:       oxPrePushProbe,
		},
	}
}

// resolveHooksDir returns the git hooks directory, respecting core.hooksPath.
func resolveHooksDir(gitRoot string) string {
	cmd := exec.Command("git", "-C", gitRoot, "config", "--get", "core.hooksPath")
	output, err := cmd.Output()
	if err == nil {
		hooksPath := strings.TrimSpace(string(output))
		if hooksPath != "" {
			if filepath.IsAbs(hooksPath) {
				return hooksPath
			}
			return filepath.Join(gitRoot, hooksPath)
		}
	}
	return filepath.Join(gitRoot, ".git", "hooks")
}

// InstallGitHooks installs every ox-managed git hook into the resolved hooks
// directory: prepare-commit-msg (trailer injection), post-commit (reverse
// index append), and post-rewrite (reverse index SHA rewrite on amend/
// rebase). Idempotent across all three. If a hook file already exists, the
// ox section is appended without disturbing existing content.
//
// post-commit / post-rewrite are best-effort CLI-side: they no-op silently
// when no recording is active, and their handler exits 0 unconditionally.
// They never block a commit or a rewrite.
func InstallGitHooks(gitRoot string) error {
	hooksDir := resolveHooksDir(gitRoot)
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	for _, spec := range allHookSpecs() {
		if err := installHookSection(hooksDir, spec); err != nil {
			return fmt.Errorf("install %s: %w", spec.name, err)
		}
	}
	return nil
}

// installHookSection adds the ox section for a single hook, preserving any
// existing user content. Idempotent.
func installHookSection(hooksDir string, spec hookSpec) error {
	hookPath := filepath.Join(hooksDir, spec.name)

	existing, err := os.ReadFile(hookPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing hook: %w", err)
	}

	if err == nil {
		if hasValidHookSection(string(existing), spec) {
			return nil
		}
		content := string(existing)
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + spec.section + "\n"
		if err := os.WriteFile(hookPath, []byte(content), 0755); err != nil {
			return fmt.Errorf("append to hook: %w", err)
		}
		return nil
	}

	full := "#!/bin/sh\n" + spec.section + "\n"
	if err := os.WriteFile(hookPath, []byte(full), 0755); err != nil {
		return fmt.Errorf("write hook: %w", err)
	}
	return nil
}

// UninstallGitHooks removes every ox-managed section from each hook file.
// If a hook contains only an ox section after removal (no other user
// content), the file is removed entirely.
func UninstallGitHooks(gitRoot string) error {
	hooksDir := resolveHooksDir(gitRoot)
	for _, spec := range allHookSpecs() {
		if err := uninstallHookSection(hooksDir, spec); err != nil {
			return fmt.Errorf("uninstall %s: %w", spec.name, err)
		}
	}
	return nil
}

func uninstallHookSection(hooksDir string, spec hookSpec) error {
	hookPath := filepath.Join(hooksDir, spec.name)

	content, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read hook: %w", err)
	}

	if !strings.Contains(string(content), spec.markerStart) {
		return nil
	}

	lines := strings.Split(string(content), "\n")
	var result []string
	inSection := false
	for _, line := range lines {
		if strings.Contains(line, spec.markerStart) {
			inSection = true
			continue
		}
		if inSection && strings.Contains(line, oxHookMarkerEnd) {
			inSection = false
			continue
		}
		if !inSection {
			result = append(result, line)
		}
	}

	cleaned := strings.TrimSpace(strings.Join(result, "\n"))
	if cleaned == "" || cleaned == "#!/bin/sh" || cleaned == "#!/bin/bash" {
		return os.Remove(hookPath)
	}
	return os.WriteFile(hookPath, []byte(cleaned+"\n"), 0755)
}

// hasValidHookSection returns true if content contains a complete ox section
// for the given spec (start marker, end marker, and the probe command).
func hasValidHookSection(content string, spec hookSpec) bool {
	return strings.Contains(content, spec.markerStart) &&
		strings.Contains(content, oxHookMarkerEnd) &&
		strings.Contains(content, spec.probe)
}

// HasGitHooks returns true when all ox-managed git hooks are installed in
// the resolved hooks directory. A partial install (e.g. only
// prepare-commit-msg present from a previous ox version) returns false so
// `ox doctor --fix` can complete the install.
func HasGitHooks(gitRoot string) bool {
	hooksDir := resolveHooksDir(gitRoot)
	for _, spec := range allHookSpecs() {
		hookPath := filepath.Join(hooksDir, spec.name)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			return false
		}
		if !hasValidHookSection(string(content), spec) {
			return false
		}
	}
	return true
}
