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
	assert.Contains(t, resp.FilesWritten, "ox.md")

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

// --- C. Uninstall lifecycle ---

// TestHandleUninstallRules_AgentxLimitationOnTopLevelOxMd documents that
// agentx v0.1.10's Uninstall cannot remove the top-level ox.md because
// ExtractCommandHash only inspects the first line, and YAML frontmatter
// (description: ...) lives there. The adapter works around this for the
// sageox/ namespace via looksStamped(), but the top-level file still
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
