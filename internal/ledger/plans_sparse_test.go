package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestConfigureSparseCheckout_IncludesDataPlans pins data/plans into the cone.
//
// Its absence made plans write-only on disk: `ox plan save` wrote and pushed a
// plan directory, then the sync scheduler's ~60s ConfigureSparseCheckout
// refresh deleted it from the working tree, leaving ledgers with dozens of
// plans on origin/main and none locally. Every local plan read path
// (`ox plan list/view/render/backfill-titles`) depends on this entry.
func TestConfigureSparseCheckout_IncludesDataPlans(t *testing.T) {
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	output, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	if !strings.Contains(string(output), "data/plans") {
		t.Errorf("sparse checkout missing data/plans in output:\n%s", output)
	}
}

// TestConfigureSparseCheckout_DataPlansIsNotWindowed guards the one way
// data/plans differs from its data/ siblings: github and murmur paths are
// rolling windows (data/github/YYYY/MM/DD, data/murmurs/YYYY-MM-DD-HH), so a
// plan saved outside the window would vanish if plans were ever given the same
// treatment. The cone must carry the bare parent directory, not a dated child.
func TestConfigureSparseCheckout_DataPlansIsNotWindowed(t *testing.T) {
	tempDir := t.TempDir()

	if err := exec.Command("git", "init", tempDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	output, err := exec.Command("git", "-C", tempDir, "sparse-checkout", "list").Output()
	if err != nil {
		t.Fatalf("sparse-checkout list: %v", err)
	}

	for _, line := range strings.Split(string(output), "\n") {
		entry := strings.Trim(strings.TrimSpace(line), "/")
		if entry == "data/plans" {
			return
		}
		if strings.HasPrefix(entry, "data/plans/") {
			t.Fatalf("data/plans is windowed as %q; a plan outside the window would be deleted from the working tree", entry)
		}
	}
	t.Fatalf("no data/plans entry in sparse-checkout list:\n%s", output)
}

// TestConfigureSparseCheckout_KeepsPlanReviewState pins the review state that
// lives below a plan dir — round files and the per-entry resolutions/ dir —
// inside the cone and un-ignored, across a sparse reapply. Resolutions moved
// from one shared resolutions.json to one file per entry (so two machines
// never rebase-conflict on them); a nested dir the cone or an ignore rule
// dropped would lose agent dispositions the same way plans once went
// write-only.
func TestConfigureSparseCheckout_KeepsPlanReviewState(t *testing.T) {
	tempDir := t.TempDir()
	git := func(args ...string) ([]byte, error) {
		full := append([]string{"-C", tempDir, "-c", "user.name=test", "-c", "user.email=test@test.sageox.ai", "-c", "commit.gpgsign=false"}, args...)
		return exec.Command("git", full...).CombinedOutput()
	}
	if out, err := exec.Command("git", "init", tempDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := ConfigureSparseCheckout(tempDir); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}

	paths := []string{
		"data/plans/2026-09-01-x/feedback/round-20260901-000000.000000000-abcd1234.json",
		"data/plans/2026-09-01-x/feedback/resolutions/20260901-000000.000000000-abcd1234.json",
	}
	for _, p := range paths {
		full := filepath.Join(tempDir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := git("check-ignore", "-q", p); err == nil {
			t.Fatalf("%s is gitignored:\n%s", p, out)
		}
	}
	if out, err := git(append([]string{"add", "--sparse"}, paths...)...); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := git("commit", "--no-verify", "-m", "review state"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if out, err := git("sparse-checkout", "reapply"); err != nil {
		t.Fatalf("sparse-checkout reapply: %v\n%s", err, out)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(tempDir, p)); err != nil {
			t.Errorf("%s dropped from the working tree by the sparse cone: %v", p, err)
		}
	}
}

// gitIn runs git in dir and returns its output, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// fileText returns the contents of path, failing the test unless it is a
// plain file.
func fileText(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is %s, want a plain file", path, info.Mode())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestConfigureSparseCheckout_SymlinksCheckOutAsFiles: a symlink committed to
// the Ledger becomes a plain file holding its link text, whether an older
// clone already checked it out or a later pull brings it in; a link
// retargeted locally keeps its new target as an uncommitted change; no
// target is touched. Failure prevented: a teammate's committed link
// redirects ox's writes into the Ledger to a file outside it.
func TestConfigureSparseCheckout_SymlinksCheckOutAsFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone and pull")
	}
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows, where git checks them out as files anyway")
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	gitIn(t, src, "init", "-q", "-b", "main")
	gitIn(t, src, "config", "user.email", "test@example.com")
	gitIn(t, src, "config", "user.name", "Test")
	plant := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(src, rel)); err != nil {
			t.Fatal(err)
		}
		gitIn(t, src, "add", "-A")
		gitIn(t, src, "commit", "-q", "-m", "plant "+rel)
	}
	remaps := "data/plans/2026-10-09-p/feedback/remaps.json"
	retargeted := "data/plans/2026-10-09-p/feedback/resolutions.json"
	plant(remaps)
	plant(retargeted)
	bare := filepath.Join(t.TempDir(), "ledger.git")
	gitIn(t, src, "clone", "-q", "--bare", src, bare)

	clone := filepath.Join(t.TempDir(), "ledger")
	gitIn(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	local := filepath.Join(t.TempDir(), "local-target")
	if err := os.Remove(filepath.Join(clone, retargeted)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(local, filepath.Join(clone, retargeted)); err != nil {
		t.Fatal(err)
	}

	if err := ConfigureSparseCheckout(clone); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}
	if got := fileText(t, filepath.Join(clone, remaps)); got != victim {
		t.Fatalf("an unchanged link should hold its link text %q, got %q", victim, got)
	}
	if got := fileText(t, filepath.Join(clone, retargeted)); got != local {
		t.Fatalf("a locally retargeted link should keep its new target %q, got %q", local, got)
	}
	if st := strings.TrimSpace(gitIn(t, clone, "status", "--porcelain")); st != "M "+retargeted {
		t.Fatalf("only the local retarget should be an uncommitted change, got:\n%s", st)
	}

	planMD := "data/plans/2026-10-09-p/plan.md"
	plant(planMD)
	gitIn(t, src, "push", "-q", bare, "main")
	gitIn(t, clone, "pull", "-q", "--rebase", "--autostash")
	if got := fileText(t, filepath.Join(clone, planMD)); got != victim {
		t.Fatalf("a link pulled afterwards should arrive as a file holding %q, got %q", victim, got)
	}
	if got := fileText(t, victim); got != "precious\n" {
		t.Fatalf("the link target was touched: %q", got)
	}
}

// TestDisableSymlinks_RetriesAndReadsTheCloneSetting: a failed run returns an
// error and leaves core.symlinks unsaved, so the next call converts the link
// and saves it; a global core.symlinks=false does not stand in for the
// clone's own. Failure prevented: a Ledger reported as protected while a link
// is still checked out, or left unprotected once a developer drops a global
// setting.
func TestDisableSymlinks_RetriesAndReadsTheCloneSetting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows")
	}
	if err := DisableSymlinks(t.TempDir()); err == nil || !strings.Contains(err.Error(), "list tracked files") {
		t.Fatalf("outside a repo: err = %v, want a listing error", err)
	}
	// repoWithLink returns a repo whose committed "link" is checked out as a
	// symlink to target.
	repoWithLink := func(t *testing.T) (dir, target string) {
		t.Helper()
		dir, target = t.TempDir(), t.TempDir()
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "config", "user.email", "test@example.com")
		gitIn(t, dir, "config", "user.name", "Test")
		if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", "link")
		return dir, target
	}
	saved := func(dir string) bool {
		out, _ := exec.Command("git", "-C", dir, "config", "--local", "--get", "core.symlinks").Output()
		return strings.TrimSpace(string(out)) == "false"
	}

	t.Run("a failed run retries", func(t *testing.T) {
		dir, target := repoWithLink(t)
		lock := filepath.Join(dir, ".git", "config.lock")
		if err := os.WriteFile(lock, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := DisableSymlinks(dir); err == nil || !strings.Contains(err.Error(), "config core.symlinks") {
			t.Fatalf("with the config locked: err = %v, want a config error", err)
		}
		if saved(dir) {
			t.Fatal("core.symlinks must stay unsaved after a failure, so the next call retries")
		}
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		if err := DisableSymlinks(dir); err != nil || !saved(dir) {
			t.Fatalf("retry: err %v, saved %v", err, saved(dir))
		}
		if got := fileText(t, filepath.Join(dir, "link")); got != target {
			t.Fatalf("link should hold its link text %q, got %q", target, got)
		}
		if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
			t.Fatalf("the retry should leave nothing to commit:\n%s", st)
		}
	})

	t.Run("a global setting is not the clone's", func(t *testing.T) {
		dir, target := repoWithLink(t)
		global := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(global, []byte("[core]\n\tsymlinks = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", global)
		if err := DisableSymlinks(dir); err != nil || !saved(dir) {
			t.Fatalf("err %v, saved in the clone %v", err, saved(dir))
		}
		if got := fileText(t, filepath.Join(dir, "link")); got != target {
			t.Fatalf("link should hold its link text %q, got %q", target, got)
		}
	})
}
