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
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "ox-cli.md"))

	ruleFile := filepath.Join(dir, ".claude", "rules", "ox-cli.md")
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
	require.NotContains(t, clean.Stale, "ox-cli.md", "precondition: freshly installed rule must not be stale")

	// hand-edit the body of the frontmatter'd top-level rule. Appending to the
	// end changes the stamped body content (the stamp hash covers the body
	// WITHOUT frontmatter) while leaving the frontmatter and stamp line intact —
	// exactly the drift agentx's first-line check cannot see.
	rulePath := filepath.Join(dir, ".claude", "rules", "ox-cli.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox-cli.md", "edited frontmatter'd body must be reported Stale (Bug 2)")
	assert.False(t, resp.Installed, "Installed must be false when a rule has drifted")
}

// TestHandleCheckRules_PointerRuleBodyEdited_ReportsStale verifies Bug 2 also
// covers the ox-cli-use-team-context.md pointer rule, which likewise carries
// frontmatter (and therefore hides its stamp from agentx's first-line check).
// Failure prevented: a drifted team-context pointer rule passes doctor while
// teaching the agent stale discovery instructions.
func TestHandleCheckRules_PointerRuleBodyEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallRules(params)
	require.NoError(t, err)

	rulePath := filepath.Join(dir, ".claude", "rules", "ox-cli-use-team-context.md")
	orig, err := os.ReadFile(rulePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulePath, append(orig, []byte("\n\ndrift\n")...), 0o644))

	resp, err := handleCheckRules(params)
	require.NoError(t, err)

	assert.Contains(t, resp.Stale, "ox-cli-use-team-context.md", "edited pointer-rule body must be reported Stale (Bug 2)")
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
		assert.NoFileExists(t, filepath.Join(dir, ".claude", "rules", name))
	}
}

// --- C2. sageox/ namespace ---

// TestHandleInstallRules_InstallsFlatReservedPrefixRules pins the 0.15.0
// inversion of the old namespacing rule.
//
// Rules used to live under .claude/rules/sageox/ so they would not clutter the
// rules root with ox-* siblings. They now install FLAT under the reserved
// ox-cli-* prefix instead, because a nested directory cannot be covered by the
// single .gitignore glob that keeps ox's rules out of the customer's pull
// requests — and staying out of those diffs is the whole point of the rework.
//
// Failure prevented: a rule ships into a subdirectory the ignore rule cannot
// reach, and every ox release puts it back into someone's PR.
func TestHandleInstallRules_InstallsFlatReservedPrefixRules(t *testing.T) {
	dir := t.TempDir()

	resp, err := handleInstallRules(adapterprotocol.RulesParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)
	assert.True(t, resp.Installed)

	// the pointer rule lives flat, under the reserved prefix
	pointer := filepath.Join(dir, ".claude", "rules", "ox-cli-use-team-context.md")
	data, err := os.ReadFile(pointer)
	require.NoError(t, err, "ox-cli-use-team-context.md must exist at the rules root after install")
	assert.Contains(t, string(data), "agentx-hash", "rule must be stamped")

	// and no legacy subdirectory is created any more
	_, nsErr := os.Stat(filepath.Join(dir, ".claude", "rules", "sageox"))
	assert.True(t, os.IsNotExist(nsErr), "install must not create the legacy sageox/ namespace")
	assert.Contains(t, string(data), "team-context", "pointer rule must reference team context")
	assert.Contains(t, string(data), "ox agent prime", "pointer rule must teach the agent how to discover team rules")

	top := filepath.Join(dir, ".claude", "rules", "ox-cli.md")
	_, err = os.Stat(top)
	require.NoError(t, err, ".claude/rules/ox-cli.md should be installed at the rules root")

	// FilesWritten must reference both, REPO-relative — not relative to the
	// rules dir. agentx returns rules-dir-relative names; reporting those
	// verbatim made ox resolve `ox.md` to <root>/ox.md, which does not
	// exist, and one bad pathspec failed the whole `git add` so nothing at
	// all was staged (GH #731).
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "ox-cli.md"),
		"got %v", resp.FilesWritten)
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "rules", "ox-cli-use-team-context.md"),
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
// version has ox.md but not ox-cli-use-team-context.md.
// Failure prevented: ox doctor passes silently when the new pointer rule
// hasn't been installed yet, depriving the agent of team-context guidance.
func TestHandleCheckRules_NamespacedRulesMissing(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	// simulate an older install where only ox.md was written
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "ox-cli.md"),
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
		if name == "ox-cli-use-team-context.md" {
			sawNS = true
		}
	}
	assert.True(t, sawNS, "check should flag ox-cli-use-team-context.md when not installed yet, got missing=%v stale=%v",
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

	// Install the current surface FIRST, then seed the pre-0.15.0 layout by hand.
	// Order matters: install now retires the legacy tree itself, so seeding before
	// it would leave nothing for uninstall to find. This models the repository
	// uninstall actually has to serve — one an older ox left a nested tree in.
	_, err := handleInstallRules(params)
	require.NoError(t, err)
	pointer := seedLegacyNamespaceRule(t, dir)

	resp, err := handleUninstallRules(params)
	require.NoError(t, err)
	assert.True(t, resp.Uninstalled, "expected at least one file removed")

	// legacy pointer rule gone
	_, err = os.Stat(pointer)
	assert.True(t, os.IsNotExist(err), "legacy namespaced rule should be removed")

	// empty sageox/ dir cleaned up
	nsDir := filepath.Join(dir, ".claude", "rules", "sageox")
	_, err = os.Stat(nsDir)
	assert.True(t, os.IsNotExist(err), "empty sageox/ dir should be removed by uninstall")

	assert.NotEmpty(t, resp.FilesRemoved, "uninstall must report what it removed")
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
	seedLegacyNamespaceRule(t, dir)

	// drop a user-authored file alongside ours in the legacy namespace
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

// seedLegacyNamespaceRule writes a stamped rule into the pre-0.15.0
// .claude/rules/sageox/ layout — the shape an older ox left behind. Install no
// longer creates it, so a test that needs it must build it.
func seedLegacyNamespaceRule(t *testing.T, repoRoot string) string {
	t.Helper()
	nsDir := filepath.Join(repoRoot, ".claude", "rules", "sageox")
	require.NoError(t, os.MkdirAll(nsDir, 0o755))
	path := filepath.Join(nsDir, "use-team-context.md")
	require.NoError(t, os.WriteFile(path, stampedLegacyRule("# legacy pointer rule\n", teamContextRuleDescription), 0o644))
	return path
}

func stampedLegacyRule(body, description string) []byte {
	frontmatter := "---\ndescription: " + description + "\n---\n"
	return append([]byte(frontmatter), agentx.StampedContent([]byte(body), "0.14.0", agentx.DefaultStampPrefix)...)
}

// TestHandleInstallRules_RetiresLegacyRuleSurface is the migration this rename
// depends on. Retiring only on uninstall would mean retiring never happens: an
// existing project installs once and reconciles forever after, so it would carry
// BOTH the legacy .claude/rules/ox.md and the nested sageox/ tree beside the new
// flat files — two overlapping rule sets, and a directory no single ignore line
// can cover.
//
// Failure prevented: an upgraded repository keeps serving pre-rename rules
// forever, and its ox rules stay visible in every pull request.
func TestHandleInstallRules_RetiresLegacyRuleSurface(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))

	legacyTop := filepath.Join(rulesDir, "ox.md")
	require.NoError(t, os.WriteFile(legacyTop, stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), 0o644))
	legacyNS := seedLegacyNamespaceRule(t, dir)

	// A rule the USER wrote under a legacy-looking name must survive: they never
	// agreed to ox owning that path.
	userAuthored := filepath.Join(rulesDir, "ox-notes.md")
	require.NoError(t, os.WriteFile(userAuthored, []byte("# my own notes\n"), 0o644))

	_, err := handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)

	_, err = os.Stat(legacyTop)
	assert.True(t, os.IsNotExist(err), "legacy .claude/rules/ox.md must be retired on install, not left beside ox-cli.md")
	_, err = os.Stat(legacyNS)
	assert.True(t, os.IsNotExist(err), "legacy sageox/ rule must be retired on install")
	_, err = os.Stat(filepath.Join(rulesDir, "sageox"))
	assert.True(t, os.IsNotExist(err), "empty legacy sageox/ directory must be cleaned up")

	_, err = os.Stat(userAuthored)
	assert.NoError(t, err, "user-authored rule must never be removed by the legacy sweep")

	// and the new flat surface is in place
	for _, name := range []string{"ox-cli.md", "ox-cli-use-team-context.md"} {
		_, err := os.Stat(filepath.Join(rulesDir, name))
		assert.NoError(t, err, "%s must be installed", name)
	}
}

func TestHandleInstallRules_PreservesEditedLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))

	legacyTop := filepath.Join(rulesDir, "ox.md")
	topData := append(stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), []byte("user edit\n")...)
	require.NoError(t, os.WriteFile(legacyTop, topData, 0o644))
	legacyNS := seedLegacyNamespaceRule(t, dir)
	nsData, err := os.ReadFile(legacyNS)
	require.NoError(t, err)
	nsData = append(nsData, []byte("user edit\n")...)
	require.NoError(t, os.WriteFile(legacyNS, nsData, 0o644))

	_, err = handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.NoError(t, err)

	assert.FileExists(t, legacyTop)
	assert.FileExists(t, legacyNS)
}

func TestHandleInstallRules_PreservesFrontmatterEditedLegacyRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".claude", "rules")
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
	rulesDir := filepath.Join(dir, ".claude", "rules")
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
	rulesDir := filepath.Join(dir, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	legacyTop := filepath.Join(rulesDir, "ox.md")
	require.NoError(t, os.WriteFile(legacyTop, stampedLegacyRule("# legacy ox rule\n", oxRuleDescription), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(rulesDir, "ox-cli.md"), 0o755))

	_, err := handleInstallRules(adapterprotocol.RulesParams{RepoRoot: dir, Version: "0.15.0"})
	require.Error(t, err)
	assert.FileExists(t, legacyTop, "legacy guidance must survive until replacement install succeeds")
}
