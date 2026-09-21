package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/stretchr/testify/require"
)

// TestCollectSkillsCatalog_SeparatesWhatIsHereFromWhatCouldBe.
//
// This is the whole command. Until the first non-default bundle existed nothing
// was ever "available but not installed", so a catalog was a static rendering of
// a compiled-in constant; the two states have to be told apart against a real
// repository or the surface means nothing.
func TestCollectSkillsCatalog_SeparatesWhatIsHereFromWhatCouldBe(t *testing.T) {
	repo := t.TempDir()
	const root = ".claude/skills"
	writeSkillDir(t, repo, root, "ox-cli-plan", manifestWithDescription("ox-cli-plan", "plan things"))

	got, err := collectSkillsCatalog(repo, []string{root})
	require.NoError(t, err)

	rows := map[string]catalogSkillRow{}
	bundles := map[string]catalogBundleGroup{}
	for _, bundle := range got.Bundles {
		bundles[bundle.ID] = bundle
		for _, row := range bundle.Skills {
			rows[row.Name] = row
		}
	}

	require.Equal(t, skillInstalled, rows["ox-cli-plan"].Status)
	require.Equal(t, skillAvailable, rows[catalogOptInSkill].Status)
	require.Equal(t, skillAvailable, rows["ox-cli-viz"].Status,
		"a skill ox ships that is not on disk must read as available, not installed")

	require.Equal(t, "core", rows["ox-cli-plan"].Bundle,
		"the row does not say how the skill would be selected")
	require.False(t, bundles["team"].Default,
		"the opt-in bundle must not report itself as a default or nothing is ever available")
	require.True(t, bundles["core"].Default)
	require.NotEmpty(t, bundles["team"].Description, "a bundle with no description cannot be chosen")
}

// TestCollectSkillsCatalog_UnreadableRootFailsInsteadOfPublishingAnIncompleteCatalog.
//
// collectInstalledSkills reports a root it genuinely could not read in
// .Problems, separately from .Skills. Reading only .Skills — as this function
// once did — would take the silence at face value: a skill sitting in that
// unreadable root is invisible to `installed`, so it comes back "available"
// and the guidance tells the reader to install something they already have.
// The fix is to fail the whole catalog rather than answer confidently with a
// hole in it; `ox skills list` already owns partial reporting for this exact
// condition.
func TestCollectSkillsCatalog_UnreadableRootFailsInsteadOfPublishingAnIncompleteCatalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod does not deny reads")
	}

	repo := t.TempDir()
	const root = ".claude/skills"
	writeSkillDir(t, repo, root, "ox-cli-plan", manifestWithDescription("ox-cli-plan", "plan things"))

	rootDir := filepath.Join(repo, filepath.FromSlash(root))
	require.NoError(t, os.Chmod(rootDir, 0o000))
	// Restore before TempDir cleanup tries to remove it, and restore it even if
	// the assertions below fail first.
	t.Cleanup(func() { _ = os.Chmod(rootDir, 0o755) })

	_, err := collectSkillsCatalog(repo, []string{root})
	require.Error(t, err,
		"a skill sitting in a root ox could not read must not be silently reported as available")
	require.Contains(t, err.Error(), root, "the error must name which root ox could not read")
}

// TestCollectSkillsCatalog_EveryShippedSkillHasADescription.
//
// The description IS the activation surface — an agent selects a skill by
// reading it. A catalog entry whose description this cannot parse is a skill
// that installs and is then never chosen, and nothing else in the build reports
// it: extensions/skills' own validator accepts a folded value without reading
// what it folds to.
func TestCollectSkillsCatalog_EveryShippedSkillHasADescription(t *testing.T) {
	got, err := collectSkillsCatalog(t.TempDir(), nil)
	require.NoError(t, err)

	var seen int
	for _, bundle := range got.Bundles {
		for _, row := range bundle.Skills {
			seen++
			require.NotEmpty(t, row.Description,
				"catalog skill %q has no readable description, so no agent will ever pick it", row.Name)
		}
	}
	require.NotZero(t, seen)
}

// TestCollectSkillsCatalog_NeverOffersARetiredName.
//
// Retirement keeps a name listed for two releases so the reconciler can still
// recognize and delete an old copy. A catalog that advertised those would be
// selling something the installer is actively removing.
func TestCollectSkillsCatalog_NeverOffersARetiredName(t *testing.T) {
	got, err := collectSkillsCatalog(t.TempDir(), nil)
	require.NoError(t, err)

	for _, bundle := range got.Bundles {
		for _, row := range bundle.Skills {
			require.False(t, skills.IsRetired(row.Name), "the catalog offers the retired name %q", row.Name)
		}
	}
}

// TestSkillsCatalogGuidance_HandsBackARunnableNextAction. The catalog's job ends
// with the command that acts on it; a listing with no way to act on it is the
// dead end this whole family exists to remove.
func TestSkillsCatalogGuidance_HandsBackARunnableNextAction(t *testing.T) {
	t.Run("something is available", func(t *testing.T) {
		got, err := collectSkillsCatalog(t.TempDir(), nil)
		require.NoError(t, err)
		requireAdviceResolves(t, got.Guidance)
		require.Contains(t, got.Guidance, "ox skills install ",
			"guidance must name the install command, not merely describe the state: %q", got.Guidance)
	})

	t.Run("everything is installed", func(t *testing.T) {
		got := skillsCatalogGuidance(skillsCatalogOutput{Bundles: []catalogBundleGroup{{
			ID: "core", Skills: []catalogSkillRow{{Name: "ox-cli-plan", Status: skillInstalled}},
		}}})
		requireAdviceResolves(t, got)
		require.NotContains(t, got, "ox skills install",
			"telling someone to install when nothing is available is advice that cannot be followed")
	})
}

// TestEmitSkillsCatalog_FitsEightyColumns, measured on the ANSI-stripped output:
// lipgloss always emits color and the test buffer is not a terminal.
func TestEmitSkillsCatalog_FitsEightyColumns(t *testing.T) {
	out, err := collectSkillsCatalog(t.TempDir(), nil)
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, emitSkillsCatalog(&buf, out, false))

	rendered := buf.String()
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(stripANSI(line))), skillsTableWidth,
			"a row wraps on an 80-column terminal: %q", line)
	}
	// The status column carries an ANSI-styled word; padding the RENDERED string
	// would count escape bytes as characters and knock the description out of
	// alignment on every installed row.
	require.Contains(t, stripANSI(rendered), "Curated team skills, installed on request")
}

// TestSkillsCatalogJSON_AlwaysAnswersEveryQuestionItCanAnswer, asserted against
// the BYTES rather than the struct that produced them.
func TestSkillsCatalogJSON_AlwaysAnswersEveryQuestionItCanAnswer(t *testing.T) {
	out, err := collectSkillsCatalog(t.TempDir(), nil)
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, emitSkillsCatalog(&buf, out, true))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got), "output was not JSON: %q", buf.String())
	require.NotEmpty(t, got["guidance"])

	bundles, ok := got["bundles"].([]any)
	require.True(t, ok, "bundles is absent or not an array: %v", got)
	require.NotEmpty(t, bundles)
	for _, raw := range bundles {
		bundle, isObject := raw.(map[string]any)
		require.True(t, isObject)
		_, hasDefault := bundle["default"]
		require.True(t, hasDefault,
			"`default` is absent, so a reader cannot tell an opt-in bundle from an ox too old to say: %v", bundle)
		list, hasSkills := bundle["skills"]
		require.True(t, hasSkills)
		require.NotNil(t, list, "`skills` was null rather than an empty array: %v", bundle)
	}
}
