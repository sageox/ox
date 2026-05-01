package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// checkClaudeHooksFormat detects and repairs settings.json files where any
// hook event value is a bare string (the legacy format) instead of the
// array-of-HookEntry shape current Claude Code requires.
//
// Old ox versions (and the now-neutered ox-adapter-claude-code) emitted
// the bare-string form, which Claude Code's parser rejects wholesale —
// silently disabling every hook in the file until the user re-runs
// `ox init` or `ox doctor --fix`. The read-side promotion in
// internal/hooks/claude/parse.go (parseHooksMixed) handles in-memory
// recovery on every read, but a write only happens when some other code
// path has reason to modify hooks. Files in repos where hooks haven't
// been edited since the upgrade stay broken indefinitely.
//
// This check exists specifically to repair that dormant population.
//
// Repair strategy: read with ParseSettingsRaw (which promotes string→array
// in memory), re-marshal with MarshalSettings (which always writes array
// form), and only write if the canonical bytes differ from on-disk —
// keeping the check idempotent. Lossless: parseHooksMixed preserves
// existing array entries verbatim and the raw-map round-trip preserves
// non-hook keys (permissions, etc.).
func checkClaudeHooksFormat(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude hooks format", "not in git repo", "")
	}

	// Project-level .claude/settings.json — the file Claude Code actually
	// reads when running in this repo.
	path := filepath.Join(gitRoot, claudeDirName, claudeSettingsFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return SkippedCheck("Claude hooks format", "no .claude/settings.json", "")
	}
	if err != nil {
		return FailedCheck("Claude hooks format", "read failed", err.Error())
	}

	settings, rawMap, parseErr := claude.ParseSettingsRaw(data)
	if parseErr != nil {
		return FailedCheck("Claude hooks format", "parse failed", parseErr.Error())
	}

	canonical, err := claude.MarshalSettings(settings, rawMap)
	if err != nil {
		return FailedCheck("Claude hooks format", "re-marshal failed", err.Error())
	}

	// Compare canonical bytes (post-MarshalIndent) against on-disk. If
	// already canonical, nothing to do — keeps the check idempotent so
	// repeated `doctor --fix` runs don't churn the file.
	if bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(canonical)) {
		return PassedCheck("Claude hooks format", "canonical")
	}

	if !fix {
		return WarningCheck(
			"Claude hooks format",
			"non-canonical hook format (Claude Code may reject)",
			"Run `ox doctor --fix` to rewrite into the array-of-entries form Claude Code requires",
		)
	}

	// preserve existing perms; default to 0600 if stat fails (defensive — settings.json may hold tokens).
	perm := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(path, canonical, perm); err != nil {
		return FailedCheck("Claude hooks format", "write failed", err.Error())
	}
	return PassedCheck("Claude hooks format", fmt.Sprintf("repaired (%d bytes)", len(canonical)))
}
