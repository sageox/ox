//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three ox doctor checks had no direct tests at all, because each resolved
// its repository from the process working directory. That binding is not merely
// untestable — it is the mechanism that once made a test run operate on the
// developer's own checkout and remove tracked files from it. These tests drive
// the root-parameterized forms against scratch repositories instead.

// TestCheckOxIgnoreRulesIn_JudgedByGitNotByStringSearch: the check must ask git,
// because only git accounts for negations, nested ignore files, and tracked-path
// semantics. Asserting the .gitignore "contains a pattern" would pass while the
// files were still visible to git.
func TestCheckOxIgnoreRulesIn_JudgedByGitNotByStringSearch(t *testing.T) {
	root := newIgnoreTestRepo(t)
	touch(t, root, ".claude/skills/ox-cli-plan/SKILL.md")

	if got := checkOxIgnoreRulesIn(root, false); got.passed {
		t.Fatalf("a repo with reserved files and no ignore rule must fail; got message=%q detail=%q", got.message, got.detail)
	}

	res := checkOxIgnoreRulesIn(root, true)
	if !res.passed {
		t.Fatalf("--fix did not repair the ignore rule: %s / %s / warn=%v", res.message, res.detail, res.warning)
	}
	if !gitIgnores(t, root, ".claude/skills/ox-cli-plan/SKILL.md") {
		t.Error("check reported success while git still sees the reserved file")
	}
}

// TestCheckOxIgnoreRulesIn_UserNegationIsReportedNotFought: a user rule that
// re-includes a reserved path must be reported, not silently overwritten. ox
// owns its block; it does not own the user's file.
func TestCheckOxIgnoreRulesIn_UserNegationIsReportedNotFought(t *testing.T) {
	root := newIgnoreTestRepo(t)
	touch(t, root, ".claude/skills/ox-cli-probe/SKILL.md")
	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	// A deeper ignore file beats the parent block. git decides; ox must notice.
	deeper := filepath.Join(root, ".claude", "skills", ".gitignore")
	if err := os.WriteFile(deeper, []byte("!ox-cli-probe/\n"), 0o644); err != nil {
		t.Fatalf("write deeper ignore: %v", err)
	}

	// WarningCheck is passed=true, warning=true by this codebase's convention:
	// "noteworthy, not broken". The claim under test is that ox SURFACES the
	// conflict rather than silently leaving reserved files visible to git.
	res := checkOxIgnoreRulesIn(root, true)
	if !res.warning {
		t.Errorf("a user negation that re-includes reserved files was not reported: message=%q detail=%q", res.message, res.detail)
	}
	if !strings.Contains(res.message, "still not ignored") {
		t.Errorf("warning does not name the real problem: %q", res.message)
	}
	data, err := os.ReadFile(deeper)
	if err != nil {
		t.Fatalf("read deeper ignore: %v", err)
	}
	if !strings.Contains(string(data), "!ox-cli-probe/") {
		t.Error("ox rewrote the user's own ignore file instead of reporting the conflict")
	}
}

// TestCheckOxIgnoreRulesIn_NoFootprintInUnusedAgentDirs: a Claude-only repo must
// not sprout .agents/ or .factory/ just because a check ran.
func TestCheckOxIgnoreRulesIn_NoFootprintInUnusedAgentDirs(t *testing.T) {
	root := newIgnoreTestRepo(t)
	touch(t, root, ".claude/skills/ox-cli-plan/SKILL.md")

	checkOxIgnoreRulesIn(root, true)

	for _, dir := range []string{".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			t.Errorf("doctor created %s/ in a repository that does not use it", dir)
		}
	}
}

// TestCheckLegacyOxFilesIn_UntracksInOneCommitAndConverges is the whole migration
// promise seen through the check that actually runs it: tracked vendor files
// leave the index, the ignore rule and on-ramp arrive in the SAME commit, and a
// second pass reports no work. Without the same-commit part, teammates pull an
// untrack with no rule and their `git status` fills with ox files.
func TestCheckLegacyOxFilesIn_UntracksInOneCommitAndConverges(t *testing.T) {
	root := migrationRepo(t)
	before := commitCount(t, root)

	if got := checkLegacyOxFilesIn(root, false); got.passed {
		t.Fatalf("tracked legacy files must be reported without --fix; got message=%q detail=%q", got.message, got.detail)
	}

	res := checkLegacyOxFilesIn(root, true)
	if !res.passed {
		t.Fatalf("--fix did not migrate: %s / %s / warn=%v", res.message, res.detail, res.warning)
	}
	if got := commitCount(t, root) - before; got != 1 {
		t.Errorf("migration made %d commits, want exactly 1", got)
	}

	tracked := strings.Join(trackedPaths(t, root), "\n")
	if strings.Contains(tracked, ".claude/skills/ox-cli-") {
		t.Error("a reserved-prefix skill is still tracked after the migration")
	}
	if !strings.Contains(tracked, ".claude/.gitignore") {
		t.Error("the ignore rule was not committed with the untrack; teammates would inherit none")
	}
	if !strings.Contains(tracked, ".claude/skills/my-skill/SKILL.md") {
		t.Error("the migration untracked a user-authored file")
	}

	// Converges: nothing left to do.
	if got := checkLegacyOxFilesIn(root, false); !got.passed {
		t.Errorf("second pass still reports work: message=%q detail=%q", got.message, got.detail)
	}
}

// TestCheckOxFilesNotTrackedIn_ReportsWithoutTouchingTheIndex: this check is
// diagnostic. It must never mutate git, even when asked to fix — the migration
// check owns that.
func TestCheckOxFilesNotTrackedIn_ReportsWithoutTouchingTheIndex(t *testing.T) {
	root := migrationRepo(t)
	before := strings.Join(trackedPaths(t, root), "\n")

	res := checkOxFilesNotTrackedIn(root, true)
	if res.passed && strings.Contains(res.message, "untrack") {
		t.Errorf("a diagnostic check reported StatusFixed: %s", res.message)
	}
	if after := strings.Join(trackedPaths(t, root), "\n"); after != before {
		t.Error("the diagnostic check mutated the git index")
	}
}
