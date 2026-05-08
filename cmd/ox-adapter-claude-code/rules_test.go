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
// ox.md to .claude/rules/ with a valid agentx stamp.
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

	ruleFile := filepath.Join(dir, ".claude", "rules", "ox.md")
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
// prompting us to remove it and rely solely on the intent-based tests
// (TestHandleUninstallRules_RemovesNamespace) that assert the desired
// end state regardless of how it was achieved.
//
// Failure prevented: agentx fix lands silently and we miss the chance
// to simplify our adapter-side workaround.
func TestHandleUninstallRules_AgentxLimitationOnTopLevelOxMd(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	resp, err := handleUninstallRules(params)
	require.NoError(t, err)

	// the top-level ox.md is NOT in FilesRemoved — agentx still can't see
	// past frontmatter on the first line. (The namespaced file IS removed
	// thanks to looksStamped(); see TestHandleUninstallRules_RemovesNamespace.)
	for _, name := range resp.FilesRemoved {
		if name == "ox.md" {
			t.Fatalf("ox.md was removed — agentx may have fixed the frontmatter limitation; remove this test and update the workaround in rules.go")
		}
	}

	// the file survives on disk
	ruleFile := filepath.Join(dir, ".claude", "rules", "ox.md")
	_, err = os.Stat(ruleFile)
	assert.NoError(t, err, "ox.md survives uninstall due to agentx frontmatter limitation")
}

// --- C2. sageox/ namespace ---

// TestHandleInstallRules_InstallsNamespacedRules verifies that ox installs
// the use-team-context pointer rule under .claude/rules/sageox/, not at
// the rules root. The namespace prevents future SageOx rules from
// polluting the rules directory with ox-* prefixed siblings.
// Failure prevented: new rules ship at top level, cluttering user repos
// and re-creating the polluted-namespace problem.
func TestHandleInstallRules_InstallsNamespacedRules(t *testing.T) {
	dir := t.TempDir()

	resp, err := handleInstallRules(adapterprotocol.RulesParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)
	assert.True(t, resp.Installed)

	// the use-team-context pointer rule lives under sageox/
	pointer := filepath.Join(dir, ".claude", "rules", "sageox", "use-team-context.md")
	data, err := os.ReadFile(pointer)
	require.NoError(t, err, "use-team-context.md must exist under sageox/ after install")
	assert.Contains(t, string(data), "agentx-hash", "namespaced rule must be stamped")
	assert.Contains(t, string(data), "team-context", "pointer rule must reference team context")
	assert.Contains(t, string(data), "ox agent prime", "pointer rule must teach the agent how to discover team rules")

	// the canonical ox.md still lives at the top level (preserved muscle memory)
	top := filepath.Join(dir, ".claude", "rules", "ox.md")
	_, err = os.Stat(top)
	require.NoError(t, err, ".claude/rules/ox.md should still be installed at top level")

	// FilesWritten should reference both
	var sawTop, sawNS bool
	for _, name := range resp.FilesWritten {
		if name == "ox.md" {
			sawTop = true
		}
		if name == "sageox/use-team-context.md" {
			sawNS = true
		}
	}
	assert.True(t, sawTop, "FilesWritten should include ox.md, got %v", resp.FilesWritten)
	assert.True(t, sawNS, "FilesWritten should include sageox/use-team-context.md, got %v", resp.FilesWritten)
}

// TestHandleCheckRules_NamespacedRulesMissing verifies that check reports
// the namespaced pointer rule as missing when only the legacy top-level
// ox.md exists. This is the upgrade path: a teammate on an older ox
// version has ox.md but not sageox/use-team-context.md.
// Failure prevented: ox doctor passes silently when the new pointer rule
// hasn't been installed yet, depriving the agent of team-context guidance.
func TestHandleCheckRules_NamespacedRulesMissing(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	// simulate an older install where only ox.md was written
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "ox.md"),
		[]byte("# old version, no stamp\n"), 0o644))

	resp, err := handleCheckRules(adapterprotocol.RulesParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)

	// missing or stale must include the namespaced rule
	missingOrStale := append([]string{}, resp.Missing...)
	missingOrStale = append(missingOrStale, resp.Stale...)
	var sawNS bool
	for _, name := range missingOrStale {
		if name == "sageox/use-team-context.md" {
			sawNS = true
		}
	}
	assert.True(t, sawNS, "check should flag sageox/use-team-context.md when not installed yet, got missing=%v stale=%v",
		resp.Missing, resp.Stale)
	assert.False(t, resp.Installed, "Installed should be false when namespaced pointer rule is missing")
}

// TestHandleUninstallRules_RemovesNamespace verifies uninstall walks the
// sageox/ namespace and removes stamped files, then cleans up the empty
// directory. agentx.Uninstall doesn't recurse into subdirs, so this is
// adapter-side logic that must work correctly.
// Failure prevented: ox uninstall leaves stamped files (and the empty
// sageox/ directory) behind, which is the kind of paper-cut that makes
// "uninstall and reinstall to fix" actually fail.
func TestHandleUninstallRules_RemovesNamespace(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	pointer := filepath.Join(dir, ".claude", "rules", "sageox", "use-team-context.md")
	_, err = os.Stat(pointer)
	require.NoError(t, err, "precondition: pointer rule must exist before uninstall")

	resp, err := handleUninstallRules(params)
	require.NoError(t, err)
	assert.True(t, resp.Uninstalled, "expected at least one file removed")

	// pointer rule gone
	_, err = os.Stat(pointer)
	assert.True(t, os.IsNotExist(err), "pointer rule should be removed by uninstall")

	// empty sageox/ dir cleaned up
	nsDir := filepath.Join(dir, ".claude", "rules", "sageox")
	_, err = os.Stat(nsDir)
	assert.True(t, os.IsNotExist(err), "empty sageox/ dir should be removed by uninstall")

	// FilesRemoved should reference the namespaced path
	var sawNS bool
	for _, name := range resp.FilesRemoved {
		if name == "sageox/use-team-context.md" {
			sawNS = true
		}
	}
	assert.True(t, sawNS, "FilesRemoved should include sageox/use-team-context.md, got %v", resp.FilesRemoved)
}

// TestHandleUninstallRules_PreservesUnstampedNamespaceFiles verifies that
// a file under sageox/ that we did NOT install (no ox stamp) is left
// alone. Users may drop their own files into the namespace; uninstall
// must not delete arbitrary content.
// Failure prevented: uninstall destroys user-authored rules under
// sageox/ that share a directory with our installs.
func TestHandleUninstallRules_PreservesUnstampedNamespaceFiles(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}
	_, err := handleInstallRules(params)
	require.NoError(t, err)

	// drop a user-authored file alongside ours
	userFile := filepath.Join(dir, ".claude", "rules", "sageox", "user-rule.md")
	require.NoError(t, os.WriteFile(userFile, []byte("# user content, no stamp\n"), 0o644))

	_, err = handleUninstallRules(params)
	require.NoError(t, err)

	_, err = os.Stat(userFile)
	assert.NoError(t, err, "user-authored unstamped file under sageox/ must NOT be removed")
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
	assert.Contains(t, slugs, "claude-code:rules-missing")
}
