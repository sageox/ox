package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// newSymlinkCheckProject builds the minimum state checkProjectSymlinks requires:
// a git repo config.IsInitialized accepts, a project config carrying an endpoint
// and repo id, and XDG_DATA_HOME redirected into the test's temp dir so
// DefaultLedgerPath never resolves into the developer's real ~/.local/share.
//
// TeamID is deliberately absent so teamTarget stays empty and the check exercises
// exactly one link — the assertions then attribute a failure to the ledger link
// rather than to whichever of two links happened to be wrong.
func newSymlinkCheckProject(t *testing.T) (repoRoot, ledgerTarget string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))

	repoRoot = filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".sageox"), 0o755); err != nil {
		t.Fatalf("mkdir .sageox: %v", err)
	}
	initCmd := exec.Command("git", "init")
	initCmd.Dir = repoRoot
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	const repoID = "repo_01jfk3mabprojectsymlink"
	const ep = "https://sageox.example"
	cfg, err := json.Marshal(map[string]string{"endpoint": ep, "repo_id": repoID})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".sageox", "config.json"), cfg, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	ledgerTarget = config.DefaultLedgerPath(repoID, ep)
	t.Chdir(repoRoot)
	return repoRoot, ledgerTarget
}

func linkLedger(t *testing.T, repoRoot, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(repoRoot, ".sageox", "ledger")); err != nil {
		t.Fatalf("symlink ledger: %v", err)
	}
}

// TestCheckProjectSymlinks_DanglingTargetIsNotHealthy is the regression this
// check existed to catch and did not: os.Readlink succeeds on a symlink whose
// target has been deleted, so a project whose ledger clone was removed reported
// "ok" and doctor moved on. The customer symptom is a repo that looks healthy
// while every ledger read fails.
//
// It also pins the second half of the bug: relinking cannot repair a dangling
// link, because createOrUpdateSymlink no-ops when the link already points at the
// requested target. A fix run must not claim a repair it did not perform.
func TestCheckProjectSymlinks_DanglingTargetIsNotHealthy(t *testing.T) {
	repoRoot, ledgerTarget := newSymlinkCheckProject(t)
	if err := os.MkdirAll(ledgerTarget, 0o755); err != nil {
		t.Fatalf("create ledger target: %v", err)
	}
	linkLedger(t, repoRoot, ledgerTarget)

	// Control: with the target present the check is genuinely clean. Without this
	// the test below would pass even if the check reported every project broken.
	if res := checkProjectSymlinks(false); !res.passed || res.warning {
		t.Fatalf("healthy ledger symlink should pass cleanly, got passed=%v warning=%v msg=%q",
			res.passed, res.warning, res.message)
	}

	// The link is now correct and dangling — exactly the state that used to pass.
	if err := os.RemoveAll(ledgerTarget); err != nil {
		t.Fatalf("remove ledger target: %v", err)
	}

	res := checkProjectSymlinks(false)
	if !res.warning {
		t.Errorf("dangling ledger symlink reported healthy: passed=%v warning=%v msg=%q",
			res.passed, res.warning, res.message)
	}
	if !strings.Contains(res.message, "dangling") {
		t.Errorf("message should name the dangling state so the operator knows relinking will not help, got %q", res.message)
	}

	// --fix must not launder a dangling link into a success.
	fixed := checkProjectSymlinks(true)
	if !fixed.warning {
		t.Errorf("--fix reported success for a dangling link it cannot repair: msg=%q", fixed.message)
	}
	if !strings.Contains(fixed.message, "dangling") {
		t.Errorf("--fix result should still name the dangling link, got %q", fixed.message)
	}
	if _, err := os.Lstat(filepath.Join(repoRoot, ".sageox", "ledger")); err != nil {
		t.Errorf("--fix must not delete the dangling link, leaving nothing behind: %v", err)
	}
}

// TestCheckProjectSymlinks_WrongTargetIsRepaired proves the pre-existing
// relink path still works, so the dangling change did not narrow the check into
// only reporting and never fixing.
func TestCheckProjectSymlinks_WrongTargetIsRepaired(t *testing.T) {
	repoRoot, ledgerTarget := newSymlinkCheckProject(t)
	if err := os.MkdirAll(ledgerTarget, 0o755); err != nil {
		t.Fatalf("create ledger target: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "some-other-ledger")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("create decoy: %v", err)
	}
	linkLedger(t, repoRoot, elsewhere)

	if res := checkProjectSymlinks(false); !res.warning {
		t.Fatalf("symlink pointing at the wrong target should warn, got msg=%q", res.message)
	}
	if res := checkProjectSymlinks(true); res.warning {
		t.Fatalf("--fix should repair a wrong-target link, got warning msg=%q", res.message)
	}

	got, err := os.Readlink(filepath.Join(repoRoot, ".sageox", "ledger"))
	if err != nil {
		t.Fatalf("readlink after fix: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(ledgerTarget) {
		t.Errorf("link not repointed: got %q want %q", got, ledgerTarget)
	}
}

// TestCheckProjectSymlinks_MissingLinkIsCreated covers the ordinary repair so a
// regression in the classification refactor cannot silently downgrade "missing"
// into an unfixable state.
func TestCheckProjectSymlinks_MissingLinkIsCreated(t *testing.T) {
	repoRoot, ledgerTarget := newSymlinkCheckProject(t)
	if err := os.MkdirAll(ledgerTarget, 0o755); err != nil {
		t.Fatalf("create ledger target: %v", err)
	}

	if res := checkProjectSymlinks(false); !res.warning {
		t.Fatalf("absent ledger symlink should warn, got msg=%q", res.message)
	}
	if res := checkProjectSymlinks(true); res.warning {
		t.Fatalf("--fix should create the missing link, got warning msg=%q", res.message)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".sageox", "ledger")); err != nil {
		t.Errorf("link not created by --fix: %v", err)
	}
}
