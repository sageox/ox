package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Install lifecycle ---

// TestHandleInstallRules_CreatesFile verifies that installing rules writes
// ox.md to .factory/rules/ with a valid agentx stamp.
// Failure prevented: install silently succeeds without writing any files.
func TestHandleInstallRules_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	resp, err := handleInstallRules(adapterprotocol.RulesParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)

	assert.True(t, resp.Installed)
	// repo-relative per the adapterprotocol FilesWritten contract — see GH #731.
	assert.Contains(t, resp.FilesWritten, filepath.Join(".factory", "rules", "ox.md"))

	ruleFile := filepath.Join(dir, ".factory", "rules", "ox.md")
	data, err := os.ReadFile(ruleFile)
	require.NoError(t, err, "ox.md must exist on disk after install")
	assert.Contains(t, string(data), "agentx-hash", "file must contain agentx stamp")
}

// TestHandleInstallRules_Idempotent verifies that installing the same version
// twice succeeds without error. The second call may skip writing (identical content)
// but must not fail.
// Failure prevented: repeated primes or hook runs cause errors.
func TestHandleInstallRules_Idempotent(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	resp1, err := handleInstallRules(params)
	require.NoError(t, err)
	assert.True(t, resp1.Installed)

	resp2, err := handleInstallRules(params)
	require.NoError(t, err)
	assert.True(t, resp2.Installed)
}

// --- B. Check lifecycle ---

// TestHandleCheckRules_Missing verifies that check reports missing rules when
// none have been installed.
// Failure prevented: check falsely reports rules as installed in a fresh repo.
func TestHandleCheckRules_Missing(t *testing.T) {
	dir := t.TempDir()

	resp, err := handleCheckRules(adapterprotocol.RulesParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)

	assert.False(t, resp.Installed)
	assert.Contains(t, resp.Missing, "ox.md")
}

// TestHandleCheckRules_Installed verifies that check reports installed=true
// after a successful install with the same version.
// Failure prevented: check always reports missing even after install.
func TestHandleCheckRules_Installed(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.True(t, resp.Installed)
	assert.Empty(t, resp.Missing)
	assert.Empty(t, resp.Stale)
}

// TestHandleCheckRules_FrontmatterBodyEdited_ReportsStale is the regression
// test for Bug 2 (frontmatter staleness blindness) on droid's .factory/rules/.
// Every rule we install sets Description, so agentx's buildContent prepends YAML
// frontmatter BEFORE the stamp line; agentx.IsRuleStale only inspects line 1
// and reports a hand-edited body fresh forever. handleCheckRules must scan all
// lines for the stamp and detect the drift.
// Failure prevented: a tampered/outdated .factory/rules/ox.md passes doctor
// silently, so installed Layer-2 guidance drifts from the live binary.
func TestHandleCheckRules_FrontmatterBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	clean, err := handleCheckRules(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, "ox.md", "precondition: freshly installed rule must not be stale")

	// Appending to the body changes the stamped content (the stamp hash covers
	// the body WITHOUT frontmatter) while leaving frontmatter and the stamp line
	// intact — exactly the drift agentx's first-line check cannot see.
	rulePath := filepath.Join(dir, ".factory", "rules", "ox.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox.md", "edited frontmatter'd body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed, "Installed must be false when a rule has drifted")
}

// TestHandleCheckRules_NamespacedBodyEdited_ReportsStale verifies Bug 2 also
// covers the namespaced sageox/use-team-context.md pointer rule on droid.
// Failure prevented: a drifted team-context pointer rule passes doctor while
// teaching the agent stale discovery instructions.
func TestHandleCheckRules_NamespacedBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	rulePath := filepath.Join(dir, ".factory", "rules", "sageox", "use-team-context.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\ndrift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "sageox/use-team-context.md", "edited namespaced body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed)
}

// TestHandleCheckRules_UserManagedRuleNotStale verifies a rule file with no
// agentx stamp is never reported stale — we only manage files we stamped.
// Failure prevented: a no-op --fix loop where doctor flags a user-owned file
// forever and never converges.
func TestHandleCheckRules_UserManagedRuleNotStale(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "ox.md"),
		[]byte("# my own ox rule, no stamp\n"), 0o644))

	resp, err := handleCheckRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"})
	require.NoError(t, err)

	assert.NotContains(t, resp.Stale, "ox.md", "unstamped user-managed file must never be flagged stale")
}

// --- C. Uninstall lifecycle ---

// TestHandleUninstallRules_AgentxLimitationOnTopLevelOxMd documents that
// agentx v0.1.10's Uninstall cannot remove the top-level ox.md because
// ExtractCommandHash only inspects the first line, and YAML frontmatter
// (description: ...) lives there. The adapter works around this for the
// sageox/ namespace via adapterstamp.LooksStamped, but the top-level file still
// hits the upstream bug.
//
// When agentx fixes the limitation upstream, this test will FAIL —
// prompting us to remove it and simplify the workaround.
func TestHandleUninstallRules_AgentxLimitationOnTopLevelOxMd(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	resp, err := handleUninstallRules(params)
	require.NoError(t, err)

	for _, name := range resp.FilesRemoved {
		if name == "ox.md" {
			t.Fatalf("ox.md was removed — agentx may have fixed the frontmatter limitation; remove this test and update the workaround in rules.go")
		}
	}

	ruleFile := filepath.Join(dir, ".factory", "rules", "ox.md")
	_, err = os.Stat(ruleFile)
	assert.NoError(t, err, "ox.md survives uninstall due to agentx frontmatter limitation")
}

// --- D. Diagnose integration ---

// TestDiagnose_RulesMissing verifies that diagnose detects missing rules and
// emits an issue with the correct slug.
// Failure prevented: doctor misses broken rules state and reports all-clear.
func TestDiagnose_RulesMissing(t *testing.T) {
	dir := t.TempDir()

	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{
		RepoRoot: dir,
	})
	require.NoError(t, err)

	var slugs []string
	for _, issue := range result.Issues {
		slugs = append(slugs, issue.Slug)
	}
	assert.Contains(t, slugs, "droid:rules-missing")
}
