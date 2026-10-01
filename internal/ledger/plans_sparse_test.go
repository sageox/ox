package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
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
