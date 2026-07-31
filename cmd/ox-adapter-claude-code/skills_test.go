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

// pickSkill returns the first embedded skill name, failing the test if none
// ship. Tests that need a concrete skill on disk use this so they don't hardcode
// an id that may be renamed.
func pickSkill(t *testing.T) string {
	t.Helper()
	skills, err := oxSkillFiles("0.8.0")
	require.NoError(t, err)
	require.NotEmpty(t, skills, "at least one embedded skill must ship for these tests to mean anything")
	return skills[0].Name
}

// --- A. Install lifecycle / stamp placement ---

// TestHandleInstallSkills_StampPlacement verifies the load-bearing on-disk
// contract: Claude must be able to parse a skill's YAML frontmatter, so the
// frontmatter MUST be the first bytes of SKILL.md (line 1 is the `---` opener)
// and the ox-hash drift stamp MUST sit AFTER the closing `---`, never on line 1.
// Failure prevented: the stamp leaks onto line 1 (breaking frontmatter parsing)
// or the frontmatter is dropped, so Claude can't read name/description.
func TestHandleInstallSkills_StampPlacement(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)

	resp, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)
	assert.True(t, resp.Installed)
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "skills", name, skillFileName),
		"FilesWritten must be repo-relative per the adapterprotocol contract")

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name, skillFileName))
	require.NoError(t, err, "SKILL.md must exist on disk after install")

	lines := strings.Split(string(data), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, "---", strings.TrimRight(lines[0], "\r"),
		"line 1 must be the frontmatter opener so Claude can parse name/description")

	stampMarker := agentx.StampComment(oxSkillStampPrefix)
	assert.Contains(t, string(data), stampMarker, "SKILL.md must carry an ox-hash stamp")
	assert.False(t, strings.HasPrefix(lines[0], stampMarker),
		"the stamp must NOT be on line 1 — it belongs after the closing frontmatter fence")

	// the stamp must appear strictly after the closing `---` fence.
	stampIdx := strings.Index(string(data), stampMarker)
	require.GreaterOrEqual(t, stampIdx, 0)
	closingFence := strings.Index(string(data), "\n---\n")
	require.GreaterOrEqual(t, closingFence, 0, "frontmatter must have a closing fence")
	assert.Greater(t, stampIdx, closingFence,
		"stamp must sit after the closing frontmatter fence, not inside the frontmatter")
}

// TestHandleInstallSkills_Idempotent verifies installing the same version twice
// succeeds without error (repeated primes / doctor runs must not break).
// Failure prevented: a second install errors or corrupts the first.
func TestHandleInstallSkills_Idempotent(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	r1, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, r1.Installed)

	r2, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, r2.Installed)
}

// TestHandleInstallSkills_NoOpReportsNothingWritten is the honesty contract that
// keeps `ox init` from claiming "Installed N skills" on an already-current repo.
// A first install writes every embedded skill; a second install on the same
// version writes nothing, so FilesWritten MUST be empty. init.go gates its
// "Installed N skills" vs "skills already up to date" message on
// len(FilesWritten), so a non-empty slice here would make init lie.
// Failure prevented: a no-op re-install reports files written, and `ox init`
// on a current repo falsely claims it installed skills.
func TestHandleInstallSkills_NoOpReportsNothingWritten(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	first, err := handleInstallSkills(params)
	require.NoError(t, err)
	require.True(t, first.Installed)
	require.NotEmpty(t, first.FilesWritten,
		"precondition: the first install must write every embedded skill")

	second, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, second.Installed, "a no-op re-install still succeeds")
	assert.Empty(t, second.FilesWritten,
		"a re-install of an already-current skill set must write nothing — "+
			"FilesWritten drives init's honest 'already up to date' message")
}

// --- B. Check lifecycle ---

// TestHandleCheckSkills_FreshInstall verifies a freshly installed skill set
// reports Installed and is not stale.
// Failure prevented: doctor flags a clean install as broken (a no-op --fix loop)
// or reports a fresh repo as already installed.
func TestHandleCheckSkills_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	// precondition: nothing installed yet -> not installed.
	pre, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.False(t, pre.Installed, "a fresh repo must not report skills as installed")
	assert.NotEmpty(t, pre.Missing, "all embedded skills should be reported missing")

	_, err = handleInstallSkills(params)
	require.NoError(t, err)

	post, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.True(t, post.Installed, "a clean install must report Installed")
	assert.Empty(t, post.Missing)
	assert.Empty(t, post.Stale)
}

// TestHandleCheckSkills_BodyEditedBelowStamp_ReportsStale is the core drift test:
// editing the body BELOW the stamp (leaving frontmatter and stamp line intact)
// must be detected as stale, because the stamp hash covers only the body. A
// first-line-only check would miss this entirely. --fix (reinstall) must restore.
// Failure prevented: a tampered SKILL.md drifts from the live binary forever with
// no detection, teaching the agent stale guidance.
func TestHandleCheckSkills_BodyEditedBelowStamp_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	clean, err := handleCheckSkills(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, name, "precondition: fresh install must not be stale")

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	// append below the stamp — the stamp line and frontmatter stay byte-identical.
	require.NoError(t, os.WriteFile(skillPath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	stale, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, stale.Stale, name, "body edited below the stamp must be reported Stale")
	assert.False(t, stale.Installed, "Installed must be false when a skill has drifted")

	// reinstall (the --fix path) restores the file and clears staleness.
	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	fixed, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, fixed.Stale, name, "reinstall must restore a drifted skill")
	assert.True(t, fixed.Installed, "after --fix the skill set must be Installed again")

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.NotContains(t, string(restored), "hand-edited drift", "reinstall must overwrite the tampered body")
}

// TestHandleCheckSkills_FrontmatterEdited_ReportsStale guards the gap CodeRabbit
// flagged: the drift stamp's hash covers ONLY the body below it, so editing the
// YAML frontmatter (name/description) of a stamped skill leaves the body hash and
// stamp line byte-identical — a body-only staleness check sees nothing wrong and
// the agent silently reads tampered metadata forever. The check must catch a
// frontmatter-only edit and --fix (reinstall) must restore the original.
// Failure prevented: a hand-edited skill description drifts from the live binary
// undetected because the stamp doesn't cover the frontmatter.
func TestHandleCheckSkills_FrontmatterEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	clean, err := handleCheckSkills(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, name, "precondition: fresh install must not be stale")

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)

	// edit ONLY the frontmatter: inject a description line into the YAML block
	// above the closing fence, leaving the stamp line and body untouched. The
	// frontmatter ends at the first "\n---\n" (closing fence) of the file.
	fence := "\n---\n"
	fenceIdx := strings.Index(string(orig), fence)
	require.GreaterOrEqual(t, fenceIdx, 0, "installed skill must have a closing frontmatter fence")
	tampered := string(orig[:fenceIdx]) + "\ndescription: hand-edited frontmatter drift" + string(orig[fenceIdx:])
	require.NotEqual(t, string(orig), tampered, "the edit must actually change the frontmatter")
	require.NoError(t, os.WriteFile(skillPath, []byte(tampered), 0o644))

	stale, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, stale.Stale, name, "a frontmatter-only edit must be reported Stale")
	assert.False(t, stale.Installed, "Installed must be false when a skill's frontmatter drifted")

	// reinstall (the --fix path) must rewrite the file and clear staleness.
	resp, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "skills", name, skillFileName),
		"--fix must rewrite a skill whose frontmatter was edited")

	fixed, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, fixed.Stale, name, "reinstall must restore the drifted frontmatter")
	assert.True(t, fixed.Installed, "after --fix the skill set must be Installed again")

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.NotContains(t, string(restored), "hand-edited frontmatter drift",
		"reinstall must overwrite the tampered frontmatter")
}

// TestHandleCheckSkills_UnstampedFrontmatterDiff_NotOverwritten verifies the
// frontmatter check is gated on the file being ox-stamped: a user-authored
// (unstamped) SKILL.md whose frontmatter differs from the embedded skill must
// NEVER be flagged stale or overwritten. Without the stamp gate, the frontmatter
// diff would clobber a user's own skill.
// Failure prevented: install destroys a user's unstamped skill because its
// frontmatter happens to differ from the shipped one.
func TestHandleCheckSkills_UnstampedFrontmatterDiff_NotOverwritten(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	// deliberately different frontmatter (and no ox stamp) from the shipped skill.
	userContent := "---\nname: " + name + "\ndescription: totally different, user-owned, no stamp\n---\nuser body\n"
	skillPath := filepath.Join(skillDir, skillFileName)
	require.NoError(t, os.WriteFile(skillPath, []byte(userContent), 0o644))

	resp, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, resp.Stale, name,
		"an unstamped user skill must not be flagged stale even when its frontmatter differs")

	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	after, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, userContent, string(after),
		"install must not overwrite a user-authored skill on a frontmatter diff alone")
}

// TestHandleCheckSkills_UnstampedSkillNotStale verifies a user-authored SKILL.md
// (no ox stamp) is never flagged stale — ox only manages files it stamped.
// Failure prevented: doctor flags a user-owned skill forever and --fix never
// converges, or worse overwrites the user's content.
func TestHandleCheckSkills_UnstampedSkillNotStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	userContent := "---\nname: " + name + "\ndescription: my own skill, no stamp\n---\nuser body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, skillFileName), []byte(userContent), 0o644))

	resp, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, resp.Stale, name, "unstamped user-authored skill must never be flagged stale")

	// install must not clobber the user's unstamped file.
	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(skillDir, skillFileName))
	require.NoError(t, err)
	assert.Equal(t, userContent, string(after), "install must not overwrite a user-authored skill")
}

// --- C. Uninstall lifecycle ---

// TestHandleUninstallSkills_RemovesStampedDirs verifies uninstall removes the
// stamped skill directories (the whole <name>/ tree, not just SKILL.md).
// Failure prevented: uninstall leaves stamped skill dirs behind, so
// "uninstall and reinstall to fix" silently fails.
func TestHandleUninstallSkills_RemovesStampedDirs(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	_, err = os.Stat(skillDir)
	require.NoError(t, err, "precondition: skill dir must exist before uninstall")

	resp, err := handleUninstallSkills(params)
	require.NoError(t, err)
	assert.True(t, resp.Uninstalled, "expected at least one stamped skill removed")
	assert.Contains(t, resp.FilesRemoved, filepath.Join(name, skillFileName),
		"FilesRemoved must reference the removed SKILL.md")

	_, err = os.Stat(skillDir)
	assert.True(t, os.IsNotExist(err), "stamped skill dir must be removed by uninstall")
}

// TestHandleUninstallSkills_PreservesUnstampedSkill verifies a user-authored
// (unstamped) skill is left intact by uninstall.
// Failure prevented: uninstall destroys a user's own skill that happens to live
// under .claude/skills/.
func TestHandleUninstallSkills_PreservesUnstampedSkill(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	userDir := filepath.Join(dir, ".claude", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	userFile := filepath.Join(userDir, skillFileName)
	require.NoError(t, os.WriteFile(userFile, []byte("---\nname: my-skill\n---\nno stamp\n"), 0o644))

	_, err = handleUninstallSkills(params)
	require.NoError(t, err)

	_, err = os.Stat(userFile)
	assert.NoError(t, err, "user-authored unstamped skill must NOT be removed by uninstall")
}

// --- D. splitFrontmatter ---

// TestSplitFrontmatter covers the parser that underpins stamp placement:
// has-frontmatter, no-frontmatter, empty-frontmatter, and a missing closing
// fence (which must be treated as no frontmatter, defensively).
// Failure prevented: misparsing frontmatter places the stamp inside or before
// the frontmatter, breaking Claude's parse or the drift hash contract.
func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantOK   bool
		wantFM   string
		wantBody string
	}{
		{
			name:     "has frontmatter",
			content:  "---\nname: x\n---\nbody line\n",
			wantOK:   true,
			wantFM:   "---\nname: x\n---\n",
			wantBody: "body line\n",
		},
		{
			name:     "no frontmatter",
			content:  "# just a heading\nbody\n",
			wantOK:   false,
			wantFM:   "",
			wantBody: "# just a heading\nbody\n",
		},
		{
			name:     "empty frontmatter",
			content:  "---\n---\nbody\n",
			wantOK:   true,
			wantFM:   "---\n---\n",
			wantBody: "body\n",
		},
		{
			name:     "no closing fence",
			content:  "---\nname: x\nstill open\n",
			wantOK:   false,
			wantFM:   "",
			wantBody: "---\nname: x\nstill open\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, ok := splitFrontmatter([]byte(tc.content))
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantFM, string(fm), "frontmatter mismatch")
			assert.Equal(t, tc.wantBody, string(body), "body mismatch")
			if !ok {
				// when there's no recognized frontmatter, body must equal the full
				// content so the stamp falls back to covering everything.
				assert.Equal(t, tc.content, string(body))
			}
		})
	}
}

// --- E. Command→skill migration cleanup (FIX 2) ---

// TestHandleInstallSkills_RemovesStampedLegacyCommand verifies the self-cleaning
// migration: when a surface moves from a slash command to a skill, an existing
// install's stale ox-stamped .claude/commands/<id>.md is pruned on skill install
// so the agent isn't left with a duplicate slash-invocable Layer-2 surface.
// Failure prevented: ox-plan / ox-session-review remain slash-invocable as stale
// commands alongside the new skill after a command→skill migration.
func TestHandleInstallSkills_RemovesStampedLegacyCommand(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	// simulate a prior install that wrote the surface as a slash command, stamped
	// the same way the command installer stamps (ox prefix, stamp on line 1).
	commandsDir := filepath.Join(dir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(commandsDir, 0o755))
	legacyPath := filepath.Join(commandsDir, name+".md")
	stamped := agentx.StampedContent([]byte("# legacy "+name+" command body\n"), "0.7.0", oxSkillStampPrefix)
	require.NoError(t, os.WriteFile(legacyPath, stamped, 0o644))

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	_, err = os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(err),
		"a stamped legacy command file superseded by an embedded skill must be removed on install")

	// the new skill must exist after migration.
	_, err = os.Stat(filepath.Join(dir, ".claude", "skills", name, skillFileName))
	assert.NoError(t, err, "the superseding skill must be installed")
}

// TestHandleInstallSkills_PreservesUnstampedLegacyCommand verifies the cleanup is
// defensive: a user-authored (unstamped) .claude/commands/<id>.md sharing an id
// with an embedded skill is NEVER deleted. We only prune files ox itself stamped.
// Failure prevented: install destroys a user's own slash command that happens to
// share a name with a shipped skill.
func TestHandleInstallSkills_PreservesUnstampedLegacyCommand(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	commandsDir := filepath.Join(dir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(commandsDir, 0o755))
	legacyPath := filepath.Join(commandsDir, name+".md")
	userContent := "# my own " + name + " command, no ox stamp\n"
	require.NoError(t, os.WriteFile(legacyPath, []byte(userContent), 0o644))

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err, "unstamped user-authored command file must survive install")
	assert.Equal(t, userContent, string(data), "user-authored command content must be untouched")
}
