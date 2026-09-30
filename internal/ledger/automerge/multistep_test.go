package automerge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeMultiStepRebaseConflict builds a rebase whose replay range holds `steps`
// SEQUENTIALLY conflicting commits, so `git rebase --continue` halts once per
// step instead of finishing the whole rebase.
//
// The single-conflict fixtures in automerge_test.go cannot reach this shape,
// which is exactly why the multi-step defects went unnoticed: the same blind
// spot ResolveRebaseAcceptTheirs documents for tier 2.
func makeMultiStepRebaseConflict(t *testing.T, path string, steps int) string {
	t.Helper()
	repo := initTestRepo(t, t.TempDir())

	writeFile(t, repo, path, "base\n")
	mustGit(t, repo, "add", path)
	mustGit(t, repo, "commit", "-m", "base "+path)

	mustGit(t, repo, "checkout", "-b", "feature")
	for i := 1; i <= steps; i++ {
		writeFile(t, repo, path, fmt.Sprintf("feature change %d\n", i))
		mustGit(t, repo, "add", path)
		mustGit(t, repo, "commit", "-m", fmt.Sprintf("feature %d", i))
	}

	mustGit(t, repo, "checkout", "main")
	writeFile(t, repo, path, "main change\n")
	mustGit(t, repo, "add", path)
	mustGit(t, repo, "commit", "-m", "main change")

	mustGit(t, repo, "checkout", "feature")
	if out, err := runGitAllowFail(repo, "rebase", "main"); err == nil {
		t.Fatalf("expected rebase conflict, got success: %s", out)
	}
	return repo
}

// rebaseStillInProgress reports whether git still has rebase state on disk.
// git uses rebase-merge or rebase-apply depending on mode and version.
func rebaseStillInProgress(t *testing.T, repo string) bool {
	t.Helper()
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(repo, ".git", name)); err == nil {
			return true
		}
	}
	return false
}

// A rebase whose replay range holds several sequentially conflicting commits
// must be carried to completion, not abandoned after the first step.
//
// `git rebase --continue` exits NON-ZERO when it commits the current step and
// halts on the next conflicting commit. That is progress, not failure. Reading
// it as failure makes the caller abort the rebase, which restores the
// pre-rebase state and re-wedges the ledger on every attempt.
func TestResolve_CarriesRebaseThroughSequentialConflicts(t *testing.T) {
	t.Parallel()
	const steps = 3
	repo := makeMultiStepRebaseConflict(t, "notes/log.md", steps)

	// tryLLMTier gates on the binary resolving from PATH. git must be on PATH
	// for the fixture's own rebase, so it is the portable choice; runLLM is
	// then overridden and the binary is never actually invoked.
	gitBin, gitErr := exec.LookPath("git")
	if gitErr != nil {
		t.Skipf("git not on PATH: %v", gitErr)
	}

	r := New(Options{LLMBinary: gitBin})
	var resolvedSteps int
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		resolvedSteps++
		return fmt.Sprintf("merged at step %d\n", resolvedSteps), nil
	}

	ok, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve errored on a rebase that was still making progress: %v", err)
	}
	if !ok {
		t.Fatal("Resolve reported the conflicts unresolved")
	}
	if rebaseStillInProgress(t, repo) {
		t.Fatal("Resolve reported success while the rebase is still in progress")
	}
	if resolvedSteps < steps {
		t.Errorf("resolved %d conflicting step(s), want %d", resolvedSteps, steps)
	}
}

// Progress must be decided by the rebase's own step counter, not by whether the
// index happens to hold unmerged entries.
//
// "Unmerged entries exist" is a proxy, and it fails open: if a step halts with
// entries no tier staged, treating that as progress hands the same step back to
// the caller forever, burning a git subprocess per pass up to maxResolvePasses.
// The step id is the fact the proxy was standing in for.
func TestRebaseStepID_TracksStepsAndDistinguishesUnknown(t *testing.T) {
	t.Parallel()

	clean := initTestRepo(t, t.TempDir())
	if got := rebaseStepID(clean); got != "" {
		t.Errorf("no rebase in progress should read as unknown, got %q", got)
	}

	repo := makeMultiStepRebaseConflict(t, "notes/log.md", 3)
	first := rebaseStepID(repo)
	if first == "" {
		t.Fatal("mid-rebase step id must be readable, got empty (would disable progress detection)")
	}

	// Resolve the halted step by taking the incoming side, then advance.
	for _, p := range listConflicted(t, repo) {
		mustGit(t, repo, "checkout", "--theirs", "--", p)
		mustGit(t, repo, "add", "--", p)
	}
	runGitAllowFail(repo, "-c", "commit.gpgsign=false", "rebase", "--continue")

	if second := rebaseStepID(repo); second == first {
		t.Errorf("step id did not change after advancing the rebase: %q — progress would be invisible", second)
	}
}

func listConflicted(t *testing.T, repo string) []string {
	t.Helper()
	out := mustGit(t, repo, "diff", "--name-only", "--diff-filter=U")
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	return paths
}

// A step that cannot advance must surface an error, never a false "progress"
// signal. Returning progress here is what lets the caller's loop hand the same
// step back forever, burning a git subprocess per pass to maxResolvePasses.
func TestContinueRebase_StuckStepReportsErrorInsteadOfProgress(t *testing.T) {
	t.Parallel()
	repo := makeMultiStepRebaseConflict(t, "notes/log.md", 2)

	// Deliberately leave the conflict unstaged: git will refuse this step, and
	// the step id will not move.
	before := rebaseStepID(repo)
	r := New(Options{})
	done, err := r.continueRebase(context.Background(), repo)

	if done {
		t.Fatal("a refused step must not report the rebase finished")
	}
	if err == nil {
		t.Fatal("a step that cannot advance must return an error, not (false, nil) progress")
	}
	if got := rebaseStepID(repo); got != before {
		t.Errorf("step id moved (%q -> %q); fixture no longer reproduces a stuck step", before, got)
	}
	if !strings.Contains(err.Error(), "made no progress") {
		t.Errorf("error should name the stall, got: %v", err)
	}
}

// A later pass that finds a clean index has nothing for any tier to stage, so it
// must hand the step to continueRebase rather than report ErrNoConflicts — that
// sentinel means "the caller had nothing to do", which is only true on entry.
func TestResolveOneStep_LaterPassWithCleanIndexDelegatesToContinue(t *testing.T) {
	t.Parallel()
	repo := makeMultiStepRebaseConflict(t, "notes/log.md", 2)

	// Resolve and stage the halted step so the index is clean while the rebase
	// is still in progress — the exact state a later pass observes.
	for _, p := range listConflicted(t, repo) {
		mustGit(t, repo, "checkout", "--theirs", "--", p)
		mustGit(t, repo, "add", "--", p)
	}

	r := New(Options{})
	done, err := r.resolveOneStep(context.Background(), repo, false)

	if errors.Is(err, ErrNoConflicts) {
		t.Fatal("a later pass must not report ErrNoConflicts; that is an entry-only verdict")
	}
	if err != nil {
		t.Fatalf("staged step should advance or finish, got: %v", err)
	}
	// Either the rebase finished (done) or it advanced to the next conflict.
	if !done && !rebaseStillInProgress(t, repo) {
		t.Error("reported not-done while no rebase state remains")
	}
}
