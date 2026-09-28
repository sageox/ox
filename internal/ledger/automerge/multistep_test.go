package automerge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
