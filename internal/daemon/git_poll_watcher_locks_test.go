package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The poll must never take the worktree's index lock: a status run that
// refreshes the index writes .git/index.lock, and the developer's git add or
// commit in the same worktree then fails with "index.lock: File exists".
func TestGitPollStatusTakesNoIndexLock(t *testing.T) {
	if gitStatusArgs[0] != "--no-optional-locks" {
		t.Fatalf("gitStatusArgs must start with --no-optional-locks, got %v", gitStatusArgs)
	}
	// And the command it builds still works on a real repo.
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@test.sageox.ai"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", gitStatusArgs...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if got := parsePorcelainZ(out)["a.txt"]; got != "??" {
		t.Fatalf("a.txt status = %q, want ??", got)
	}
}
