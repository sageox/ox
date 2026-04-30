package automerge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a fresh git repo in dir and returns its path.
// Test git identity is set per-repo (NEVER changes the user's global config).
func initTestRepo(t *testing.T, dir string) string {
	t.Helper()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test User")
	// initial commit
	writeFile(t, dir, "README", "init\n")
	mustGit(t, dir, "add", "README")
	mustGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_TERMINAL_PROMPT=0") // safe: tempdir git subprocess; layered env disables editor + prompts
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// runGitAllowFail runs git and returns output regardless of exit code.
// Used for commands like `rebase` that may exit non-zero on conflict.
func runGitAllowFail(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_TERMINAL_PROMPT=0") // safe: same as mustGit; tolerates non-zero exit (rebase conflicts)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, dir, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// makeRebaseConflict produces a repo whose `git rebase main` is halted on
// a conflict in the named path. The conflicted file ends up in the
// working tree with standard <<<<<<< markers. Returns the repo path.
func makeRebaseConflict(t *testing.T, path, oursContent, theirsContent string) string {
	t.Helper()
	repo := initTestRepo(t, t.TempDir())

	// branch off, make 'theirs' on main, 'ours' on feature
	writeFile(t, repo, path, "base\n")
	mustGit(t, repo, "add", path)
	mustGit(t, repo, "commit", "-m", "base "+path)

	mustGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, repo, path, oursContent)
	mustGit(t, repo, "add", path)
	mustGit(t, repo, "commit", "-m", "ours")

	mustGit(t, repo, "checkout", "main")
	writeFile(t, repo, path, theirsContent)
	mustGit(t, repo, "add", path)
	mustGit(t, repo, "commit", "-m", "theirs")

	mustGit(t, repo, "checkout", "feature")
	// rebase main onto feature; expect conflict
	if out, err := runGitAllowFail(repo, "rebase", "main"); err == nil {
		t.Fatalf("expected rebase conflict, got success: %s", out)
	}
	return repo
}

func TestResolve_NoConflicts(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t, t.TempDir())
	r := New(Options{})
	ok, err := r.Resolve(context.Background(), repo)
	if ok {
		t.Errorf("expected ok=false")
	}
	if !errors.Is(err, ErrNoConflicts) {
		t.Errorf("expected ErrNoConflicts, got: %v", err)
	}
}

func TestResolve_AcceptTheirsTier(t *testing.T) {
	t.Parallel()
	repo := makeRebaseConflict(t, "data/feed.json", `{"v":"ours"}`+"\n", `{"v":"theirs"}`+"\n")

	r := New(Options{SafePrefixes: []string{"data/"}})
	ok, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}

	// rebase should be done — neither rebase-merge nor rebase-apply dir
	// should exist (git uses one or the other depending on mode/version).
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(repo, ".git", name)); statErr == nil {
			t.Errorf("%s dir still exists; rebase didn't continue", name)
		}
	}

	// after `git rebase main` from feature, the "incoming" side (the
	// commit being replayed) is feature's content. ResolveRebaseAcceptTheirs
	// picks that side. We just verify the file is no longer in conflict.
	got := readFile(t, repo, "data/feed.json")
	if hasConflictMarkers([]byte(got)) {
		t.Errorf("expected resolved content, got conflict markers: %s", got)
	}
}

func TestResolve_LLMTier(t *testing.T) {
	t.Parallel()
	repo := makeRebaseConflict(t, "config.toml", "version = \"ours\"\n", "version = \"theirs\"\n")

	r := New(Options{LLMBinary: "fake-llm"})
	// The LookPath gate in tryLLMTier requires the binary to resolve from
	// PATH. Use `git` — it must be on PATH for the rebase setup itself to
	// work, so this is portable across darwin/linux/windows. runLLM is then
	// overridden to ignore the binary entirely.
	gitBin, gitErr := exec.LookPath("git")
	if gitErr != nil {
		t.Skipf("git not on PATH: %v", gitErr)
	}
	r.opts.LLMBinary = gitBin
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		return "version = \"ours-and-theirs\"\n", nil
	}

	ok, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	got := readFile(t, repo, "config.toml")
	if !strings.Contains(got, "ours-and-theirs") {
		t.Errorf("expected merged content, got: %s", got)
	}
}

func TestResolve_LLMTier_DisabledWithoutBinary(t *testing.T) {
	t.Parallel()
	repo := makeRebaseConflict(t, "src/main.go", "ours\n", "theirs\n")

	// no SafePrefixes match src/, no LLMBinary configured
	r := New(Options{})
	ok, err := r.Resolve(context.Background(), repo)
	if ok {
		t.Errorf("expected ok=false")
	}
	if err == nil || !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("expected ErrLLMUnavailable sentinel, got: %v", err)
	}
}

func TestTryUnionTier_StagesCleanFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)

	// simulate post-union output: file in working tree has no markers
	writeFile(t, repo, "AGENTS.md", "ours\ntheirs\n")
	// simulate the file being in a conflicted index state by adding it
	// via update-index — easier: just verify the helper's stage decision
	// via direct call.
	r := New(Options{})
	remaining, err := r.tryUnionTier(context.Background(), repo, []string{"AGENTS.md"})
	if err != nil {
		t.Fatalf("tryUnionTier: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining, got %v", remaining)
	}
}

func TestTryUnionTier_LeavesMarkedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)
	writeFile(t, repo, "x.go", "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> br\n")

	r := New(Options{})
	remaining, err := r.tryUnionTier(context.Background(), repo, []string{"x.go"})
	if err != nil {
		t.Fatalf("tryUnionTier: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "x.go" {
		t.Errorf("expected x.go remaining, got %v", remaining)
	}
}

func TestHasConflictMarkers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain", "hello\n", false},
		{"marker at start of line", "a\n<<<<<<< HEAD\nb\n", true},
		{"marker mid-line is not a marker", "echo \"<<<<<<< embedded\"\n", false},
		{"only at line start", "<<<<<<< X\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasConflictMarkers([]byte(tc.in)); got != tc.want {
				t.Errorf("hasConflictMarkers(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchesSafePrefix(t *testing.T) {
	t.Parallel()
	if !matchesSafePrefix("data/x.json", []string{"data/"}, nil) {
		t.Error("expected data/x.json safe under data/")
	}
	if matchesSafePrefix("src/main.go", []string{"data/"}, nil) {
		t.Error("expected src/main.go NOT safe")
	}
	if matchesSafePrefix("data/secret/x", []string{"data/"}, []string{"data/secret/"}) {
		t.Error("deny prefix should override")
	}
	if !matchesSafePrefix("data/secret/public/x", []string{"data/", "data/secret/public/"}, []string{"data/secret/"}) {
		t.Error("most-specific allow should win")
	}
}
