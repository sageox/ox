//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isFailed reports whether a doctor check result is a failure (not passed and
// not skipped). There is no dedicated `failed` field on checkResult.
func isFailed(r checkResult) bool {
	return !r.passed && !r.skipped
}

// --- doctor rules-drift check (ox-fvjh.4) ---
//
// These tests exercise checkAdapterRules end-to-end against REAL adapter
// binaries. The rules-drift detection logic lives inside the adapter binaries
// (handleCheckRules), so a fake script can't exercise it — we build the actual
// ox-adapter-claude-code and ox-adapter-droid binaries into a temp dir and
// point discovery at them via OX_ADAPTER_PATH.

// buildRulesAdapters compiles the rules-capable adapter binaries into a fresh
// temp dir and returns that dir (suitable for OX_ADAPTER_PATH). Slow: requires
// `go build`, so callers must be guarded by skipIntegration / -short.
func buildRulesAdapters(t *testing.T) string {
	t.Helper()

	moduleRoot := findModuleRoot(t)
	binDir := t.TempDir()

	for _, pkg := range []struct{ name, path string }{
		{"ox-adapter-claude-code", "./cmd/ox-adapter-claude-code"},
		{"ox-adapter-droid", "./cmd/ox-adapter-droid"},
	} {
		out := filepath.Join(binDir, pkg.name)
		cmd := exec.Command("go", "build", "-o", out, pkg.path)
		cmd.Dir = moduleRoot
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build %s: %v\n%s", pkg.name, err, combined)
		}
	}
	return binDir
}

// findModuleRoot walks up from this test file's own location to the ox module
// root. Anchored on runtime.Caller (not os.Getwd) so it stays correct even after
// a test has chdir'd into a temp repo via changeToDir.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(thisFile)
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if strings.Contains(string(data), "github.com/sageox/ox") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "could not find ox module root")
		dir = parent
	}
}

// installRulesViaAdapter installs ox rules for one adapter binary into repoRoot.
func installRulesViaAdapter(t *testing.T, adapterBin, repoRoot string) {
	t.Helper()
	cmd := exec.Command(adapterBin, "install-rules", "--repo-root", repoRoot, "--version", "0.1.0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install-rules via %s failed: %v\n%s", adapterBin, err, out)
	}
}

// TestCheckAdapterRules_NoRulesAdapter_SkipsGracefully verifies that a repo with
// NO rules-capable adapter discovered (the codex-only case) does not error — the
// check skips, since the deterministic floor reaches every agent via Layer 1.
// Failure prevented: doctor crashes or reports failure on codex-only repos that
// legitimately have no installed rule files.
func TestCheckAdapterRules_NoRulesAdapter_SkipsGracefully(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// point discovery at an empty dir so NO adapters are found
	t.Setenv("OX_ADAPTER_PATH", t.TempDir())

	result := checkAdapterRules(false)

	assert.False(t, isFailed(result), "no rules adapter must not fail: %+v", result)
	assert.True(t, result.skipped, "no rules adapter must skip gracefully: %+v", result)
}

// TestCheckAdapterRules_StaleBody_DetectedAndFixed is the core acceptance test:
// a hand-edited .claude/rules/ox.md body is reported stale by checkAdapterRules,
// and --fix restores it so a subsequent check passes.
// Failure prevented: installed Layer-2 rules silently drift from the live binary
// with no detection and no repair path (Bug 1), and a tampered frontmatter'd
// body is invisible to staleness (Bug 2).
func TestCheckAdapterRules_StaleBody_DetectedAndFixed(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	adapterDir := buildRulesAdapters(t)
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	claudeBin := filepath.Join(adapterDir, "ox-adapter-claude-code")
	droidBin := filepath.Join(adapterDir, "ox-adapter-droid")
	installRulesViaAdapter(t, claudeBin, gitRoot)
	installRulesViaAdapter(t, droidBin, gitRoot)

	// baseline: freshly installed rules pass for all rules-capable adapters.
	require.True(t, checkAdapterRules(false).passed, "precondition: clean install must pass")

	// hand-edit the frontmatter'd top-level claude rule body (append to the body
	// below the stamp — the stamp covers the body WITHOUT frontmatter, so this is
	// exactly the drift agentx's first-line check can't see).
	claudeRule := filepath.Join(gitRoot, ".claude", "rules", "ox.md")
	orig, err := os.ReadFile(claudeRule)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(claudeRule, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	stale := checkAdapterRules(false)
	assert.True(t, isFailed(stale), "edited claude rule body must be reported failed: %+v", stale)
	assert.Contains(t, stale.message, "claude-code", "drift must be labeled by adapter name: %q", stale.message)

	// --fix restores the rule.
	fixed := checkAdapterRules(true)
	assert.True(t, fixed.passed, "--fix must restore the drifted rule: %+v", fixed)

	// subsequent check is green.
	assert.True(t, checkAdapterRules(false).passed, "check must pass after --fix")
}

// TestCheckAdapterRules_DroidStaleBody_DetectedAndFixed mirrors the acceptance
// test for droid's .factory/rules/ox.md — both rules-capable adapters must be
// covered, not just the first discovered one.
// Failure prevented: the check only inspects one adapter (like findCommandsAdapter)
// and droid drift goes undetected.
func TestCheckAdapterRules_DroidStaleBody_DetectedAndFixed(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	adapterDir := buildRulesAdapters(t)
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	claudeBin := filepath.Join(adapterDir, "ox-adapter-claude-code")
	droidBin := filepath.Join(adapterDir, "ox-adapter-droid")
	installRulesViaAdapter(t, claudeBin, gitRoot)
	installRulesViaAdapter(t, droidBin, gitRoot)

	require.True(t, checkAdapterRules(false).passed, "precondition: clean install must pass")

	droidRule := filepath.Join(gitRoot, ".factory", "rules", "ox.md")
	orig, err := os.ReadFile(droidRule)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(droidRule, append(orig, []byte("\n\ndroid drift\n")...), 0o644))

	stale := checkAdapterRules(false)
	assert.True(t, isFailed(stale), "edited droid rule body must be reported failed: %+v", stale)
	assert.Contains(t, stale.message, "droid", "drift must be labeled by adapter name: %q", stale.message)

	fixed := checkAdapterRules(true)
	assert.True(t, fixed.passed, "--fix must restore the drifted droid rule: %+v", fixed)
	assert.True(t, checkAdapterRules(false).passed, "check must pass after --fix")
}

// TestCheckAdapterRules_NamespacedFrontmatterDrift_Detected is the targeted
// regression for Bug 2: the frontmatter'd sageox/use-team-context.md pointer
// rule, when its body is hand-edited, must be reported stale through the doctor
// check (it would pass today via raw agentx Validate, which only reads line 1).
// Failure prevented: a drifted team-context pointer rule passes doctor while
// teaching the agent stale discovery instructions.
func TestCheckAdapterRules_NamespacedFrontmatterDrift_Detected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	adapterDir := buildRulesAdapters(t)
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	claudeBin := filepath.Join(adapterDir, "ox-adapter-claude-code")
	installRulesViaAdapter(t, claudeBin, gitRoot)
	// droid not installed here — its rules will read as missing, which still
	// exercises aggregation, but we assert specifically on the namespaced edit.

	pointer := filepath.Join(gitRoot, ".claude", "rules", "sageox", "use-team-context.md")
	orig, err := os.ReadFile(pointer)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pointer, append(orig, []byte("\n\npointer drift\n")...), 0o644))

	result := checkAdapterRules(false)
	assert.True(t, isFailed(result), "namespaced frontmatter drift must be detected: %+v", result)
	assert.Contains(t, result.message, "use-team-context.md",
		"the drifted namespaced rule must be named in the report: %q", result.message)
}
