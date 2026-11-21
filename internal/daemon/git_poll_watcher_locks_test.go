package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The poll must never write the worktree's index: a status run that refreshes
// the stat cache takes .git/index.lock and rewrites .git/index, and the
// developer's git add or commit in the same worktree then fails with
// "index.lock: File exists". Exercised through the production gitStatus.
func TestGitPollStatusLeavesTheIndexUntouched(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@test.sageox.ai")
	git("config", "user.name", "t")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-q", "-m", "init")
	// Same content, newer mtime: the index's stat cache is stale, which is
	// exactly what makes a plain `git status` refresh and rewrite the index.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tracked, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	w := &GitPollWatcher{projectRoot: dir}
	status, err := w.gitStatus(context.Background())
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if got := status["new.txt"]; got != "??" {
		t.Fatalf("new.txt status = %q, want ?? (output control)", got)
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("gitStatus rewrote .git/index; the poll must run with --no-optional-locks")
	}
}
