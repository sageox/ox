package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/require"
)

func catalogRowNamed(t *testing.T, out skillsCatalogOutput, name string) catalogSkillRow {
	t.Helper()
	for _, bundle := range out.Bundles {
		for _, row := range bundle.Skills {
			if row.Name == name {
				return row
			}
		}
	}
	t.Fatalf("catalog has no row named %q", name)
	return catalogSkillRow{}
}

func selectCatalogNameInLock(t *testing.T, repo, name string) {
	t.Helper()
	lockPath := skillmanager.LockPath(repo)
	data, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	var lock map[string]any
	require.NoError(t, json.Unmarshal(data, &lock))
	desired, ok := lock["desired"].(map[string]any)
	require.True(t, ok)
	names, _ := desired["names"].([]any)
	desired["names"] = append(names, name)
	data, err = json.Marshal(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lockPath, data, 0o644))
}

// TestCollectSkillsCatalog_SeparatesWhatIsHereFromWhatCouldBe.
//
// This is the whole command. Until the first non-default bundle existed nothing
// was ever "available but not installed", so a catalog was a static rendering of
// a compiled-in constant; the two states have to be told apart against a real
// repository or the surface means nothing.
func TestCollectSkillsCatalog_SeparatesWhatIsHereFromWhatCouldBe(t *testing.T) {
	repo := stageInstallRepo(t)
	const root = approvalTargetRoot

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

	require.Equal(t, skillCatalogProjected, rows["ox-cli-plan"].Status)
	require.True(t, rows["ox-cli-plan"].Selected)
	require.True(t, rows["ox-cli-plan"].Projected)
	require.Equal(t, skillAvailable, rows[catalogOptInSkill].Status)

	require.Equal(t, "core", rows["ox-cli-plan"].Bundle,
		"the row does not say how the skill would be selected")
	require.False(t, bundles["team"].Default,
		"the opt-in bundle must not report itself as a default or nothing is ever available")
	require.True(t, bundles["core"].Default)
	require.NotEmpty(t, bundles["team"].Description, "a bundle with no description cannot be chosen")
}

func TestCollectSkillsCatalog_DistinguishesSelectionProjectionAndNameMatches(t *testing.T) {
	t.Run("selected but not projected", func(t *testing.T) {
		repo := t.TempDir()
		stageSelectedTarget(t, repo)

		got, err := collectSkillsCatalog(repo, []string{approvalTargetRoot})
		require.NoError(t, err)
		row := catalogRowNamed(t, got, "ox-cli-plan")
		require.Equal(t, skillCatalogSelected, row.Status)
		require.True(t, row.Selected)
		require.False(t, row.Projected)
	})

	t.Run("selected but conflicting", func(t *testing.T) {
		repo := t.TempDir()
		stageSelectedTarget(t, repo)
		selectCatalogNameInLock(t, repo, catalogOptInSkill)
		writeSkillDir(t, repo, approvalTargetRoot, catalogOptInSkill,
			manifestWithDescription(catalogOptInSkill, "mine"))

		got, err := collectSkillsCatalog(repo, []string{approvalTargetRoot})
		require.NoError(t, err)
		row := catalogRowNamed(t, got, catalogOptInSkill)
		require.Equal(t, skillCatalogConflict, row.Status)
		require.True(t, row.Selected)
		require.True(t, row.Conflicting)
		require.False(t, row.Projected)
	})

	t.Run("unselected same-name local skill", func(t *testing.T) {
		repo := t.TempDir()
		stageSelectedTarget(t, repo)
		writeSkillDir(t, repo, approvalTargetRoot, catalogOptInSkill,
			manifestWithDescription(catalogOptInSkill, "mine"))

		got, err := collectSkillsCatalog(repo, []string{approvalTargetRoot})
		require.NoError(t, err)
		row := catalogRowNamed(t, got, catalogOptInSkill)
		require.Equal(t, skillCatalogNameMatch, row.Status)
		require.False(t, row.Selected)
		require.True(t, row.NameMatch)
		require.False(t, row.Projected)
		require.Contains(t, row.Detail, "does not own")
	})

	t.Run("selected collision without a valid manifest", func(t *testing.T) {
		repo := t.TempDir()
		stageSelectedTarget(t, repo)
		selectCatalogNameInLock(t, repo, catalogOptInSkill)
		dir := filepath.Join(repo, approvalTargetRoot, catalogOptInSkill)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("mine, but malformed"), 0o644))

		got, err := collectSkillsCatalog(repo, []string{approvalTargetRoot})
		require.NoError(t, err)
		row := catalogRowNamed(t, got, catalogOptInSkill)
		require.Equal(t, skillCatalogConflict, row.Status,
			"a same-name manifest stopped being a conflict merely because its frontmatter was malformed")
	})
}

func TestCollectSkillsCatalog_UsesOwnershipPlanForManagedDrift(t *testing.T) {
	repo := stageInstallRepo(t)
	_, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill)
	require.NoError(t, err)

	manifest := filepath.Join(installedSkillDirPath(repo, catalogOptInSkill), "SKILL.md")
	file, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString("\nlocally edited drift\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	got, err := collectSkillsCatalog(repo, []string{approvalTargetRoot})
	require.NoError(t, err)
	row := catalogRowNamed(t, got, catalogOptInSkill)
	require.Equal(t, skillCatalogSelected, row.Status,
		"repairable drift is selected-but-stale, not an unowned conflict")
	require.True(t, row.Selected)
	require.False(t, row.Projected)
	require.False(t, row.Conflicting)
}

// TestCollectSkillsCatalog_UnreadableRootFailsInsteadOfPublishingAnIncompleteCatalog.
//
// collectInstalledSkills reports a root it genuinely could not read in
// .Problems, separately from .Skills. Reading only .Skills — as this function
// once did — would take the silence at face value: a skill sitting in that
// unreadable root makes ownership unknowable, so a same-named skill could come
// back "available" and the guidance would tell the reader to install into a
// collision.
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

	t.Run("everything is projected", func(t *testing.T) {
		got := skillsCatalogGuidance(skillsCatalogOutput{Bundles: []catalogBundleGroup{{
			ID: "core", Skills: []catalogSkillRow{{Name: "ox-cli-plan", Status: skillCatalogProjected}},
		}}})
		requireAdviceResolves(t, got)
		require.NotContains(t, got, "ox skills install",
			"telling someone to install when nothing is available is advice that cannot be followed")
	})

	t.Run("one skill is available", func(t *testing.T) {
		got := skillsCatalogGuidance(skillsCatalogOutput{Bundles: []catalogBundleGroup{{
			ID: "team", Skills: []catalogSkillRow{{Name: "post-cutoff", Status: skillAvailable}},
		}}})
		require.Contains(t, got, "1 of the 1 skills ox ships is available for selection")
	})

	t.Run("conflict takes priority over installation advice", func(t *testing.T) {
		got := skillsCatalogGuidance(skillsCatalogOutput{Bundles: []catalogBundleGroup{{
			ID: "team", Skills: []catalogSkillRow{
				{Name: "post-cutoff", Status: skillCatalogConflict},
				{Name: "other", Status: skillAvailable},
			},
		}}})
		requireAdviceResolves(t, got)
		require.Contains(t, got, "same-named content")
		require.NotContains(t, got, "ox skills install",
			"installing while a selected collision exists would only repeat the failure")
	})
}

// TestRunSkillsCatalog_DrivesTheRealCommand covers the command boundary rather
// than only its collector and renderer. The boundary owns repository discovery,
// lockfile errors, flag parsing, and wiring the result to stdout; any one of
// those can break while the pure helpers below stay green.
func TestRunSkillsCatalog_DrivesTheRealCommand(t *testing.T) {
	t.Run("json success", func(t *testing.T) {
		stageInstallRepo(t)
		out, err := runSkillsChange(t, skillsCatalogCmd, "--json")
		require.NoError(t, err)
		var got skillsCatalogOutput
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		require.NotEmpty(t, got.Bundles)
	})

	t.Run("outside a repository", func(t *testing.T) {
		chdirOutsideGit(t)
		_, err := runSkillsChange(t, skillsCatalogCmd)
		require.ErrorContains(t, err, "not inside a git repository")
	})

	t.Run("unreadable selection", func(t *testing.T) {
		repo := stageInstallRepo(t)
		require.NoError(t, os.WriteFile(skillmanager.LockPath(repo), []byte("{broken"), 0o644))
		_, err := runSkillsChange(t, skillsCatalogCmd)
		require.Error(t, err)
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

func TestEmitSkillsCatalog_StylesProjectedRows(t *testing.T) {
	out := skillsCatalogOutput{
		Bundles: []catalogBundleGroup{{
			ID: "team", Description: "Team workflows", Default: false,
			Skills: []catalogSkillRow{{Name: "post-cutoff", Status: skillCatalogProjected, Description: "Review dates"}},
		}},
	}
	var buf strings.Builder
	require.NoError(t, emitSkillsCatalog(&buf, out, false))
	require.Contains(t, stripANSI(buf.String()), "projected")
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
		for _, rawRow := range list.([]any) {
			row := rawRow.(map[string]any)
			for _, key := range []string{"selected", "projected", "conflicting", "name_match"} {
				_, present := row[key]
				require.True(t, present, "%q is absent from catalog row: %v", key, row)
			}
		}
	}
}
