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

// TestConfigureSparseCheckout_SymlinksCheckOutAsFiles: a symlink committed to
// the Ledger becomes a plain file holding the link text, both one an older
// clone already checked out and one a later pull brings in, and its target is
// never touched. Failure prevented: a teammate's committed link redirects
// ox's writes into the Ledger (plan saves, review records) to a file outside
// it.
func TestConfigureSparseCheckout_SymlinksCheckOutAsFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone and pull")
	}
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows, where git checks them out as files anyway")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join("data", "plans", "2026-10-09-p")
	src := t.TempDir()
	git(src, "init", "-q", "-b", "main")
	git(src, "config", "user.email", "test@example.com")
	git(src, "config", "user.name", "Test")
	plant := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(src, rel)); err != nil {
			t.Fatal(err)
		}
		git(src, "add", "-A")
		git(src, "commit", "-q", "-m", "plant "+rel)
	}
	remaps := filepath.Join(plan, "feedback", "remaps.json")
	plant(remaps)
	bare := filepath.Join(t.TempDir(), "ledger.git")
	git(src, "clone", "-q", "--bare", src, bare)

	clone := filepath.Join(t.TempDir(), "ledger")
	git(filepath.Dir(clone), "clone", "-q", bare, clone)
	isPlainFile := func(rel string) bool {
		t.Helper()
		info, err := os.Lstat(filepath.Join(clone, rel))
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().IsRegular()
	}
	if isPlainFile(remaps) {
		t.Fatal("setup: a clone made before the fix should hold a real link")
	}

	if err := ConfigureSparseCheckout(clone); err != nil {
		t.Fatalf("ConfigureSparseCheckout: %v", err)
	}
	if !isPlainFile(remaps) {
		t.Fatal("the link an older clone checked out must become a plain file")
	}
	if body, _ := os.ReadFile(filepath.Join(clone, remaps)); string(body) != victim {
		t.Fatalf("the file should hold the link text %q, got %q", victim, body)
	}
	if st, _ := exec.Command("git", "-C", clone, "status", "--porcelain").Output(); len(st) != 0 {
		t.Fatalf("rewriting the link must leave nothing to commit:\n%s", st)
	}

	planMD := filepath.Join(plan, "plan.md")
	plant(planMD)
	git(src, "push", "-q", bare, "main")
	git(clone, "pull", "-q")
	if !isPlainFile(planMD) {
		t.Fatal("a link pulled after the fix must arrive as a plain file")
	}
	if body, _ := os.ReadFile(victim); string(body) != "precious\n" {
		t.Fatalf("the link target was touched: %q", body)
	}
}

// TestDisableSymlinks_FailuresAreReported: a step that fails returns an error
// and leaves core.symlinks unset, so the next sync cycle runs the rewrite
// again; a link already removed stays removed, never relinked. Failure
// prevented: a Ledger reported as protected while a link is still checked out.
func TestDisableSymlinks_FailuresAreReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows")
	}
	repo := func(t *testing.T, link bool) string {
		t.Helper()
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		if link {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
				t.Fatalf("git add: %v\n%s", err, out)
			}
		}
		return dir
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr string
	}{
		{"not a git repo", func(t *testing.T) string { return t.TempDir() }, "list tracked files"},
		{"index locked during the rewrite", func(t *testing.T) string {
			dir := repo(t, true)
			writeLock(t, filepath.Join(dir, ".git", "index.lock"))
			return dir
		}, "check out symlinks as files"},
		{"config locked", func(t *testing.T) string {
			dir := repo(t, false)
			writeLock(t, filepath.Join(dir, ".git", "config.lock"))
			return dir
		}, "config core.symlinks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			err := ConfigureSparseCheckout(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.wantErr)
			}
			if out, _ := exec.Command("git", "-C", dir, "config", "--get", "core.symlinks").Output(); strings.TrimSpace(string(out)) == "false" {
				t.Fatal("core.symlinks must stay unset after a failure, so the next call retries")
			}
			if info, err := os.Lstat(filepath.Join(dir, "link")); err == nil && info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("a failed rewrite must not leave the link checked out")
			}
		})
	}
}

func writeLock(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
