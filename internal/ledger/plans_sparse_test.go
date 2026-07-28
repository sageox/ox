package ledger

import (
	"os/exec"
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
