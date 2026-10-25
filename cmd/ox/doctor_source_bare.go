package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// CheckSlugSourceRepoBare is the slug for the flipped-core.bare repair.
const CheckSlugSourceRepoBare = "source-repo-bare"

// checkSourceRepoBareFlipped detects and repairs a managed source checkout whose
// core.bare was flipped to true — the issue #819 failure mode. When something
// rewrites the project's (or its shared worktree hub's) .git/config with
// core.bare=true, every work-tree git command (git status, git pull) then fails
// with "fatal: this operation must be run in a work tree" until the value is
// manually reset.
//
// A SageOx-managed project root is a WORKING checkout — never legitimately bare —
// so core.bare=true there is always corruption. Repair restores core.bare=false
// (the same one-liner affected users run by hand) and logs loudly, so a still-
// active writer stays visible instead of being silently papered over.
//
// This is a defense-in-depth safety net, not the #819 root-cause fix: the guard
// in internal/codedb/gitopen prevents codedb from writing the config, while this
// recovers a checkout that was wedged by any writer (including one we have not
// yet pinned down). See internal/codedb/gitopen and issue #819.
//
// Detection deliberately uses config.FindProjectRoot (walks up for .sageox/) and
// `git config` reads — both work even when the repo is flipped bare. findGitRoot()
// would fail on `git rev-parse --show-toplevel` for a bare repo and skip the very
// checkout that is broken.
//
// The fix parameter is ignored: restoring a managed checkout's work-tree access
// is always safe, so this runs unconditionally under FixLevelAuto.
//
// Failure prevented: a flipped core.bare wedges the user's checkout until they
// manually run `git config core.bare false` (issue #819).
func checkSourceRepoBareFlipped(_ bool) checkResult {
	root := config.FindProjectRoot()
	if root == "" {
		return SkippedCheck("Source repo work tree", "not a SageOx project", "")
	}
	return repairSourceRepoBare(root)
}

// repairSourceRepoBare holds the detection + repair for a single project root,
// split out so tests can drive it with a temp repo instead of the process CWD.
func repairSourceRepoBare(root string) checkResult {
	if !config.IsInitialized(root) {
		return SkippedCheck("Source repo work tree", "not initialized", "")
	}

	bare, set := gitConfigBoolAt(root, "core.bare")
	if !set || !bare {
		// core.bare unset, false, or unreadable — a normal working checkout.
		return PassedCheck("Source repo work tree", "work tree intact")
	}

	// core.bare=true on a managed working checkout IS the #819 corruption.
	if err := setGitConfigAt(root, "core.bare", "false"); err != nil {
		return FailedCheck("Source repo work tree",
			"core.bare=true but repair failed",
			fmt.Sprintf("run `git -C %s config core.bare false` manually: %v", root, err))
	}
	slog.Warn("repaired flipped core.bare on managed source checkout",
		"repo", root, "issue", "819")
	return WarningCheck("Source repo work tree",
		"repaired core.bare=true → false (was wedging work-tree git commands)",
		"issue #819 — if this recurs, capture daemon logs around the .git/config rewrite")
}

// gitConfigBoolAt reads a boolean git config value from repoRoot. Returns
// (value, true) when the key is set and parses as bool, (false, false)
// otherwise. A plain config read, so it works regardless of core.bare state
// (unlike work-tree operations, which fail on a bare repo).
func gitConfigBoolAt(repoRoot, key string) (value bool, set bool) {
	out, err := exec.Command("git", "-C", repoRoot, "config", "--type=bool", "--get", key).Output()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) == "true", true
}

// setGitConfigAt sets a git config value in repoRoot.
func setGitConfigAt(repoRoot, key, value string) error {
	return exec.Command("git", "-C", repoRoot, "config", key, value).Run()
}
