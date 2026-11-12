package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initIdentityRepo(t *testing.T, stamped bool) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if stamped {
		run("config", "user.name", "Bunyan")
		run("config", "user.email", "agt_bunyan@coworkers.sageox.ai")
		run("config", authorStampKey, "Bunyan <agt_bunyan@coworkers.sageox.ai>")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	return dir
}

func setHarnessIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Paul Bunyan")
	t.Setenv("GIT_AUTHOR_EMAIL", "bunyan@sageox.ai")
	t.Setenv("GIT_COMMITTER_NAME", "Paul Bunyan")
	t.Setenv("GIT_COMMITTER_EMAIL", "bunyan@sageox.ai")
}

func headIdents(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%an <%ae>|%cn <%ce>")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=", "GIT_COMMITTER_NAME=") // safe: git log only, cmd.Dir is a temp repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A hook inherits the coding agent's GIT_AUTHOR_*/GIT_COMMITTER_*; in a clone
// ox stamped, ox's commit must still carry the stamped identity (ox#1845).
func TestRunGitStampedCloneIgnoresInheritedIdentity(t *testing.T) {
	dir := initIdentityRepo(t, true)
	setHarnessIdentity(t)
	if _, err := RunGit(context.Background(), dir, "commit", "-q", "-m", "x"); err != nil {
		t.Fatal(err)
	}
	want := "Bunyan <agt_bunyan@coworkers.sageox.ai>|Bunyan <agt_bunyan@coworkers.sageox.ai>"
	if got := headIdents(t, dir); got != want {
		t.Fatalf("commit identity = %q, want %q", got, want)
	}
}

func TestCommitTreeStampedCloneIgnoresInheritedCommitter(t *testing.T) {
	dir := initIdentityRepo(t, true)
	setHarnessIdentity(t)
	tree, err := runPlumbing(context.Background(), dir, nil, nil, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if err := commitTreeToBranch(context.Background(), dir, strings.TrimSpace(string(tree)), nil, "", "x",
		[]string{"GIT_AUTHOR_NAME=Orig", "GIT_AUTHOR_EMAIL=orig@example.com"}); err != nil {
		t.Fatal(err)
	}
	want := "Orig <orig@example.com>|Bunyan <agt_bunyan@coworkers.sageox.ai>"
	if got := headIdents(t, dir); got != want {
		t.Fatalf("commit identity = %q, want %q", got, want)
	}
}

// A repo ox never stamped keeps the identity the environment chose.
func TestRunGitUnstampedRepoKeepsInheritedIdentity(t *testing.T) {
	dir := initIdentityRepo(t, false)
	setHarnessIdentity(t)
	if _, err := RunGit(context.Background(), dir, "commit", "-q", "-m", "x"); err != nil {
		t.Fatal(err)
	}
	want := "Paul Bunyan <bunyan@sageox.ai>|Paul Bunyan <bunyan@sageox.ai>"
	if got := headIdents(t, dir); got != want {
		t.Fatalf("commit identity = %q, want %q", got, want)
	}
}
