package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// A global author.email outranks a local user.email, so ox must set the
// stamped identity rather than only drop the inherited one.
func TestRunGitStampedCloneOutranksGlobalAuthorEmail(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[author]\n\temail = personal@example.com\n[committer]\n\temail = personal@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
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

// The daemon's pull --rebase runs through NewNetworkCmd and rewrites local
// commits; the rewritten committer must be the stamped identity.
func TestNewNetworkCmdRebaseUsesStampedCommitter(t *testing.T) {
	dir := initIdentityRepo(t, true)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "upstream")
	git("commit", "-q", "--allow-empty", "-m", "upstream")
	git("checkout", "-q", "main")
	git("commit", "-q", "--allow-empty", "-m", "local")
	setHarnessIdentity(t)
	if out, err := NewNetworkCmd(context.Background(), "-C", dir, "rebase", "-q", "upstream").CombinedOutput(); err != nil {
		t.Fatalf("rebase: %v: %s", err, out)
	}
	cmd := exec.Command("git", "log", "-1", "--format=%cn <%ce>")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(out)), "Bunyan <agt_bunyan@coworkers.sageox.ai>"; got != want {
		t.Fatalf("rebased committer = %q, want %q", got, want)
	}
}

func TestOxIdentityEnvCanceledContextSkipsRead(t *testing.T) {
	dir := initIdentityRepo(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := []string{"GIT_AUTHOR_NAME=Paul Bunyan"}
	if got := oxIdentityEnv(ctx, dir, env); !slices.Equal(got, env) {
		t.Fatalf("oxIdentityEnv with canceled context = %v, want %v", got, env)
	}
}
