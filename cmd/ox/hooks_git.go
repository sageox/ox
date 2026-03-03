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

	// markers delimit the ox section within a hook file.
	// these match the patterns in internal/uninstall/hooks.go sageoxHookMarkers.
	oxHookMarkerStart = "# ox prepare-commit-msg hook"
	oxHookMarkerEnd   = "# end ox hook"

	// oxHookSection is the script fragment appended to prepare-commit-msg.
	// Calls ox hooks commit-msg which is a deterministic command (not AI-driven).
	oxHookSection = `# ox prepare-commit-msg hook
# appends configured trailers (Co-Authored-By, SageOx-Session) to commits
if command -v ox >/dev/null 2>&1; then
  ox hooks commit-msg --msg-file "$1" --source "${2:-}" 2>/dev/null || true
fi
# end ox hook`

	oxHookFull = "#!/bin/sh\n" + oxHookSection + "\n"
)

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

// InstallGitHooks installs the ox prepare-commit-msg hook into the resolved
// hooks directory. If the hook file already exists, appends the ox section
// (delimited by markers) without disturbing existing content. Idempotent.
//
// This is best-effort CLI-side linking. Commits made outside an active session
// won't get a session trailer. For full commit↔session correlation, grant
// SageOx GitHub repo permissions so cloud services can match after the fact.
func InstallGitHooks(gitRoot string) error {
	hooksDir := resolveHooksDir(gitRoot)
	hookPath := filepath.Join(hooksDir, prepareCommitMsgHookName)

	// ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	existing, err := os.ReadFile(hookPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing hook: %w", err)
	}

	if err == nil {
		// hook file exists — check if already installed
		if strings.Contains(string(existing), oxHookMarkerStart) {
			return nil // already installed
		}

		// append ox section to existing hook
		content := string(existing)
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + oxHookSection + "\n"

		if err := os.WriteFile(hookPath, []byte(content), 0755); err != nil {
			return fmt.Errorf("append to hook: %w", err)
		}
		return nil
	}

	// no existing hook — create new
	if err := os.WriteFile(hookPath, []byte(oxHookFull), 0755); err != nil {
		return fmt.Errorf("write hook: %w", err)
	}
	return nil
}

// UninstallGitHooks removes the ox section from the prepare-commit-msg hook.
// If the hook file contains only the ox section, removes the file entirely.
func UninstallGitHooks(gitRoot string) error {
	hooksDir := resolveHooksDir(gitRoot)
	hookPath := filepath.Join(hooksDir, prepareCommitMsgHookName)

	content, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to uninstall
		}
		return fmt.Errorf("read hook: %w", err)
	}

	if !strings.Contains(string(content), oxHookMarkerStart) {
		return nil // ox section not present
	}

	// remove the ox section (marker start through marker end, inclusive)
	lines := strings.Split(string(content), "\n")
	var result []string
	inOxSection := false
	for _, line := range lines {
		if strings.Contains(line, oxHookMarkerStart) {
			inOxSection = true
			continue
		}
		if inOxSection && strings.Contains(line, oxHookMarkerEnd) {
			inOxSection = false
			continue
		}
		if !inOxSection {
			result = append(result, line)
		}
	}

	cleaned := strings.TrimSpace(strings.Join(result, "\n"))

	// if only shebang (or empty) remains, remove the file
	if cleaned == "" || cleaned == "#!/bin/sh" || cleaned == "#!/bin/bash" {
		return os.Remove(hookPath)
	}

	return os.WriteFile(hookPath, []byte(cleaned+"\n"), 0755)
}

// HasGitHooks returns true if the ox prepare-commit-msg hook is installed.
func HasGitHooks(gitRoot string) bool {
	hooksDir := resolveHooksDir(gitRoot)
	hookPath := filepath.Join(hooksDir, prepareCommitMsgHookName)

	content, err := os.ReadFile(hookPath)
	if err != nil {
		return false
	}

	return strings.Contains(string(content), oxHookMarkerStart)
}
