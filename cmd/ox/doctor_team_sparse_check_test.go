//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// seedSparseTeamContext builds a team-context clone in the GH #862 shape: its
// own manifest includes agents/, but the sparse spec omits it, so the directory
// exists in HEAD and is absent from the working tree.
func seedSparseTeamContext(t *testing.T) string {
	t.Helper()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	repo := t.TempDir()
	run(repo, "init", "--initial-branch=main")
	run(repo, "config", "user.email", "t@example.com")
	run(repo, "config", "user.name", "T")
	for rel, content := range map[string]string{
		".sageox/sync.manifest": "version 1\ninclude .sageox/\ninclude agents/\ninclude memory/\n",
		"agents/rules/team.md":  "team rule\n",
		"memory/MEMORY.md":      "memory\n",
		"README.md":             "root\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-q", "-m", "seed")
	run(repo, "sparse-checkout", "init", "--no-cone")
	// deliberately omit /agents/ — this is the #862 shape
	run(repo, "sparse-checkout", "set", "--no-cone", "/*", "!/*/", "/.sageox/", "/memory/")

	if _, err := os.Stat(filepath.Join(repo, "agents")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: agents/ should be absent from the working tree (err=%v)", err)
	}
	return repo
}

// TestCheckTeamSparseCheckout_ReportsAndRepairsTheManifestOmission drives the
// doctor check itself, not just its helpers.
//
// Failure prevented: GH #862 — the tracked manifest omitted agents/, sparse
// checkout never materialized it, and team rules silently never reached any
// client while this check reported everything fine. The helper that detects the
// omission was tested; the check that reports and repairs it was not, so a
// regression in the reporting or repair branch would ship unnoticed.
func TestCheckTeamSparseCheckout_ReportsAndRepairsTheManifestOmission(t *testing.T) {
	teamPath := seedSparseTeamContext(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()
	requireSageoxDir(t, gitRoot)

	if err := config.SaveLocalConfig(gitRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "team-862", TeamName: "Engineering", Path: teamPath},
		},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	// --- report mode: name the omission, change nothing ---
	result := checkTeamSparseCheckout(false)
	if result.passed {
		t.Error("check must fail while a manifest-included directory is unmaterialized")
	}
	if !strings.Contains(result.message, "agents/") {
		t.Errorf("message must name the missing directory, got: %s", result.message)
	}
	if !strings.Contains(result.message, "already included by their manifest") {
		t.Errorf("message must classify this as locally repairable, got: %s", result.message)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents")); !os.IsNotExist(err) {
		t.Error("report mode must not modify the checkout")
	}

	// --- fix mode: actually materialize it ---
	if fixed := checkTeamSparseCheckout(true); fixed.passed != true && len(fixed.detail) == 0 {
		t.Errorf("unexpected empty result from fix mode: %+v", fixed)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents", "rules", "team.md")); err != nil {
		t.Fatalf("fix mode must materialize the manifest-included directory: %v", err)
	}

	// --- converged: a repaired checkout reports clean ---
	if after := checkTeamSparseCheckout(false); !after.passed {
		t.Errorf("check must pass once the directory is materialized, got: %s", after.detail)
	}
}

// TestCheckTeamSparseCheckout_UnmaterializedNeedsServerFixNotLocalRepair pins
// the branch that separates "ox can fix this" from "only the server can".
//
// A directory that exists in HEAD but not in the working tree, whose absence
// the tracked manifest does NOT contradict, cannot be repaired locally: the
// manifest is generated server-side and the tracked copy wins over the client
// fallback, so re-applying the sparse spec would faithfully re-exclude it.
//
// Failure prevented: ox claiming a local repair it cannot perform. `--fix` would
// report success, the content would still be absent, and the next run would
// report the same thing forever — while the actual defect (a manifest that omits
// content the commit carries, GH #862) went unreported to the only people who
// can fix it.
func TestCheckTeamSparseCheckout_UnmaterializedNeedsServerFixNotLocalRepair(t *testing.T) {
	teamPath := seedSparseTeamContext(t)

	// Rewrite the manifest so it no longer includes agents/. The directory is
	// still in HEAD and still absent from the working tree, but now nothing
	// claims it should be there — which is exactly the server-side omission.
	manifestPath := filepath.Join(teamPath, ".sageox", "sync.manifest")
	if err := os.WriteFile(manifestPath,
		[]byte("version 1\ninclude .sageox/\ninclude memory/\n"), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()
	requireSageoxDir(t, gitRoot)

	if err := config.SaveLocalConfig(gitRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "team-862b", TeamName: "Engineering", Path: teamPath},
		},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	result := checkTeamSparseCheckout(false)

	if !strings.Contains(result.message, "missing directories that exist in HEAD") {
		t.Errorf("must report the HEAD-vs-worktree gap, got: %s", result.message)
	}
	if !strings.Contains(result.detail, "server-side manifest fix") {
		t.Errorf("must name the server-side remedy rather than implying a local fix, got: %s", result.detail)
	}
	// A warning, not a failure: nothing here is broken on this machine, and it
	// must not become a --fix target that can never converge.
	if !result.passed || !result.warning {
		t.Errorf("unrepairable-locally must be a warning, not a failure: passed=%v warning=%v",
			result.passed, result.warning)
	}

	// And --fix must not pretend otherwise.
	if fixed := checkTeamSparseCheckout(true); !strings.Contains(fixed.message, "missing directories that exist in HEAD") {
		t.Errorf("--fix must not claim to have repaired a server-side omission, got: %s", fixed.message)
	}
}

// TestManifestIncludedMissingDirs_NoManifestClaimsNothing.
// Failure prevented: treating "we could not read a manifest" as "the manifest
// includes everything", which would route every missing directory into the
// locally-repairable branch and make ox re-apply a sparse spec it never read.
func TestManifestIncludedMissingDirs_NoManifestClaimsNothing(t *testing.T) {
	if got := manifestIncludedMissingDirs(nil, []string{"agents/", "memory/"}); got != nil {
		t.Errorf("a nil manifest must claim no directories, got %v", got)
	}
}

// TestCheckTeamSparseCheckout_RepairFailureIsReportedNotSwallowed covers the
// branch where ox tries a local repair and git refuses.
//
// Failure prevented: a --fix that reports success after its repair failed. The
// directory is still unmaterialized, so the coworker's team rules are still
// absent, but doctor has told them it fixed the problem — the worst of the
// three possible outcomes, because it ends the investigation.
//
// The fixture replaces .git/info/sparse-checkout with a DIRECTORY, so git
// cannot write the new spec. Portable on every platform, unlike a chmod or
// symlink fixture (bead ox-avjb).
func TestCheckTeamSparseCheckout_RepairFailureIsReportedNotSwallowed(t *testing.T) {
	teamPath := seedSparseTeamContext(t)

	// Break git's ability to update the working tree while leaving the sparse
	// spec itself readable — the check skips any repo whose spec it cannot read,
	// so an unreadable spec would never reach the repair branch at all.
	idx := filepath.Join(teamPath, ".git", "index")
	if err := os.Remove(idx); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if err := os.MkdirAll(idx, 0o755); err != nil {
		t.Fatalf("fixture: .git/index must be a directory: %v", err)
	}

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()
	requireSageoxDir(t, gitRoot)

	if err := config.SaveLocalConfig(gitRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "team-862c", TeamName: "Engineering", Path: teamPath},
		},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	result := checkTeamSparseCheckout(true)

	if result.passed {
		t.Error("a failed repair must not report success")
	}
	if !strings.Contains(result.message, "agents/") {
		t.Errorf("the still-missing directory must be named, got: %s", result.message)
	}
	// still absent on disk — the report matches reality
	if _, err := os.Stat(filepath.Join(teamPath, "agents")); !os.IsNotExist(err) {
		t.Error("fixture invalid: the repair must genuinely have failed")
	}
}
