//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/manifest"
)

// seedSparseTeamContext builds a team-context clone whose manifest includes
// agents/ but whose sparse spec omits it, so the directory exists in HEAD and
// is absent from the working tree.
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
	files := map[string]string{
		".sageox/sync.manifest": "version 1\ninclude .sageox/\ninclude agents/\ninclude coworkers/\ninclude memory/\n",
		"agents/rules/team.md":  "team rule\n",
		"coworkers/helper.md":   "coworker\n",
		"memory/MEMORY.md":      "memory\n",
		"README.md":             "root\n",
	}
	for rel, content := range files {
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
	run(repo, "sparse-checkout", "set", "--no-cone", "/*", "!/*/", "/.sageox/", "/coworkers/", "/memory/")

	if _, err := os.Stat(filepath.Join(repo, "agents")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: agents/ should be absent from the working tree (err=%v)", err)
	}
	return repo
}

// TestCheckTeamSparseCheckout_ReportsAndRepairsManifestIncludedDirectory drives
// the doctor check itself, not just its helpers.
//
// Failure prevented: a stale local sparse spec can omit a directory the current
// manifest includes. The helper that detects the omission was tested; the check
// that reports and repairs it was not, so a regression in the reporting or
// repair branch would ship unnoticed.
func TestCheckTeamSparseCheckout_ReportsAndRepairsManifestIncludedDirectory(t *testing.T) {
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
	if !strings.Contains(result.message, "ox can restore locally") {
		t.Errorf("message must classify this as locally repairable, got: %s", result.message)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents")); !os.IsNotExist(err) {
		t.Error("report mode must not modify the checkout")
	}

	// --- fix mode: actually materialize it ---
	if fixed := checkTeamSparseCheckout(true); !fixed.passed || fixed.warning {
		t.Errorf("fix mode must repair the manifest-included directory: %+v", fixed)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents", "rules", "team.md")); err != nil {
		t.Fatalf("fix mode must materialize the manifest-included directory: %v", err)
	}

	// --- converged: a repaired checkout reports clean ---
	if after := checkTeamSparseCheckout(false); !after.passed {
		t.Errorf("check must pass once the directory is materialized, got: %s", after.detail)
	}
}

// TestCheckTeamSparseCheckout_OmittedRequiredDirRepairsLocally is the real GH
// #862 shape: the tracked manifest omits agents/, but the client sparse policy
// floors it in because ox reads team rules and skills from that directory.
func TestCheckTeamSparseCheckout_OmittedRequiredDirRepairsLocally(t *testing.T) {
	teamPath := seedSparseTeamContext(t)

	// Rewrite the manifest so it no longer includes agents/. The committed
	// directory remains absent from the working tree until doctor reapplies the
	// client policy that floors it into the sparse set.
	manifestPath := filepath.Join(teamPath, ".sageox", "sync.manifest")
	if err := os.WriteFile(manifestPath,
		[]byte("version 1\ninclude .sageox/\ninclude coworkers/\ninclude memory/\n"), 0o644); err != nil {
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
	if result.passed || result.warning {
		t.Errorf("a locally repairable omission must fail, not warn: passed=%v warning=%v", result.passed, result.warning)
	}
	if !strings.Contains(result.message, "agents/") || !strings.Contains(result.message, "ox can restore locally") {
		t.Errorf("must name agents/ and the local remedy, got: %s", result.message)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents")); !os.IsNotExist(err) {
		t.Error("report mode must not modify the checkout")
	}

	fixed := checkTeamSparseCheckout(true)
	if !fixed.passed || fixed.warning {
		t.Errorf("fix mode must repair the omitted required directory: %+v", fixed)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "agents", "rules", "team.md")); err != nil {
		t.Fatalf("fix mode must materialize the committed team rule: %v", err)
	}
	if after := checkTeamSparseCheckout(false); !after.passed || after.warning {
		t.Errorf("check must converge after repair: %+v", after)
	}
}

func TestCheckTeamSparseCheckout_NonRequiredOmissionNeedsServerFix(t *testing.T) {
	teamPath := seedSparseTeamContext(t)

	// Simulate the server-side manifest omitting coworkers/ while keeping the
	// client's required agents/ directory explicitly included.
	manifestPath := filepath.Join(teamPath, ".sageox", "sync.manifest")
	if err := os.WriteFile(manifestPath,
		[]byte("version 1\ninclude .sageox/\ninclude agents/\ninclude memory/\n"), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	// Materialize required agents/ while excluding coworkers/, which is part of
	// the expected team-context shape but is not a client-required floor.
	cmd := exec.Command("git", "sparse-checkout", "set", "--no-cone",
		"/*", "!/*/", "/.sageox/", "/memory/", "/agents/")
	cmd.Dir = teamPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set sparse checkout: %v: %s", err, out)
	}

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()
	requireSageoxDir(t, gitRoot)
	if err := config.SaveLocalConfig(gitRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "team-server", TeamName: "Engineering", Path: teamPath}},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	result := checkTeamSparseCheckout(false)
	if !result.passed || !result.warning {
		t.Errorf("a non-required omission must remain a warning: %+v", result)
	}
	if !strings.Contains(result.message, "coworkers/") || !strings.Contains(result.detail, "server-side manifest fix") {
		t.Errorf("must name the missing directory and server remedy: message=%q detail=%q", result.message, result.detail)
	}
	if fixed := checkTeamSparseCheckout(true); !fixed.passed || !fixed.warning {
		t.Errorf("--fix must not claim a server-only omission was repaired: %+v", fixed)
	}
}

func TestLocallyRepairableMissingDirs_UnionDenyAndDedup(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *manifest.ManifestConfig
		missing []string
		want    []string
	}{
		{
			name:    "required floor applies without manifest",
			missing: []string{"agents/", "memory/"},
			want:    []string{"agents/"},
		},
		{
			name: "manifest and floor are deduplicated",
			cfg: &manifest.ManifestConfig{
				Includes: []string{"agents/", "memory/"},
			},
			missing: []string{"agents/", "memory/"},
			want:    []string{"agents/", "memory/"},
		},
		{
			name: "explicit deny wins",
			cfg: &manifest.ManifestConfig{
				Includes: []string{"agents/", "memory/"},
				Denies:   []string{"agents/"},
			},
			missing: []string{"agents/", "memory/"},
			want:    []string{"memory/"},
		},
		{
			name: "nested deny removes overlapping manifest include",
			cfg: &manifest.ManifestConfig{
				Includes: []string{"docs/"},
				Denies:   []string{"docs/private/"},
			},
			missing: []string{"docs/"},
		},
		{
			name: "nested deny preserves required floor",
			cfg: &manifest.ManifestConfig{
				Denies: []string{"agents/private/"},
			},
			missing: []string{"agents/"},
			want:    []string{"agents/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := locallyRepairableMissingDirs(tt.cfg, tt.missing)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("locallyRepairableMissingDirs() = %v, want %v", got, tt.want)
			}
		})
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
