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
