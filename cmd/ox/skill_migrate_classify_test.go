//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"

	"github.com/sageox/agentx"
)

// classifyLegacyPath is the ownership boundary for the whole migration: it decides
// which of a customer's tracked files ox is allowed to untrack, delete, or leave
// alone. Both directions are destructive when wrong — misclassifying a user's file
// as ox-owned deletes their work; misclassifying ox's own leaves the orphan that
// this rework exists to end.

func classifyRepo(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func put(t *testing.T, root, rel string, body []byte) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return p
}

func TestClassifyLegacyPath_ReservedNamesAreOxOwnedRegardlessOfContent(t *testing.T) {
	root := classifyRepo(t)
	cases := []string{
		".claude/skills/ox-cli-plan/SKILL.md",
		".agents/skills/ox-cli-recap/SKILL.md",
		".claude/rules/ox-cli.md",
		".factory/rules/ox-cli-use-team-context.md",
		".claude/skills/sageox-team-deploy/SKILL.md",
	}
	for _, rel := range cases {
		// Deliberately unstamped: inside a reserved namespace the NAME is the
		// contract, so ox owns the bytes whatever they contain.
		put(t, root, rel, []byte("whatever\n"))
		if got := classifyLegacyPath(root, rel); got != legacyReserved {
			t.Errorf("%s classified %v, want legacyReserved", rel, got)
		}
	}
}

func TestClassifyLegacyPath_UserAuthoredFilesAreNeverTouched(t *testing.T) {
	root := classifyRepo(t)
	cases := []string{
		".claude/skills/my-skill/SKILL.md",
		".claude/skills/ox-kb/SKILL.md", // ox-named but outside the CLI prefix
		".claude/rules/design.md",
		".claude/commands/my-command.md",
		".claude/settings.json",
	}
	for _, rel := range cases {
		put(t, root, rel, []byte("mine\n"))
		if got := classifyLegacyPath(root, rel); got != legacyUserOwned {
			t.Errorf("%s classified %v, want legacyUserOwned", rel, got)
		}
	}
}

// TestClassifyLegacyPath_RetiredNamesNeedAVerifyingStamp: a retired name predates
// the reserved-prefix contract, so the NAME alone proves nothing. Only a stamp
// that still hashes to the body shows the bytes are still ox's.
func TestClassifyLegacyPath_RetiredNamesNeedAVerifyingStamp(t *testing.T) {
	root := classifyRepo(t)
	rel := ".claude/skills/ox-plan/SKILL.md"

	put(t, root, rel, agentx.StampedContent([]byte("legacy plan body\n"), "0.14.0", "ox"))
	if got := classifyLegacyPath(root, rel); got != legacySuperseded {
		t.Errorf("a stamp-verified retired skill classified %v, want legacySuperseded", got)
	}

	// Same name, user has edited it: the stamp no longer matches its body.
	put(t, root, rel, []byte("<!-- ox-hash: deadbeefcafe ver: 0.14.0 -->\nI rewrote this\n"))
	if got := classifyLegacyPath(root, rel); got != legacyUserOwned {
		t.Errorf("a user-edited retired skill classified %v, want legacyUserOwned — deleting it destroys their work", got)
	}
}

// TestClassifyLegacyPath_OnlyTheStampedManifestOfARetiredSkillIsOxs: supporting
// files under a retired skill directory may hold user work and carry no stamp of
// their own, so ox must not claim them.
func TestClassifyLegacyPath_OnlyTheStampedManifestOfARetiredSkillIsOxs(t *testing.T) {
	root := classifyRepo(t)
	put(t, root, ".claude/skills/ox-plan/SKILL.md", agentx.StampedContent([]byte("body\n"), "0.14.0", "ox"))
	put(t, root, ".claude/skills/ox-plan/scripts/helper.sh", []byte("#!/bin/sh\necho mine\n"))

	if got := classifyLegacyPath(root, ".claude/skills/ox-plan/scripts/helper.sh"); got != legacyUserOwned {
		t.Errorf("a supporting file under a retired skill classified %v, want legacyUserOwned", got)
	}
}

// TestClassifyLegacyPath_NestedLegacyRulesInEveryRulesRoot pins the miss found by
// running against a real clone: hardcoding ".claude/rules" left a verifying
// .factory/rules/sageox/use-team-context.md tracked forever.
func TestClassifyLegacyPath_NestedLegacyRulesInEveryRulesRoot(t *testing.T) {
	root := classifyRepo(t)
	// A rule file is generated frontmatter followed by an agentx stamp over the
	// body. RuleStampVerifies checks BOTH, so the description has to match exactly.
	body := append(
		[]byte("---\ndescription: "+teamContextRuleDescription+"\n---\n"),
		agentx.StampedContent([]byte("nested rule body\n"), "0.14.0", agentx.DefaultStampPrefix)...,
	)

	for _, surface := range []string{".claude/rules", ".factory/rules", ".agents/rules"} {
		rel := surface + "/sageox/use-team-context.md"
		put(t, root, rel, body)
		if got := classifyLegacyPath(root, rel); got != legacySuperseded {
			t.Errorf("%s classified %v, want legacySuperseded — it stays tracked forever", rel, got)
		}
	}
}

// TestPlainStampVerifies_RequiresTheStampOnLineOne: without the prefix check, a
// file whose body verifies but which has user-authored bytes inserted ABOVE the
// stamp would be claimed by ox and deleted.
func TestPlainStampVerifies_RequiresTheStampOnLineOne(t *testing.T) {
	root := classifyRepo(t)
	stamped := agentx.StampedContent([]byte("body\n"), "0.14.0", "ox")

	clean := put(t, root, "clean.md", stamped)
	if !plainStampVerifies(clean, "ox") {
		t.Error("a correctly stamped file did not verify")
	}

	prefixed := put(t, root, "prefixed.md", append([]byte("MY OWN HEADER\n"), stamped...))
	if plainStampVerifies(prefixed, "ox") {
		t.Error("ox claimed a file with user bytes inserted above the stamp")
	}

	if plainStampVerifies(filepath.Join(root, "does-not-exist.md"), "ox") {
		t.Error("a missing file verified")
	}
}

// TestInstalledClaudeReplacement_RequiresExactCanonicalBytes backs the gate that
// stops the migration removing the legacy command surface before its replacement
// exists. A near-miss must not count: a half-written or user-edited replacement
// leaves the project with no working surface at all.
func TestInstalledClaudeReplacement_RequiresExactCanonicalBytes(t *testing.T) {
	root := classifyRepo(t)
	const targetRoot = ".claude/skills"

	if installedClaudeReplacement(root, targetRoot) {
		t.Error("reported a replacement present in an empty repository")
	}

	want, err := canonicalSkillManifest("ox-cli-prime")
	if err != nil {
		t.Skipf("ox-cli-prime not in this catalog: %v", err)
	}
	put(t, root, targetRoot+"/ox-cli-prime/SKILL.md", want)
	if !installedClaudeReplacement(root, targetRoot) {
		t.Error("the canonical replacement was not recognized")
	}

	put(t, root, targetRoot+"/ox-cli-prime/SKILL.md", append(want, []byte("\nedited\n")...))
	if installedClaudeReplacement(root, targetRoot) {
		t.Error("an edited replacement counted as installed; the migration would remove the old surface anyway")
	}
}

// safeUntrackedMigrationFootprint decides what the migration may add to a commit
// it makes on the user's behalf. Anything it accepts gets committed without the
// user reviewing it, so "is this byte-for-byte something ox generated?" is the
// only acceptable test — a near-match means committing their bytes for them.
func TestSafeUntrackedMigrationFootprint_AcceptsOnlyExactGeneratedContent(t *testing.T) {
	root := classifyRepo(t)

	// The on-ramp, exactly as the catalog ships it.
	want, err := canonicalSkillManifest(skillmanager.CommittedOnRamp)
	if err != nil {
		t.Skipf("on-ramp unavailable in this catalog: %v", err)
	}
	onramp := filepath.ToSlash(filepath.Join(".claude", "skills", skillmanager.CommittedOnRamp, "SKILL.md"))
	put(t, root, onramp, want)
	if !safeUntrackedMigrationFootprint(root, onramp) {
		t.Error("the canonical on-ramp was refused; the migration would never commit it")
	}

	// The same path, edited by the user.
	put(t, root, onramp, append(want, []byte("\nmy own note\n")...))
	if safeUntrackedMigrationFootprint(root, onramp) {
		t.Error("ox would commit a user-edited on-ramp on their behalf")
	}

	// A scoped ignore file holding the user's own rules alongside ox's block.
	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	rel := ".claude/.gitignore"
	if !safeUntrackedMigrationFootprint(root, rel) {
		t.Error("a purely ox-generated ignore file was refused")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	put(t, root, rel, append([]byte("settings.local.json\n\n"), data...))
	if safeUntrackedMigrationFootprint(root, rel) {
		t.Error("ox would commit a .gitignore containing the user's own rules")
	}
}

// TestSafeUntrackedMigrationFootprint_RejectsAnythingUnexpected: only regular
// files ox itself generates are eligible. A symlink or a path outside the known
// footprint must never ride along in an automatic commit.
func TestSafeUntrackedMigrationFootprint_RejectsAnythingUnexpected(t *testing.T) {
	root := classifyRepo(t)

	put(t, root, ".claude/skills/my-skill/SKILL.md", []byte("mine\n"))
	if safeUntrackedMigrationFootprint(root, ".claude/skills/my-skill/SKILL.md") {
		t.Error("a user-authored skill was eligible for the automatic commit")
	}
	if safeUntrackedMigrationFootprint(root, ".claude/skills/absent/SKILL.md") {
		t.Error("a missing path was eligible")
	}

	link := filepath.Join(root, ".claude", "linked.gitignore")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), link); err == nil {
		if safeUntrackedMigrationFootprint(root, ".claude/linked.gitignore") {
			t.Error("a symlink was eligible for the automatic commit")
		}
	}
}

// TestCanonicalSkillManifest_UnknownNameIsAnError: every caller treats a returned
// error as "no replacement available", which is the gate that stops the migration
// removing a surface before its replacement exists. Returning empty bytes with no
// error would make that gate pass vacuously.
func TestCanonicalSkillManifest_UnknownNameIsAnError(t *testing.T) {
	if _, err := canonicalSkillManifest("definitely-not-a-skill"); err == nil {
		t.Error("an unknown skill name produced a manifest")
	}
	got, err := canonicalSkillManifest(skillmanager.CommittedOnRamp)
	if err != nil {
		t.Skipf("on-ramp unavailable: %v", err)
	}
	if len(got) == 0 {
		t.Error("the on-ramp manifest is empty")
	}
}

// The migration writes one commit to the user's history, so its cleanliness gate
// decides whether their in-progress work rides along. These cover the ways that
// gate can be defeated.

// TestTrackedFileMatchesHead_SeesAnEditGitDiffDeliberatelyHides.
//
// `git update-index --assume-unchanged` tells git to stop checking a file for
// modifications — a real performance flag people set on large or generated files.
// `git diff` then reports the working tree CLEAN even though the bytes differ, so
// a gate built on `git diff` would wave the migration through and commit the
// user's edit. hash-object compares content directly and is not fooled.
func TestTrackedFileMatchesHead_SeesAnEditGitDiffDeliberatelyHides(t *testing.T) {
	root := migrationRepo(t)
	rel := ".claude/rules/design.md"
	abs := filepath.Join(root, filepath.FromSlash(rel))

	matches, err := trackedFileMatchesHead(root, rel)
	if err != nil {
		t.Fatalf("trackedFileMatchesHead: %v", err)
	}
	if !matches {
		t.Fatalf("precondition: an untouched tracked file should match HEAD")
	}

	git(t, root, "update-index", "--assume-unchanged", "--", rel)
	if err := os.WriteFile(abs, []byte("MY UNCOMMITTED EDIT\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Confirm git itself is now hiding the edit, so the test is exercising the
	// real condition rather than asserting against a straw man.
	out := git(t, root, "diff", "--name-only", "--", rel)
	if strings.TrimSpace(out) != "" {
		t.Skipf("this git reports the edit despite assume-unchanged (%q); nothing to defeat", out)
	}

	matches, err = trackedFileMatchesHead(root, rel)
	if err != nil {
		t.Fatalf("trackedFileMatchesHead: %v", err)
	}
	if matches {
		t.Error("the gate believed a file matched HEAD while it held the user's uncommitted edit; " +
			"the migration would have committed their work")
	}
}

// TestHasUnstagedMigrationChanges_BlocksOnAnEditToAPathTheCommitWouldTouch.
// It covers the ADD side of the commit specifically: the on-ramp, the lockfile
// and the scoped ignore files all get `git add --force`d, so an unstaged edit to
// any of them would be swept into ox's commit as the user's unreviewed work.
func TestHasUnstagedMigrationChanges_BlocksOnAnEditToAPathTheCommitWouldTouch(t *testing.T) {
	root := migrationRepo(t)

	dirty, err := hasUnstagedMigrationChanges(root)
	if err != nil {
		t.Fatalf("hasUnstagedMigrationChanges: %v", err)
	}
	if dirty {
		t.Fatal("precondition: a freshly built fixture should be clean")
	}

	// The gate covers the files the migration ADDS to its commit — the on-ramp,
	// the lockfile, the scoped ignore files — not the ones it removes. A removal
	// target the user has edited is caught by `git rm` refusing it, which is a
	// different mechanism with its own test. So perturb a tracked footprint path.
	abs := filepath.Join(root, ".sageox", "skills.lock.json")
	if _, statErr := os.Stat(abs); statErr != nil {
		t.Skipf("fixture has no tracked lockfile to perturb: %v", statErr)
	}
	if err := os.WriteFile(abs, []byte("{\"schema_version\": 2}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dirty, err = hasUnstagedMigrationChanges(root)
	if err != nil {
		t.Fatalf("hasUnstagedMigrationChanges: %v", err)
	}
	if !dirty {
		t.Error("an unstaged edit to a file the migration commits was not detected")
	}
}

// TestHasUnstagedMigrationChanges_UntrackedUserFileAtAFootprintPathBlocks.
//
// `git diff` says nothing about untracked files, so an existence-only check would
// let `git add --force` sweep a user's own sageox/SKILL.md into ox's commit. Only
// byte-exact canonical content is adoptable; anything else must block.
func TestHasUnstagedMigrationChanges_UntrackedUserFileAtAFootprintPathBlocks(t *testing.T) {
	root := migrationRepo(t)
	rel := filepath.Join(".claude", "skills", skillmanager.CommittedOnRamp, "SKILL.md")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("---\nname: sageox\n---\nMY OWN VERSION\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dirty, err := hasUnstagedMigrationChanges(root)
	if err != nil {
		t.Fatalf("hasUnstagedMigrationChanges: %v", err)
	}
	if !dirty {
		t.Error("a user-authored file sitting at a footprint path was not treated as blocking; " +
			"`git add --force` would have committed it")
	}
}
