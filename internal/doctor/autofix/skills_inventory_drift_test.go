package autofix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func driftRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@sageox.example"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

// TestCheckSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo: a repository
// that never ran `ox init` has no skill targets. Materializing into it would put
// vendor files in a project that never asked for them — the daemon must not make
// that choice on the user's behalf.
func TestCheckSkillsInventoryDrift_NeverInstallsIntoAnUnselectedRepo(t *testing.T) {
	root := driftRepo(t)

	res := checkSkillsInventoryDrift(context.Background(), root)

	if res.Status != StatusClean {
		t.Errorf("unselected repo produced status %v (%s)", res.Status, res.Summary)
	}
	for _, dir := range []string{".claude", ".agents", ".factory"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			t.Errorf("the daemon created %s/ in a repository that never selected ox", dir)
		}
	}
}

// TestCheckSkillsInventoryDrift_EmptyRepoPathIsANoOp guards the daemon's own
// bookkeeping: an unresolved repo path must never be treated as the process cwd.
func TestCheckSkillsInventoryDrift_EmptyRepoPathIsANoOp(t *testing.T) {
	res := checkSkillsInventoryDrift(context.Background(), "")
	if res.Status != StatusClean || res.Summary != "" {
		t.Errorf("empty repo path produced work: status=%v summary=%q", res.Status, res.Summary)
	}
}

// TestCheckSkillsInventoryDrift_CanceledContextStopsBeforeAnyWork: the daemon
// cancels its tick on shutdown. The check must observe that before touching a
// repository, not midway through an apply.
func TestCheckSkillsInventoryDrift_CanceledContextStopsBeforeAnyWork(t *testing.T) {
	root := driftRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := checkSkillsInventoryDrift(ctx, root)

	if res.Status != StatusClean {
		t.Errorf("canceled tick reported work: status=%v (%s)", res.Status, res.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error("the daemon wrote into a repository after its context was canceled")
	}
}

// TestCheckSkillsInventoryDrift_ReportsAnUnreadableLockfileInsteadOfGuessing:
// a corrupt lockfile must surface as an error, never be silently treated as
// "no targets" — that would let the daemon quietly stop maintaining a repo.
func TestCheckSkillsInventoryDrift_ReportsAnUnreadableLockfileInsteadOfGuessing(t *testing.T) {
	root := driftRepo(t)
	lockDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "skills.lock.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := checkSkillsInventoryDrift(context.Background(), root)

	if res.Status != StatusError {
		t.Errorf("a corrupt lockfile was not reported: status=%v summary=%q", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "lockfile") {
		t.Errorf("summary does not name the problem: %q", res.Summary)
	}
}
