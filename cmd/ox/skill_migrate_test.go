//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// migrationRepo builds a repository in the state a real customer is in before the
// transition: ox's files tracked at HEAD, exactly as earlier ox versions staged
// them.
func migrationRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	// Identity is set locally so the test never depends on — or touches — the
	// developer's global git config.
	git(t, root, "config", "user.email", "test@sageox.example")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "commit.gpgsign", "false")

	// ox-owned, current reserved names
	writeRepoFile(t, root, ".claude/skills/ox-cli-plan/SKILL.md", "managed\n")
	writeRepoFile(t, root, ".claude/rules/ox-cli.md", "managed rule\n")
	// ox-owned, superseded legacy names carrying a VERIFYING stamp
	writeRepoFile(t, root, ".claude/commands/ox-prime.md",
		string(agentx.StampedContent([]byte("legacy prime\n"), "0.14.0", "ox")))
	// user-owned, must survive untouched
	writeRepoFile(t, root, ".claude/skills/my-skill/SKILL.md", "mine\n")
	writeRepoFile(t, root, ".claude/rules/design.md", "my rule\n")
	// ox-named but user-EDITED: stamp no longer verifies, so ox must not remove it
	writeRepoFile(t, root, ".claude/commands/ox-status.md",
		"<!-- ox-hash: deadbeefcafe ver: 0.14.0 -->\nI edited this\n")

	// A recorded skill target: the replacement surface EXISTS. Without it the
	// migration correctly refuses to remove the legacy command files, because
	// doing so would leave the project with no ox surface at all.
	writeRepoFile(t, root, ".sageox/skills.lock.json", `{
  "schema_version": 2,
  "desired": {"bundles": ["core"], "targets": ["claude-project"]},
  "targets": [{"key": "claude-project", "root": ".claude/skills", "format": "agent-skills/v1", "scope": "project", "link_policy": "reject"}]
}`)

	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "initial")
	return root
}

func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func trackedPaths(t *testing.T, root string) []string {
	t.Helper()
	out := git(t, root, "ls-files")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// TestLegacyMigration_NeverCommitsUnrelatedStagedWork is the highest-blast-radius
// case in this whole change, and the likeliest: a dirty index is the normal state
// of a working developer.
//
// A bare `git commit` records the ENTIRE index, so ox's housekeeping commit would
// swallow whatever the user had staged. This codebase has already shipped that bug
// once — a "chore: add .gitignore" commit that recorded 10,373 unrelated deletions.
//
// The guard is therefore "no staged changes at all", which is stricter than "clean
// for the affected paths": the affected-paths test would pass while the user's
// unrelated file sat staged and ready to be swept in.
func TestLegacyMigration_NeverCommitsUnrelatedStagedWork(t *testing.T) {
	root := migrationRepo(t)
	headBefore := git(t, root, "rev-parse", "HEAD")

	writeRepoFile(t, root, "src/feature.go", "package main\n")
	git(t, root, "add", "src/feature.go")

	if reason := migrationBlocker(root); reason == "" {
		t.Fatal("migration was allowed to commit while the user had unrelated staged work")
	} else if !strings.Contains(reason, "staged") {
		t.Errorf("blocker should name the staged changes, got %q", reason)
	}

	if got := git(t, root, "rev-parse", "HEAD"); got != headBefore {
		t.Error("HEAD moved despite the guard")
	}
	staged := git(t, root, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "src/feature.go") {
		t.Errorf("the user's staged work was disturbed, got %q", staged)
	}
}

// TestLegacyMigration_UntracksOxFilesAndPreservesEverythingElse is the
// post-migration invariant set: what leaves git, what stays on disk, and what ox
// must never touch.
func TestLegacyMigration_UntracksOxFilesAndPreservesEverythingElse(t *testing.T) {
	root := migrationRepo(t)

	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if reason := migrationBlocker(root); reason != "" {
		t.Fatalf("unexpected blocker in a clean repo: %s", reason)
	}
	if err := m.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	tracked := trackedPaths(t, root)
	joined := strings.Join(tracked, "\n")

	for _, gone := range []string{
		".claude/skills/ox-cli-plan/SKILL.md",
		".claude/rules/ox-cli.md",
		".claude/commands/ox-prime.md",
	} {
		if strings.Contains(joined, gone) {
			t.Errorf("%s is still tracked; it would keep appearing in pull requests", gone)
		}
	}
	for _, kept := range []string{
		".claude/skills/my-skill/SKILL.md",
		".claude/rules/design.md",
		".claude/commands/ox-status.md", // ox-named but user-edited
	} {
		if !strings.Contains(joined, kept) {
			t.Errorf("%s was untracked; ox removed something it does not own", kept)
		}
	}

	// Reserved artifacts stay ON DISK (they are the live managed files, now ignored).
	for _, onDisk := range []string{".claude/skills/ox-cli-plan/SKILL.md", ".claude/rules/ox-cli.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(onDisk))); err != nil {
			t.Errorf("%s left the working tree; the agent would lose the skill: %v", onDisk, err)
		}
	}
	// Superseded legacy names leave disk too, or the next `git add .` re-tracks them.
	if _, err := os.Stat(filepath.Join(root, ".claude/commands/ox-prime.md")); err == nil {
		t.Error("superseded ox-prime.md remained on disk; `git add .` would re-track the migration")
	}

	// The working tree must be clean afterwards: leftover untracked ox files are how
	// the whole migration gets undone by one `git add .`.
	if status := git(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("working tree not clean after migration:\n[%s]", status)
	}

	// The ignore rules ride in the same commit — without them the untracked paths
	// would be noise in every teammate's `git status`.
	if !strings.Contains(joined, ".claude/.gitignore") {
		t.Error(".claude/.gitignore was not committed; teammates would not inherit the ignore rules")
	}

	// Exactly one commit, with ox's subject.
	if subject := git(t, root, "log", "-1", "--pretty=%s"); subject != MigrationCommitSubject {
		t.Errorf("unexpected commit subject %q", subject)
	}
	// ...and it is revertable.
	git(t, root, "revert", "--no-edit", "HEAD")
	if !strings.Contains(strings.Join(trackedPaths(t, root), "\n"), ".claude/skills/ox-cli-plan/SKILL.md") {
		t.Error("reverting the migration commit did not restore the previous state")
	}
}

// TestLegacyMigration_CommitFailureLeavesTheRepositoryUnchanged covers ordinary
// causes: a pre-commit hook that rejects, a GPG agent that is not available, an
// unset user.email. Without rollback the repository is left holding staged
// deletions and no commit — and the guard then refuses to run again, because those
// staged deletions are themselves "staged changes".
func TestLegacyMigration_CommitFailureLeavesTheRepositoryUnchanged(t *testing.T) {
	root := migrationRepo(t)
	headBefore := git(t, root, "rev-parse", "HEAD")
	trackedBefore := strings.Join(trackedPaths(t, root), "\n")

	hooks := filepath.Join(t.TempDir(), "reject-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	git(t, root, "config", "core.hooksPath", hooks)

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if err := m.Apply(); err == nil {
		t.Fatal("Apply reported success even though the pre-commit hook rejected the commit")
	}

	if got := git(t, root, "rev-parse", "HEAD"); got != headBefore {
		t.Error("HEAD moved despite the failed commit")
	}
	if got := strings.Join(trackedPaths(t, root), "\n"); got != trackedBefore {
		t.Errorf("the index was left mutated after a failed commit:\n--- before ---\n%s\n--- after ---\n%s", trackedBefore, got)
	}
	if status := git(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("working tree left dirty after a failed commit:\n[%s]", status)
	}

	// And once the cause is removed, the migration completes.
	git(t, root, "config", "--unset", "core.hooksPath")
	m2, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("replan: %v", err)
	}
	if reason := migrationBlocker(root); reason != "" {
		t.Fatalf("rollback left the repository in a state the guard rejects: %s", reason)
	}
	if err := m2.Apply(); err != nil {
		t.Fatalf("migration did not complete after the cause was removed: %v", err)
	}
}

// TestLegacyMigration_StandsDownDuringGitOperations. In a linked worktree .git is
// a FILE, so a naive stat of "<root>/.git/MERGE_HEAD" returns ENOTDIR and reports
// "nothing in flight" during a live merge. Conductor runs every workspace as a
// worktree, so that is the common case here, not the exotic one.
func TestLegacyMigration_StandsDownDuringGitOperations(t *testing.T) {
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "CHERRY_PICK_HEAD", "BISECT_LOG"} {
		t.Run(marker, func(t *testing.T) {
			root := migrationRepo(t)
			gitDir, err := resolvedGitDir(root)
			if err != nil {
				t.Fatalf("resolve git dir: %v", err)
			}
			target := filepath.Join(gitDir, marker)
			if marker == "rebase-merge" {
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatalf("create %s: %v", marker, err)
				}
			} else if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
				t.Fatalf("create %s: %v", marker, err)
			}

			if reason := migrationBlocker(root); reason == "" {
				t.Errorf("migration would have committed during an in-progress operation (%s)", marker)
			}
		})
	}
}

// TestLegacyMigration_StandsDownOnDetachedHeadAndUnbornBranch: a commit on a
// detached HEAD is orphaned by the next checkout, so the migration would appear to
// succeed and then silently un-happen; on an unborn branch it would become the
// root commit carrying the user's entire initial import.
func TestLegacyMigration_StandsDownOnDetachedHeadAndUnbornBranch(t *testing.T) {
	t.Run("detached HEAD", func(t *testing.T) {
		root := migrationRepo(t)
		git(t, root, "checkout", "--detach", "-q", "HEAD")
		if reason := migrationBlocker(root); !strings.Contains(reason, "detached") {
			t.Errorf("expected a detached-HEAD blocker, got %q", reason)
		}
	})
	t.Run("unborn branch", func(t *testing.T) {
		root := t.TempDir()
		git(t, root, "init", "--initial-branch=main")
		if reason := migrationBlocker(root); !strings.Contains(reason, "no commits") {
			t.Errorf("expected an unborn-branch blocker, got %q", reason)
		}
	})
}

// TestLegacyMigration_InFlightGuardWorksInALinkedWorktree is C1 made concrete.
//
// In a linked worktree, .git is a FILE, not a directory. The in-flight detection
// used elsewhere in doctor stats "<root>/.git/MERGE_HEAD" directly, which returns
// ENOTDIR there and therefore reports "nothing in flight" during a live merge —
// so ox would commit into the middle of the user's operation. Conductor runs every
// workspace as a linked worktree, so this is the common case for this team, not an
// exotic one.
func TestLegacyMigration_InFlightGuardWorksInALinkedWorktree(t *testing.T) {
	main := migrationRepo(t)
	wt := filepath.Join(t.TempDir(), "linked")
	git(t, main, "worktree", "add", "-q", "-b", "wt-branch", wt)

	// Sanity: .git really is a file here, or the test proves nothing.
	info, err := os.Lstat(filepath.Join(wt, ".git"))
	if err != nil || info.IsDir() {
		t.Fatalf("expected .git to be a FILE in a linked worktree (err=%v)", err)
	}

	gitDir, err := resolvedGitDir(wt)
	if err != nil {
		t.Fatalf("resolve git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("stage MERGE_HEAD: %v", err)
	}

	if reason := migrationBlocker(wt); !strings.Contains(reason, "merge") {
		t.Errorf("in-flight merge not detected inside a linked worktree; ox would commit mid-merge. got %q", reason)
	}
}

// TestLegacyMigration_NeverRemovesTheOnlySurface is the gate that stops the
// migration from being a pure regression.
//
// The legacy .claude/commands files ARE the entire ox surface for a project that
// predates the skills installer. Removing them where no skill target is recorded
// leaves the user with neither — /ox-prime and every other lifecycle command gone,
// and nothing installed to replace them. That happened on a real repository before
// this gate existed.
func TestLegacyMigration_NeverRemovesTheOnlySurface(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.email", "t@e.example")
	git(t, root, "config", "user.name", "T")

	// A pre-skills project: ox commands, no skills lockfile, no skill target.
	writeRepoFile(t, root, ".claude/commands/ox-prime.md",
		string(agentx.StampedContent([]byte("legacy prime\n"), "0.14.0", "ox")))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "initial")

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	for _, rel := range m.remove {
		if strings.HasPrefix(rel, ".claude/commands/") {
			t.Errorf("%s would be removed with no replacement installed; the project would lose its only ox surface", rel)
		}
	}
	if len(m.preserved) == 0 {
		t.Error("the legacy command should be preserved and reported, not silently dropped from the plan")
	}
}

// TestLegacyMigration_RollbackDoesNotDiscardUnrelatedUnstagedWork is the
// data-loss guard on the failure path.
//
// migrationBlocker refuses to run when anything is STAGED, but it deliberately
// tolerates unstaged work — a developer mid-edit is the normal case. A repo-wide
// `git checkout -- .` in the rollback would therefore reach past ox's own paths
// and silently revert their work in progress, turning a failed housekeeping
// commit into lost work.
func TestLegacyMigration_RollbackDoesNotDiscardUnrelatedUnstagedWork(t *testing.T) {
	root := migrationRepo(t)

	// The developer's work in progress: tracked, edited, NOT staged.
	writeRepoFile(t, root, "src/feature.go", "package main\n")
	git(t, root, "add", "src/feature.go")
	git(t, root, "commit", "-q", "-m", "add feature")
	inProgress := "package main\n\n// half-written thought I have not saved anywhere else\n"
	writeRepoFile(t, root, "src/feature.go", inProgress)

	// Force the commit to fail.
	hooks := filepath.Join(t.TempDir(), "reject-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	git(t, root, "config", "core.hooksPath", hooks)

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if err := m.Apply(); err == nil {
		t.Fatal("Apply should have failed on the rejecting hook")
	}

	got, err := os.ReadFile(filepath.Join(root, "src", "feature.go"))
	if err != nil {
		t.Fatalf("read feature.go: %v", err)
	}
	if string(got) != inProgress {
		t.Errorf("the rollback discarded unrelated unstaged work.\n--- want ---\n%q\n--- got ---\n%q", inProgress, string(got))
	}
}

func TestLegacyMigration_PreservesUnverifiedRetiredSkillTree(t *testing.T) {
	root := migrationRepo(t)
	manifest := ".claude/skills/ox-plan/SKILL.md"
	notes := ".claude/skills/ox-plan/notes.md"
	writeRepoFile(t, root, manifest,
		"<!-- ox-hash: deadbeefcafe ver: 0.14.0 -->\nI customized this retired skill\n")
	writeRepoFile(t, root, notes, "notes that were never part of ox's manifest\n")
	git(t, root, "add", "--", manifest, notes)
	git(t, root, "commit", "-q", "-m", "customize retired skill")

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	removed := strings.Join(m.remove, "\n")
	if strings.Contains(removed, manifest) || strings.Contains(removed, notes) {
		t.Fatalf("unverified retired skill content was scheduled for deletion: %v", m.remove)
	}
	preserved := strings.Join(m.preserved, "\n")
	for _, rel := range []string{manifest, notes} {
		if !strings.Contains(preserved, rel) {
			t.Errorf("%s was not reported as preserved: %v", rel, m.preserved)
		}
	}
}

func TestLegacyMigration_AgentsTargetDoesNotReplaceClaudeCommands(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.email", "t@e.example")
	git(t, root, "config", "user.name", "T")

	command := ".claude/commands/ox-prime.md"
	writeRepoFile(t, root, command,
		string(agentx.StampedContent([]byte("legacy prime\n"), "0.14.0", "ox")))
	writeRepoFile(t, root, ".sageox/skills.lock.json", `{
  "schema_version": 2,
  "desired": {"bundles": ["core"], "targets": ["portable-project"]},
  "targets": [{"key": "portable-project", "root": ".agents/skills", "format": "agent-skills/v1", "scope": "project", "link_policy": "reject"}]
}`)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "legacy Claude command with portable target")

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if m.replacementAvailable {
		t.Fatal(".agents/skills was treated as a Claude Code replacement")
	}
	if strings.Contains(strings.Join(m.remove, "\n"), command) {
		t.Fatalf("Claude command would be removed without a .claude/skills target: %v", m.remove)
	}
	if !strings.Contains(strings.Join(m.preserved, "\n"), command) {
		t.Fatalf("Claude command was not preserved: %v", m.preserved)
	}
}

func TestLegacyMigration_FailedCommitPreservesUnstagedUncachedBytes(t *testing.T) {
	root := migrationRepo(t)
	managed := filepath.Join(root, ".claude", "skills", "ox-cli-plan", "SKILL.md")
	inProgress := "my local experiment in a still-tracked managed file\n"
	if err := os.WriteFile(managed, []byte(inProgress), 0o644); err != nil {
		t.Fatalf("edit managed file: %v", err)
	}

	hooks := filepath.Join(t.TempDir(), "reject-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	git(t, root, "config", "core.hooksPath", hooks)

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if err := m.Apply(); err == nil {
		t.Fatal("Apply should fail on the rejecting hook")
	}
	got, err := os.ReadFile(managed)
	if err != nil {
		t.Fatalf("read managed file after rollback: %v", err)
	}
	if string(got) != inProgress {
		t.Fatalf("rollback discarded unstaged managed bytes: got %q want %q", got, inProgress)
	}
}

func TestLegacyMigration_BlocksDirtyCommittedFootprint(t *testing.T) {
	root := migrationRepo(t)
	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	git(t, root, "add", "--force", "--", ".claude/.gitignore")
	git(t, root, "commit", "-q", "-m", "track scoped ignore")
	ignorePath := filepath.Join(root, ".claude", ".gitignore")
	f, err := os.OpenFile(ignorePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open ignore file: %v", err)
	}
	if _, err := f.WriteString("my-local-rule/\n"); err != nil {
		_ = f.Close()
		t.Fatalf("edit ignore file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close ignore file: %v", err)
	}

	reason := migrationBlocker(root)
	if !strings.Contains(reason, "unstaged changes in files the migration must commit") {
		t.Fatalf("dirty committed footprint did not block migration: %q", reason)
	}
}

func TestLegacyMigration_StagesOnlyOnRampManifest(t *testing.T) {
	root := migrationRepo(t)
	manifest := ".claude/skills/sageox/SKILL.md"
	notes := ".claude/skills/sageox/notes.md"
	writeRepoFile(t, root, manifest, "on-ramp\n")
	writeRepoFile(t, root, notes, "user notes\n")
	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if err := m.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	tracked := strings.Join(trackedPaths(t, root), "\n")
	if !strings.Contains(tracked, manifest) {
		t.Errorf("on-ramp manifest was not adopted: %s", tracked)
	}
	if strings.Contains(tracked, notes) {
		t.Errorf("user file beside on-ramp was swept into migration commit: %s", tracked)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(notes))); err != nil {
		t.Errorf("user file beside on-ramp did not remain on disk: %v", err)
	}
}

// TestLegacyMigration_CommitsTheIgnoreFilesAndConverges pins the two halves of
// the ignore-file lifecycle.
//
// Prime and the daemon WRITE the scoped .gitignore — that is what protects the
// local checkout the moment reserved files appear — but neither can commit. So
// the migration has to adopt them, or they sit untracked forever and a teammate's
// fresh clone gets no rule at all: the first ox run there puts vendor files
// straight back into their pull request.
//
// The convergence half matters just as much: once committed, the plan must stop
// reporting them, or `ox doctor` nags about the same files on every run.
func TestLegacyMigration_CommitsTheIgnoreFilesAndConverges(t *testing.T) {
	root := migrationRepo(t)

	// The state prime leaves behind: ignore files written, nothing committed.
	if _, err := ensureScopedIgnoreFiles(root); err != nil {
		t.Fatalf("ensureScopedIgnoreFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", ".gitignore")); err != nil {
		t.Fatalf("precondition: ignore file should exist on disk: %v", err)
	}

	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if err := m.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	tracked := strings.Join(trackedPaths(t, root), "\n")
	if !strings.Contains(tracked, ".claude/.gitignore") {
		t.Error(".claude/.gitignore was not committed; teammates would inherit no rule")
	}

	// Converges: a second plan has no ignore-file work left.
	m2, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("replan: %v", err)
	}
	for _, rel := range m2.adopt {
		if strings.HasSuffix(rel, ".gitignore") {
			t.Errorf("%s reported as outstanding work after it was committed; doctor would nag every run", rel)
		}
	}
}

// TestLegacyMigration_FailedRemovalNeverClobbersUnstagedWork covers the rollback's
// blast radius.
//
// `git rm` validates its whole pathspec up front and refuses the ENTIRE invocation
// when any path carries local modifications — so a failure there deletes nothing.
// The rollback used to check out every path in m.remove regardless, which meant a
// refused housekeeping commit silently overwrote the user's unstaged edits in
// files the migration had never touched. migrationBlocker deliberately tolerates
// unstaged work (a developer mid-edit is the normal case), so this is the common
// path, not an exotic one.
func TestLegacyMigration_FailedRemovalNeverClobbersUnstagedWork(t *testing.T) {
	root := migrationRepo(t)

	// Plan first, exactly as doctor does: classification runs against a clean
	// stamped file, so the path lands in m.remove.
	m, err := planLegacyMigration(root)
	if err != nil {
		t.Fatalf("planLegacyMigration: %v", err)
	}
	if len(m.remove) == 0 {
		t.Fatal("fixture no longer plans any superseded removal; the test would prove nothing")
	}

	// The user then starts editing one of those paths — the window CodeRabbit
	// named, between the blocker check and the git rm. git rm now refuses its
	// entire pathspec and deletes nothing.
	edited := filepath.Join(root, filepath.FromSlash(m.remove[0]))
	userBytes := "MY UNSAVED WORK\n"
	if err := os.WriteFile(edited, []byte(userBytes), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}

	// Apply is expected to fail here; what matters is what it leaves behind.
	_ = m.Apply()

	got, readErr := os.ReadFile(edited)
	if readErr != nil {
		t.Fatalf("the rollback deleted a file it never removed: %v", readErr)
	}
	if string(got) != userBytes {
		t.Errorf("rollback overwrote the user's unstaged work with HEAD:\n got: %q\nwant: %q", got, userBytes)
	}
}
