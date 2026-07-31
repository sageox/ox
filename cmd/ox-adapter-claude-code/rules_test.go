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
	// repo-relative per the adapterprotocol FilesWritten contract — see GH #731.
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "ox.md"))

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

// TestHandleCheckRules_FrontmatterBodyEdited_ReportsStale is the regression
// test for Bug 2 (frontmatter staleness blindness). Every rule we install sets
// Description, so agentx's buildContent prepends YAML frontmatter BEFORE the
// stamp line. agentx.IsRuleStale only inspects line 1 (the `---` opener), so a
// hand-edited body is reported fresh forever. handleCheckRules must scan all
// lines for the stamp and detect the drift.
// Failure prevented: a tampered/outdated .claude/rules/ox.md passes doctor
// silently, so installed Layer-2 guidance drifts from the live binary with no
// detection and no --fix.
func TestHandleCheckRules_FrontmatterBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	// sanity: clean install is not stale
	clean, err := handleCheckRules(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, "ox.md", "precondition: freshly installed rule must not be stale")

	// hand-edit the body of the frontmatter'd top-level rule. Appending to the
	// end changes the stamped body content (the stamp hash covers the body
	// WITHOUT frontmatter) while leaving the frontmatter and stamp line intact —
	// exactly the drift agentx's first-line check cannot see.
	rulePath := filepath.Join(dir, ".claude", "rules", "ox.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox.md", "edited frontmatter'd body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed, "Installed must be false when a rule has drifted")
}

// TestHandleCheckRules_NamespacedBodyEdited_ReportsStale verifies Bug 2 also
// covers the namespaced sageox/use-team-context.md pointer rule, which likewise
// carries frontmatter.
// Failure prevented: a drifted team-context pointer rule passes doctor while
// teaching the agent stale discovery instructions.
func TestHandleCheckRules_NamespacedBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	rulePath := filepath.Join(dir, ".claude", "rules", "sageox", "use-team-context.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\ndrift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "sageox/use-team-context.md", "edited namespaced body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed)
}

// TestHandleCheckRules_UserManagedRuleNotStale verifies a rule file with no
// agentx stamp (e.g. a user replaced our content entirely) is never reported
// stale — we only manage files we stamped.
// Failure prevented: a no-op --fix loop where doctor flags a user-owned file
// forever and never converges.
func TestHandleCheckRules_UserManagedRuleNotStale(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	// unstamped, user-authored content at the canonical path
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
	// thanks to adapterstamp.LooksStamped; see TestHandleUninstallRules_RemovesNamespace.)
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

	// FilesWritten must reference both, REPO-relative — not relative to the
	// rules dir. agentx returns rules-dir-relative names; reporting those
	// verbatim made ox resolve `ox.md` to <root>/ox.md, which does not
	// exist, and one bad pathspec failed the whole `git add` so nothing at
	// all was staged (GH #731).
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "ox.md"),
		"got %v", resp.FilesWritten)
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "sageox", "use-team-context.md"),
		"got %v", resp.FilesWritten)

	// and every entry must actually resolve inside the repo, or ox drops it.
	for _, rel := range resp.FilesWritten {
		assert.False(t, filepath.IsAbs(rel), "%s should be repo-relative", rel)
		_, statErr := os.Stat(filepath.Join(dir, rel))
		assert.NoError(t, statErr, "%s must resolve to a real file under RepoRoot", rel)
	}
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
