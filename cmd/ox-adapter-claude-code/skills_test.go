package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pickSkill returns a default-installed skill name, failing the test if none
// ship. Lifecycle tests must use a skill supplied by the default installer.
func pickSkill(t *testing.T) string {
	t.Helper()
	skills, err := skills.Selected("0.8.0", nil)
	require.NoError(t, err)
	require.NotEmpty(t, skills, "at least one default skill must ship for these tests to mean anything")
	return skills[0].Name
}

// --- A. Install lifecycle / stamp placement ---

// TestHandleInstallSkills_ManifestOwnership verifies native frontmatter stays
// byte-clean while ownership moves to the project lockfile.
func TestHandleInstallSkills_ManifestOwnership(t *testing.T) {
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

	assert.NotContains(t, string(data), agentx.StampComment(oxSkillStampPrefix),
		"new native skills use manifest ownership; inline stamps are migration-only")
	assert.FileExists(t, filepath.Join(dir, ".sageox", "skills.lock.json"))
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

// Explicit selections must be saved even when their skills also belong to defaults.
func TestHandleInstallSkills_ExplicitSelection(t *testing.T) {
	dir := t.TempDir()
	installed, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: dir, Version: "0.8.0", Names: []string{"ox-cli-consult", "ox-cli-recap"},
	})
	require.NoError(t, err)
	assert.Contains(t, installed.FilesWritten, filepath.Join(".claude", "skills", "ox-cli-consult", skillFileName))
	assert.Contains(t, installed.FilesWritten, filepath.Join(".claude", "skills", "ox-cli-recap", skillFileName))
	desired, _, err := skillmanager.LoadDesired(dir)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ox-cli-consult", "ox-cli-recap"}, desired.Names)
}

// Retired opt-ins must not make the Claude-only cleanup fail after installation
// succeeds, or expand an explicitly empty selection into cleanup of every skill.
func TestHandleInstallSkills_RetiredSelectionsDoNotBreakLegacyCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("short: multiple complete skill installations and command migrations")
	}
	tests := []struct {
		name          string
		names         []string
		bundles       []string
		removeConsult bool
	}{
		{name: "retired bundle", bundles: []string{"attest"}},
		{name: "retired skill names", names: []string{"ox-cli-attest-goal", "ox-cli-attest-create"}},
		{name: "older retired skill names", names: []string{"ox-attest-goal", "ox-attest-create"}},
		{name: "retired and active bundles", bundles: []string{"attest", "core"}, removeConsult: true},
		{name: "retired and active names", names: []string{"ox-cli-attest-goal", "ox-cli-consult"}, bundles: []string{"attest"}, removeConsult: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			commandsDir := filepath.Join(dir, ".claude", "commands")
			require.NoError(t, os.MkdirAll(commandsDir, 0o755))
			for _, name := range []string{"ox-cli-consult", "ox-cli-prime"} {
				content := agentx.StampedContent([]byte("# legacy "+name+"\n"), "0.8.0", oxSkillStampPrefix)
				require.NoError(t, os.WriteFile(filepath.Join(commandsDir, name+".md"), content, 0o644))
			}

			result, err := handleInstallSkills(adapterprotocol.SkillsParams{
				RepoRoot: dir, Version: "0.8.0", Names: tt.names, Bundles: tt.bundles,
			})
			require.NoError(t, err)
			require.True(t, result.Installed)
			assert.FileExists(t, filepath.Join(skillsDir(dir), "ox-cli-consult", skillFileName))
			assert.NoDirExists(t, filepath.Join(skillsDir(dir), "ox-cli-attest-goal"))
			assert.NoDirExists(t, filepath.Join(skillsDir(dir), "ox-cli-attest-create"))
			if tt.removeConsult {
				assert.NoFileExists(t, filepath.Join(commandsDir, "ox-cli-consult.md"))
			} else {
				assert.FileExists(t, filepath.Join(commandsDir, "ox-cli-consult.md"))
			}
			assert.FileExists(t, filepath.Join(commandsDir, "ox-cli-prime.md"),
				"explicit selections must not clean up unrelated default commands")
		})
	}
}

// Ignoring known retired selections must not hide unknown requests or partially
// install skills before reporting their validation error.
func TestHandleInstallSkills_RejectsUnknownOptInSelection(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		bundles []string
		want    string
	}{
		{name: "unknown name", names: []string{"does-not-exist"}, want: "unknown ox skill(s): [does-not-exist]"},
		{name: "unknown bundle", bundles: []string{"does-not-exist"}, want: `unknown ox skill bundle "does-not-exist"`},
		{name: "retired and unknown name", names: []string{"ox-cli-attest-goal", "does-not-exist"}, want: "unknown ox skill(s): [does-not-exist]"},
		{name: "retired and unknown bundle", bundles: []string{"attest", "does-not-exist"}, want: `unknown ox skill bundle "does-not-exist"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := handleInstallSkills(adapterprotocol.SkillsParams{
				RepoRoot: dir, Version: "0.8.0", Names: tt.names, Bundles: tt.bundles,
			})
			require.EqualError(t, err, tt.want)
			assert.NoFileExists(t, filepath.Join(dir, ".sageox", "skills.lock.json"))
			assert.NoFileExists(t, filepath.Join(skillsDir(dir), "ox-cli-consult", skillFileName))
		})
	}
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

// TestHandleCheckSkills_BodyEditIsRestored pins the 0.15.0 ownership inversion.
//
// A managed skill in a RESERVED namespace is ox's unconditionally: a local edit is
// restored on the next reconcile rather than preserved as a conflict.
//
// The old behavior was correct while these files were TRACKED — an edit appeared
// in git diff, so it was visible and plausibly deliberate. Once they are
// gitignored, a preserved edit is permanent SILENT drift: one machine quietly
// running a different playbook, invisible to git, unrepairable by ox, and
// undiagnosable by a teammate reading the same repository.
//
// Editing one of these files to experiment is fine and expected; customizing means
// forking to a name of your own OUTSIDE the reserved prefixes.
func TestHandleCheckSkills_BodyEditIsRestored(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(skillPath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	_, err = handleInstallSkills(params)
	require.NoError(t, err)

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, string(orig), string(restored),
		"a reserved-namespace skill must be restored to the shipped content, not left drifted")
	assert.NotContains(t, string(restored), "hand-edited drift")
}

// TestHandleCheckSkills_FrontmatterEditIsRestored keeps the gap CodeRabbit found
// covered under the new ownership rule.
//
// The drift stamp's hash covers ONLY the body below it, so editing the YAML
// frontmatter (name/description) leaves the body hash and stamp line
// byte-identical — a body-only staleness check sees nothing wrong and the agent
// reads tampered metadata forever. The description is the skill's activation
// surface, so tampering with it silently changes when the skill fires.
//
// What changed in 0.15.0 is the remedy, not the detection: the edit is now
// RESTORED rather than reported and preserved.
func TestHandleCheckSkills_FrontmatterEditIsRestored(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)

	fence := "\n---\n"
	fenceIdx := strings.Index(string(orig), fence)
	require.GreaterOrEqual(t, fenceIdx, 0, "installed skill must have a closing frontmatter fence")
	tampered := string(orig[:fenceIdx]) + "\ndescription: hand-edited frontmatter drift" + string(orig[fenceIdx:])
	require.NotEqual(t, string(orig), tampered, "the edit must actually change the frontmatter")
	require.NoError(t, os.WriteFile(skillPath, []byte(tampered), 0o644))

	_, err = handleInstallSkills(params)
	require.NoError(t, err)

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, string(orig), string(restored),
		"a frontmatter-only edit must be restored; the description decides when the skill activates")
}

// TestHandleInstallSkills_ReclaimsASquattedReservedName covers the case that used
// to be preserved forever: a file sitting at a RESERVED name that ox never wrote.
//
// The prefix is the contract. Reclaiming is safe precisely because the namespace
// is reserved and gitignored — and it is bounded: a skill OUTSIDE the prefixes is
// never touched, which the companion assertion here pins.
func TestHandleInstallSkills_ReclaimsASquattedReservedName(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	squatted := "---\nname: " + name + "\ndescription: totally different, user-owned, no stamp\n---\nuser body\n"
	skillPath := filepath.Join(skillDir, skillFileName)
	require.NoError(t, os.WriteFile(skillPath, []byte(squatted), 0o644))

	// A skill the user owns, outside the reserved prefixes, in the same directory.
	mineDir := filepath.Join(dir, ".claude", "skills", "my-own-skill")
	require.NoError(t, os.MkdirAll(mineDir, 0o755))
	mine := "---\nname: my-own-skill\ndescription: mine\n---\nmy body\n"
	minePath := filepath.Join(mineDir, skillFileName)
	require.NoError(t, os.WriteFile(minePath, []byte(mine), 0o644))

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	after, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.NotEqual(t, squatted, string(after),
		"a reserved name must be reclaimed; leaving it means the agent reads content ox cannot update")

	untouched, err := os.ReadFile(minePath)
	require.NoError(t, err)
	assert.Equal(t, mine, string(untouched),
		"a skill outside the reserved prefixes must never be touched")
}

// TestHandleCheckSkills_UserAuthoredSkillIsNeverTouched verifies ox leaves skills
// it does not own completely alone — never flagged, never rewritten.
//
// The fixture deliberately uses a name OUTSIDE the reserved prefixes. That is the
// whole boundary: inside ox-cli-* / sageox-team-* ox owns the bytes absolutely,
// and everywhere else the directory belongs to the user. Testing this with a
// reserved name would assert the opposite of the contract.
//
// Failure prevented: ox flags or overwrites a user's own skill, either nagging
// forever or destroying their work.
func TestHandleCheckSkills_UserAuthoredSkillIsNeverTouched(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	const name = "my-own-skill"
	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	userContent := "---\nname: " + name + "\ndescription: my own skill, no stamp\n---\nuser body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, skillFileName), []byte(userContent), 0o644))

	resp, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, resp.Stale, name, "a user-authored skill must never be flagged stale")

	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(skillDir, skillFileName))
	require.NoError(t, err)
	assert.Equal(t, userContent, string(after), "install must never overwrite a user-authored skill")
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
	assert.Contains(t, resp.FilesRemoved, filepath.Join(".claude", "skills", name, skillFileName),
		"FilesRemoved must reference the removed SKILL.md")

	_, err = os.Stat(filepath.Join(skillDir, skillFileName))
	assert.True(t, os.IsNotExist(err), "stamped SKILL.md must be removed by uninstall")
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

// --- D. Command→skill migration cleanup ---

// TestHandleInstallSkills_RemovesStampedLegacyCommand verifies the self-cleaning
// migration: when a surface moves from a slash command to a skill, an existing
// install's stale ox-stamped .claude/commands/<id>.md is pruned on skill install
// so the agent isn't left with a duplicate slash-invocable Layer-2 surface.
// Failure prevented: ox-cli-plan / ox-cli-session-review remain slash-invocable as stale
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
