package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
)

// legacyMigration is the one-time transition that takes ox's own files out of a
// customer's git history.
//
// Gitignoring them is not enough: ignore rules do not apply to files git already
// tracks, so until the old paths leave the index they keep appearing in diffs. And
// there is no way to untrack without a commit. So ox writes exactly one, and every
// design choice below exists to make that commit safe.
type legacyMigration struct {
	repoRoot string

	// uncache are ox-owned paths that stay ON DISK but leave the index. These are
	// current reserved-prefix artifacts: the ignore rules already match them, so
	// once untracked they become properly ignored rather than untracked noise.
	uncache []string

	// remove are superseded LEGACY names (ox-plan/, ox.md, ox-prime.md). They leave
	// both the index and the working tree, because nothing matches them any more —
	// left on disk they would show up as untracked files, and the next `git add .`
	// would put the whole migration straight back.
	remove []string

	// preserved are paths that wear an ox name but whose bytes ox cannot verify as
	// its own. They are left tracked and untouched: these predate the reserved-prefix
	// contract, so their author never agreed to ox owning that path.
	preserved []string
}

// Empty reports whether there is nothing to migrate.
func (m *legacyMigration) Empty() bool { return len(m.uncache)+len(m.remove) == 0 }

// planLegacyMigration classifies every tracked path under the agent directories.
//
// Classification reads bytes from DISK, never from the index. A user can hide an
// edit from `git status` with skip-worktree or assume-unchanged, and trusting the
// index would then let ox delete work it cannot see.
func planLegacyMigration(repoRoot string) (*legacyMigration, error) {
	m := &legacyMigration{repoRoot: repoRoot}

	tracked, err := trackedAgentPaths(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, rel := range tracked {
		switch classifyLegacyPath(repoRoot, rel) {
		case legacyReserved:
			m.uncache = append(m.uncache, rel)
		case legacySuperseded:
			m.remove = append(m.remove, rel)
		case legacyUserOwned:
			m.preserved = append(m.preserved, rel)
		}
	}
	return m, nil
}

type legacyClass int

const (
	legacyUserOwned legacyClass = iota
	legacyReserved
	legacySuperseded
)

func classifyLegacyPath(repoRoot, rel string) legacyClass {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return legacyUserOwned
	}
	surface := parts[0] + "/" + parts[1]
	name := strings.TrimSuffix(parts[2], ".md")

	switch surface {
	case ".claude/skills", ".agents/skills":
		if skillmanager.IsReservedName(name) {
			return legacyReserved
		}
		if skills.IsRetired(name) {
			return legacySuperseded
		}
	case ".claude/rules", ".factory/rules", ".claude/commands":
		if skillmanager.IsReservedName(name) {
			return legacyReserved
		}
		// A legacy rule or command is removed only when its ox stamp still verifies
		// against its body. An ox-stamped file the user has since EDITED no longer
		// hashes to its stamp, and deleting it would destroy their work.
		if skills.IsRetired(name) || (surface == ".claude/rules" && len(parts) > 3 && parts[2] == "sageox") {
			if stampVerifies(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return legacySuperseded
			}
			return legacyUserOwned
		}
	}
	return legacyUserOwned
}

func stampVerifies(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, prefix := range []string{"ox", agentx.DefaultStampPrefix} {
		if hash, _, body := adapterstamp.ExtractStampAnywhere(data, prefix); hash != "" && agentx.ContentHash(body) == hash {
			return true
		}
	}
	return false
}

func trackedAgentPaths(repoRoot string) ([]string, error) {
	args := []string{"ls-files", "-z", "--",
		".claude/skills", ".claude/rules", ".claude/commands",
		".agents/skills", ".agents/rules", ".factory/rules"}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		// A repository with none of these directories is not an error.
		return nil, nil
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// migrationBlocker reports why tier 2 must not run right now, or "" when it may.
//
// Every condition here is a way the one commit ox writes could damage something.
// The checks are deliberately conservative: doing nothing is always recoverable,
// and the migration will simply run on the next invocation.
func migrationBlocker(repoRoot string) string {
	// A recording session means an AI coworker is mid-turn in this repository.
	// `ox doctor` is routinely run BY an agent during a session, so this is not a
	// theoretical case — and a commit appearing under the user's session is exactly
	// the surprise the #732 ruling was about.
	if session.IsRecording(repoRoot) {
		return "a session is recording; rerun `ox doctor --fix` when it has stopped"
	}

	gitDir, err := resolvedGitDir(repoRoot)
	if err != nil {
		return "cannot resolve the git directory"
	}
	// .git is a FILE in a linked worktree, so a naive filepath.Join(root, ".git",
	// "MERGE_HEAD") stat returns ENOTDIR and silently reports "nothing in flight"
	// during a live merge. Conductor runs every workspace as a worktree, so this is
	// the common case here, not the exotic one.
	for _, c := range []struct{ marker, op string }{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, c.marker)); err == nil {
			return "a " + c.op + " is in progress"
		}
	}

	if unborn, err := headIsUnborn(repoRoot); err != nil || unborn {
		// On an unborn branch the migration commit would BE the root commit, and
		// would carry whatever the user had staged as their initial import.
		return "the branch has no commits yet"
	}
	if detached, err := headIsDetached(repoRoot); err != nil || detached {
		// A commit on a detached HEAD is orphaned by the next checkout, so the
		// migration would appear to succeed and then silently un-happen.
		return "HEAD is detached"
	}

	// Any staged change at all blocks the commit.
	//
	// This is stricter than "clean for the affected paths", and deliberately so: a
	// bare `git commit` records the ENTIRE index, so a user with unrelated staged
	// work would find it swallowed into ox's housekeeping commit. That exact
	// failure has already happened in this codebase once — a chore commit that
	// recorded 10,373 unrelated deletions.
	staged, err := hasStagedChanges(repoRoot)
	if err != nil {
		return "cannot read the git index"
	}
	if staged {
		return "you have staged changes; commit or unstage them and rerun `ox doctor --fix`"
	}
	return ""
}

func resolvedGitDir(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--absolute-git-dir")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func headIsUnborn(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		return true, nil
	}
	return false, nil
}

func headIsDetached(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		return true, nil
	}
	return false, nil
}

func hasStagedChanges(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = repoRoot
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// MigrationCommitSubject is the one commit ox writes into a customer repository.
const MigrationCommitSubject = "chore(sageox): stop tracking ox-managed agent files"

// Apply performs tier 2: untrack, then commit.
//
// On any failure the index is restored, so a rejecting pre-commit hook, an
// unavailable GPG agent, or an unset user.email leaves the repository exactly as
// it was rather than stranding thirty staged deletions the guard would then refuse
// to clear on the next run.
func (m *legacyMigration) Apply() (err error) {
	if m.Empty() {
		return nil
	}
	defer func() {
		if err != nil {
			// Restore the index to HEAD. Safe precisely because migrationBlocker
			// already established there were no staged changes of the user's own.
			reset := exec.Command("git", "reset", "--quiet", "HEAD", "--")
			reset.Dir = m.repoRoot
			_ = reset.Run()
			// Bring back any working-tree file `git rm` deleted.
			restore := exec.Command("git", "checkout", "--", ".")
			restore.Dir = m.repoRoot
			_ = restore.Run()
		}
	}()

	if len(m.uncache) > 0 {
		// --cached: the file stays on disk. It is a CURRENT managed artifact whose
		// name the ignore rules already match, so untracking it is all that is
		// needed for it to become properly ignored.
		if err := runMigrationGit(m.repoRoot, append([]string{"rm", "--cached", "--quiet", "--"}, m.uncache...)); err != nil {
			return fmt.Errorf("untrack managed files: %w", err)
		}
	}
	if len(m.remove) > 0 {
		// No --cached: a superseded legacy name matches no ignore rule, so leaving
		// it on disk would show it as an untracked file and the next `git add .`
		// would re-track the whole migration.
		if err := runMigrationGit(m.repoRoot, append([]string{"rm", "--quiet", "--"}, m.remove...)); err != nil {
			return fmt.Errorf("remove superseded files: %w", err)
		}
	}

	// Stage the ignore files in the SAME commit. They are what make the untracked
	// paths ignored rather than untracked noise, so shipping the untrack without
	// them would leave every teammate's `git status` full of ox files — and the
	// next `git add .` would put the whole migration straight back.
	//
	// --force because plenty of repositories root-ignore .claude/ (people do it
	// because of settings.local.json); without it the one file that has to reach
	// teammates would be silently skipped.
	for _, f := range scopedIgnoreFiles() {
		rel := filepath.Join(f.dir, ".gitignore")
		if _, statErr := os.Stat(filepath.Join(m.repoRoot, rel)); statErr != nil {
			continue
		}
		if err := runMigrationGit(m.repoRoot, []string{"add", "--force", "--", rel}); err != nil {
			return fmt.Errorf("stage ignore rules: %w", err)
		}
	}

	body := "ox now materializes its skills and rules locally and gitignores them,\n" +
		"so upgrading ox no longer changes any tracked file in this repository.\n\n" +
		"Reverting this commit is safe: `ox doctor --fix` will redo it."
	if err := runMigrationGit(m.repoRoot, []string{"commit", "--quiet", "-m", MigrationCommitSubject, "-m", body}); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func runMigrationGit(dir string, args []string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args[:1], " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
