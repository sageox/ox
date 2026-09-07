package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitIgnores asks git itself whether a path is ignored.
//
// --no-index is load-bearing: without it `git check-ignore` reports a TRACKED
// file as not-ignored regardless of the rules, because ignore rules do not apply
// to tracked paths. During the migration window the legacy files ARE still
// tracked, so a check without this flag would report the rules broken exactly
// when they matter most.
func gitIgnores(t *testing.T, repoRoot, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "--no-index", "-q", "--", rel)
	cmd.Dir = repoRoot
	err := cmd.Run()
	if err == nil {
		return true
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", rel, err)
	return false
}

func newIgnoreTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return root
}

func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestScopedIgnoreFiles_GitActuallyIgnoresEveryReservedPath is the truth test for
// the whole PR-noise fix. Asserting that the .gitignore CONTAINS a pattern proves
// nothing — git decides, and it accounts for negations, ordering, nested ignore
// files, and tracked-path semantics that a string search cannot see.
//
// The ox-cli.md case is here because it is the one that was actually wrong:
// "rules/ox-cli-*" does not match "ox-cli.md", and the miss is silent — that one
// file would have kept appearing in every pull request.
func TestScopedIgnoreFiles_GitActuallyIgnoresEveryReservedPath(t *testing.T) {
	root := newIgnoreTestRepo(t)

	reserved := []string{
		".claude/skills/ox-cli-plan/SKILL.md",
		".claude/skills/sageox-team-deploy/SKILL.md",
		".claude/rules/ox-cli.md",
		".claude/rules/ox-cli-use-team-context.md",
		".claude/commands/ox-cli-prime.md",
		".agents/skills/ox-cli-recap/SKILL.md",
		".factory/rules/ox-cli.md",
		".factory/rules/ox-cli-use-team-context.md",
	}
	mustBeVisible := []string{
		".claude/skills/sageox/SKILL.md",     // the one committed on-ramp
		".claude/skills/monitor-pr/SKILL.md", // user-authored
		".claude/rules/my-team-rule.md",      // user-authored
		".claude/skills/ox-kb/SKILL.md",      // user-authored, ox-named but NOT ox-cli-
	}
	for _, rel := range append(append([]string{}, reserved...), mustBeVisible...) {
		touch(t, root, rel)
	}

	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}

	for _, rel := range reserved {
		if !gitIgnores(t, root, rel) {
			t.Errorf("git does NOT ignore %s — it would land in the customer's pull request", rel)
		}
	}
	for _, rel := range mustBeVisible {
		if gitIgnores(t, root, rel) {
			t.Errorf("git ignores %s — ox hid a file it does not own", rel)
		}
	}
}

// TestScopedIgnoreFiles_NoFootprintInDirectoriesOxDoesNotUse: a Claude-only
// repository must not sprout .agents/ or .factory/. Adding vendor footprint to a
// project that never selected that agent is the same complaint this rework exists
// to answer.
func TestScopedIgnoreFiles_NoFootprintInDirectoriesOxDoesNotUse(t *testing.T) {
	root := newIgnoreTestRepo(t)
	touch(t, root, ".claude/skills/ox-cli-plan/SKILL.md")

	written, err := ensureScopedIgnoreFiles(root)
	if err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	if len(written) != 1 || written[0] != filepath.Join(".claude", ".gitignore") {
		t.Errorf("expected only .claude/.gitignore, got %v", written)
	}
	for _, dir := range []string{".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			t.Errorf("ox created %s/ in a repository that does not use it", dir)
		}
	}
}

// TestScopedIgnoreFiles_SecondRunWritesNothing: the writer runs at init, at
// doctor, and on the daemon tick. If it reported a change every time, the
// committed ignore file would churn — reintroducing the exact noise being removed.
func TestScopedIgnoreFiles_SecondRunWritesNothing(t *testing.T) {
	root := newIgnoreTestRepo(t)
	touch(t, root, ".claude/skills/ox-cli-plan/SKILL.md")

	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("first: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".claude", ".gitignore"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	written, err := ensureScopedIgnoreFiles(root)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("second run reported writes: %v", written)
	}
	after, _ := os.ReadFile(filepath.Join(root, ".claude", ".gitignore"))
	if string(before) != string(after) {
		t.Errorf("second run modified the committed ignore file")
	}
}

// TestIsReservedManagedPath_OwnershipBoundary pins exactly which paths ox will
// refuse to stage. Both directions matter: staging a reserved path puts vendor
// files back into pull requests, while treating a user's path as reserved would
// hide their own work from git.
func TestIsReservedManagedPath_OwnershipBoundary(t *testing.T) {
	root := "/repo"
	cases := []struct {
		rel      string
		reserved bool
		why      string
	}{
		{".claude/skills/ox-cli-plan/SKILL.md", true, "CLI-shipped skill"},
		{".claude/skills/sageox-team-deploy/SKILL.md", true, "team-synced skill"},
		{".claude/rules/ox-cli.md", true, "primary rule: exact reserved name, no trailing hyphen"},
		{".claude/rules/ox-cli-use-team-context.md", true, "prefixed rule"},
		{".claude/commands/ox-cli-prime.md", true, "pre-fold command still on disk"},
		{".agents/skills/ox-cli-recap/SKILL.md", true, "shared Codex/Gemini projection"},
		{".factory/rules/ox-cli.md", true, "droid rule"},

		{".claude/skills/sageox/SKILL.md", false, "the committed on-ramp must stay staged"},
		{".claude/skills/ox-kb/SKILL.md", false, "user-authored, ox-named but not ox-cli-"},
		{".claude/skills/monitor-pr/SKILL.md", false, "user-authored"},
		{".claude/rules/design.md", false, "user-authored rule"},
		{".claude/settings.json", false, "hook settings must still be staged"},
		{".claude/rules/sageox/use-team-context.md", false, "legacy nested rule: retired by the adapter sweep, not hidden"},
	}
	for _, c := range cases {
		got := isReservedManagedPath(root, filepath.Join(root, filepath.FromSlash(c.rel)))
		if got != c.reserved {
			t.Errorf("%s: got reserved=%v want %v (%s)", c.rel, got, c.reserved, c.why)
		}
	}
}
