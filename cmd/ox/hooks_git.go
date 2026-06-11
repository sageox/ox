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
fi
# end ox hook`
	oxPrepareCommitMsgProbe = "ox hooks commit-msg"

	// post-commit: appends HEAD SHA to RecordingState.ProducedCommits while
	// a recording is active. No-op when no recording. Best-effort, never blocks.
	oxPostCommitMarkerStart = "# ox post-commit hook"
	oxPostCommitSection     = `# ox post-commit hook
# appends HEAD SHA to active recording's ProducedCommits index
if command -v ox >/dev/null 2>&1; then
  ox hooks post-commit 2>/dev/null || true
fi
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
fi
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
fi
# end ox hook`
	oxPrePushProbe = "ox hooks pre-push"

	// Legacy marker name from the single-hook era. Some code paths
	// (internal/uninstall/hooks.go) may still reference this; keep it as
	// an alias for the prepare-commit-msg marker so external readers don't
	// silently break.
	oxHookMarkerStart = oxPrepareCommitMsgMarkerStart
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
