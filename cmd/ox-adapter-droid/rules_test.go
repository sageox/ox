package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
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
	assert.Contains(t, resp.FilesWritten, filepath.Join(".factory", "rules", "ox-cli.md"))

	ruleFile := filepath.Join(dir, ".factory", "rules", "ox-cli.md")
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
	assert.Contains(t, resp.Missing, "ox-cli.md")
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
	require.NotContains(t, clean.Stale, "ox-cli.md", "precondition: freshly installed rule must not be stale")

	// Appending to the body changes the stamped content (the stamp hash covers
	// the body WITHOUT frontmatter) while leaving frontmatter and the stamp line
	// intact — exactly the drift agentx's first-line check cannot see.
	rulePath := filepath.Join(dir, ".factory", "rules", "ox-cli.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox-cli.md", "edited frontmatter'd body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed, "Installed must be false when a rule has drifted")
}

// TestHandleCheckRules_NamespacedBodyEdited_ReportsStale verifies Bug 2 also
// covers the ox-cli-use-team-context.md pointer rule on droid.
// Failure prevented: a drifted team-context pointer rule passes doctor while
// teaching the agent stale discovery instructions.
func TestHandleCheckRules_PointerRuleBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	rulePath := filepath.Join(dir, ".factory", "rules", "ox-cli-use-team-context.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\ndrift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox-cli-use-team-context.md", "edited pointer-rule body must be reported Stale (Bug 2)")
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
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "ox-cli.md"),
		[]byte("# my own ox rule, no stamp\n"), 0o644))

	resp, err := handleCheckRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"})
	require.NoError(t, err)

	assert.NotContains(t, resp.Stale, "ox-cli.md", "unstamped user-managed file must never be flagged stale")
}

// --- C. Uninstall lifecycle ---

// TestHandleUninstallRules_RemovesCurrentFlatRules verifies uninstall handles
// the YAML-frontmatter rules that agentx's first-line-only stamp reader misses.
func TestHandleUninstallRules_RemovesCurrentFlatRules(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	resp, err := handleUninstallRules(params)
	require.NoError(t, err)

	for _, name := range []string{"ox-cli.md", "ox-cli-use-team-context.md"} {
		assert.Contains(t, resp.FilesRemoved, name)
		assert.NoFileExists(t, filepath.Join(dir, ".factory", "rules", name))
	}
}

func stampedLegacyRule(body, description string) []byte {
	frontmatter := "---\ndescription: " + description + "\n---\n"
	return append([]byte(frontmatter), agentx.StampedContent([]byte(body), "0.14.0", agentx.DefaultStampPrefix)...)
}

func seedLegacyNamespaceRule(t *testing.T, repoRoot string) string {
	t.Helper()
	nsDir := filepath.Join(repoRoot, ".factory", "rules", "sageox")
	require.NoError(t, os.MkdirAll(nsDir, 0o755))
	path := filepath.Join(nsDir, "use-team-context.md")
	require.NoError(t, os.WriteFile(path, stampedLegacyRule("# legacy pointer rule\n", teamContextRuleDescription), 0o644))
	return path
}

func TestHandleInstallRules_RetiresVerifiedLegacyRuleSurface(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	require.NoError(t, os.WriteFile(legacyTop, stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), 0o644))
	legacyNS := seedLegacyNamespaceRule(t, dir)

	_, err := handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)
	assert.NoFileExists(t, legacyTop)
	assert.NoFileExists(t, legacyNS)
}

func TestHandleInstallRules_PreservesEditedLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	require.NoError(t, os.WriteFile(legacyTop,
		append(stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), []byte("user edit\n")...), 0o644))
	legacyNS := seedLegacyNamespaceRule(t, dir)
	nsData, err := os.ReadFile(legacyNS)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyNS, append(nsData, []byte("user edit\n")...), 0o644))

	_, err = handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)
	assert.FileExists(t, legacyTop)
	assert.FileExists(t, legacyNS)
}

func TestHandleInstallRules_PreservesFrontmatterEditedLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	topData := strings.Replace(
		string(stampedLegacyRule("# legacy ox rule\n", oxRuleDescription)),
		oxRuleDescription,
		"User-owned description",
		1,
	)
	require.NoError(t, os.WriteFile(legacyTop, []byte(topData), 0o644))

	legacyNS := seedLegacyNamespaceRule(t, dir)
	nsData, err := os.ReadFile(legacyNS)
	require.NoError(t, err)
	nsData = []byte(strings.Replace(string(nsData), teamContextRuleDescription, "User-owned description", 1))
	require.NoError(t, os.WriteFile(legacyNS, nsData, 0o644))

	_, err = handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)
	assert.FileExists(t, legacyTop, "frontmatter edits must make a legacy rule user-owned")
	assert.FileExists(t, legacyNS, "frontmatter edits must make a legacy rule user-owned")
}

func TestHandleInstallRules_RetiresCRLFLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	topData := strings.ReplaceAll(
		string(stampedLegacyRule("# legacy ox rule\n", oxRuleDescription)),
		"\n",
		"\r\n",
	)
	require.NoError(t, os.WriteFile(legacyTop, []byte(topData), 0o644))

	legacyNS := seedLegacyNamespaceRule(t, dir)
	nsData, err := os.ReadFile(legacyNS)
	require.NoError(t, err)
	nsData = []byte(strings.ReplaceAll(string(nsData), "\n", "\r\n"))
	require.NoError(t, os.WriteFile(legacyNS, nsData, 0o644))

	_, err = handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)
	assert.NoFileExists(t, legacyTop, "CRLF checkout must not prevent clean legacy retirement")
	assert.NoFileExists(t, legacyNS, "CRLF checkout must not prevent clean legacy retirement")
}

func TestHandleInstallRules_FailurePreservesLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".factory", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	require.NoError(t, os.WriteFile(legacyTop, stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(rulesDir, "ox-cli.md"), 0o755))

	_, err := handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.Error(t, err)
	assert.FileExists(t, legacyTop, "legacy guidance must survive until replacement install succeeds")
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

// TestPointerRule_TellsTheTruthAboutTeamContent is the droid twin of the
// claude-code assertion: both adapters ship the same pointer-rule text from
// separate Go literals, so a correction to one can silently miss the other.
// Failure prevented: droid users keep reading that team commands are slash
// commands after the claude-code copy was fixed.
func TestPointerRule_TellsTheTruthAboutTeamContent(t *testing.T) {
	dir := t.TempDir()

	_, err := handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".factory", "rules", "ox-cli-use-team-context.md"))
	require.NoError(t, err)
	body := string(data)

	assert.Contains(t, body, "      agents/")
	assert.Contains(t, body, "        profiles/")
	assert.Contains(t, body, "        commands/")
	assert.NotContains(t, body, "team slash commands",
		"pointer rule must not advertise team commands as invocable slash commands")
	// droid's copy wraps the sentence, so compare on whitespace-normalized text
	flat := strings.Join(strings.Fields(body), " ")
	assert.Contains(t, flat, "absolute path shown in the prime output",
		"pointer rule tells agents to read the absolute path; prime must keep emitting one")
}
