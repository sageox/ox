package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/require"
)

// writeSkillDir authors one skill directory under a repo-relative root.
func writeSkillDir(t *testing.T, repo, root, name, manifest string) string {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(root), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644))
	return dir
}

func manifestWithDescription(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nbody\n"
}

// TestCollectInstalledSkills_ClassifiesEveryProvenance.
//
// The local rows are the reason this command exists. In a real repository
// hand-authored skills are the majority, and a listing that renders only the
// rows ox has a lockfile for teaches the reader that the list is lying.
func TestCollectInstalledSkills_ClassifiesEveryProvenance(t *testing.T) {
	repo := t.TempDir()
	const root = ".claude/skills"
	writeSkillDir(t, repo, root, skillmanager.CLIPrefix+"plan", manifestWithDescription("ox-cli-plan", "plan things"))
	writeSkillDir(t, repo, root, skillmanager.CommittedOnRamp, manifestWithDescription("sageox", "the on-ramp"))
	writeSkillDir(t, repo, root, skillmanager.TeamPrefix+"deploy", manifestWithDescription("sageox-team-deploy", "how we deploy"))
	writeSkillDir(t, repo, root, "my-own-skill", manifestWithDescription("my-own-skill", "mine, hand written"))
	// Shipped by ox in the opt-in bundle, and carrying NO reserved prefix. Naming
	// alone cannot classify it, which is the whole reason skillProvenance consults
	// the catalog.
	writeSkillDir(t, repo, root, catalogOptInSkill, manifestWithDescription(catalogOptInSkill, "curated facts"))

	got := collectInstalledSkills(repo, []string{root})

	byName := map[string]installedSkillRow{}
	for _, row := range got.Skills {
		byName[row.Name] = row
	}
	require.Len(t, byName, 5)
	require.Equal(t, provenanceOx, byName[skillmanager.CLIPrefix+"plan"].Provenance)
	require.Equal(t, provenanceOx, byName[skillmanager.CommittedOnRamp].Provenance,
		"the committed on-ramp is deliberately unprefixed; a prefix-only rule files ox's own file under local")
	require.Equal(t, provenanceTeam, byName[skillmanager.TeamPrefix+"deploy"].Provenance)
	require.Equal(t, provenanceLocal, byName["my-own-skill"].Provenance)
	require.Equal(t, provenanceOx, byName[catalogOptInSkill].Provenance,
		"a catalog skill with no reserved prefix was reported as the human's own work")

	require.Equal(t, "mine, hand written", byName["my-own-skill"].Description,
		"the description column is empty, so a reader cannot tell what a local skill is for")
	require.Empty(t, got.Problems)
}

// TestCollectInstalledSkills_OneSkillInTwoRootsIsOneRow: a repo wired to both
// Claude Code and Codex installs each skill twice. Two rows would read as two
// skills.
func TestCollectInstalledSkills_OneSkillInTwoRootsIsOneRow(t *testing.T) {
	repo := t.TempDir()
	roots := []string{".claude/skills", ".agents/skills"}
	for _, root := range roots {
		writeSkillDir(t, repo, root, "ox-cli-plan", manifestWithDescription("ox-cli-plan", "plan things"))
	}

	got := collectInstalledSkills(repo, roots)

	require.Len(t, got.Skills, 1)
	require.Equal(t, roots, got.Skills[0].Roots,
		"both homes must be reported: a skill present in only one of two selected roots is a real half-install")
}

// TestCollectInstalledSkills_IgnoresDirectoriesThatAreNotSkills: a skill is a
// directory with a manifest. Listing everything under the root would report a
// skill's own references/ subdirectory, and any stray folder, as skills.
func TestCollectInstalledSkills_IgnoresDirectoriesThatAreNotSkills(t *testing.T) {
	repo := t.TempDir()
	const root = ".claude/skills"
	writeSkillDir(t, repo, root, "real", manifestWithDescription("real", "a skill"))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, filepath.FromSlash(root), "not-a-skill"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, filepath.FromSlash(root), "README.md"), []byte("hi"), 0o644))

	got := collectInstalledSkills(repo, []string{root})

	require.Len(t, got.Skills, 1)
	require.Equal(t, "real", got.Skills[0].Name)
}

// TestCollectInstalledSkills_AnUnreadableRootIsReportedNotSilentlyEmpty.
//
// An unreadable root and an empty one render identically, and they need opposite
// fixes. A missing root is different again and is NOT a problem — a fresh
// checkout has not materialized anything yet, and `ox skills status` owns that
// explanation.
func TestCollectInstalledSkills_AnUnreadableRootIsReportedNotSilentlyEmpty(t *testing.T) {
	repo := t.TempDir()

	t.Run("missing root is silent", func(t *testing.T) {
		got := collectInstalledSkills(repo, []string{".claude/skills"})
		require.Empty(t, got.Problems)
		require.Empty(t, got.Skills)
	})

	t.Run("root that is a file is reported", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".agents", "skills"), []byte("not a directory"), 0o644))

		got := collectInstalledSkills(repo, []string{".agents/skills"})

		require.Len(t, got.Problems, 1)
		require.Contains(t, got.Problems[0], ".agents/skills")
		require.Equal(t, got.Problems[0], got.Guidance,
			"a root ox cannot read must become the next action, not a footnote")
	})
}

// TestManifestDescription_ReadsTextButReportsShape.
//
// Two callers need different halves of this one read: the listings want the
// TEXT, and `ox skills install --team` needs to know the value was a block
// scalar, because a Team Context stores the marker and drops the text.
func TestManifestDescription_ReadsTextButReportsShape(t *testing.T) {
	for _, tc := range []struct {
		name       string
		manifest   string
		want       string
		wantFolded bool
	}{
		{
			name:     "single line",
			manifest: "---\nname: a\ndescription: does a thing\n---\n",
			want:     "does a thing",
		},
		{
			name:     "quoted",
			manifest: "---\nname: a\ndescription: \"does a thing\"\n---\n",
			want:     "does a thing",
		},
		{
			name:       "folded scalar folds its continuation lines in",
			manifest:   "---\nname: a\ndescription: >-\n  does a thing\n  across two lines\n---\n",
			want:       "does a thing across two lines",
			wantFolded: true,
		},
		{
			name:       "literal scalar",
			manifest:   "---\nname: a\ndescription: |\n  does a thing\n---\n",
			want:       "does a thing",
			wantFolded: true,
		},
		{
			name:     "an indented key is not a key",
			manifest: "---\nname: a\nmeta:\n  description: nested\n---\n",
			want:     "",
		},
		{
			name:     "no frontmatter at all",
			manifest: "# just a heading\ndescription: not frontmatter\n",
			want:     "",
		},
		{
			name:     "control bytes cannot hide the rest of the row",
			manifest: "---\nname: a\ndescription: safe\x1b[31mtext\n---\n",
			want:     "safe[31mtext",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, folded := manifestDescription([]byte(tc.manifest))
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantFolded, folded)
		})
	}
}

// TestManifestDescription_StopsWhereTheTeamParserStops. The Team Context reader
// gives up after 30 lines of frontmatter, so a description below that line is
// invisible there — and reporting it here would promise something publishing
// cannot deliver.
func TestManifestDescription_StopsWhereTheTeamParserStops(t *testing.T) {
	manifest := "---\n" + strings.Repeat("filler: x\n", 40) + "description: too late\n---\n"

	got, folded := manifestDescription([]byte(manifest))

	require.Empty(t, got)
	require.False(t, folded)
}

// TestEmitSkillsList_FitsEightyColumns.
//
// Asserted on the ANSI-stripped output: lipgloss always emits color and the test
// buffer is not a terminal, so measuring the styled bytes would measure escape
// codes.
func TestEmitSkillsList_FitsEightyColumns(t *testing.T) {
	out := skillsListOutput{
		Roots: []string{".claude/skills"},
		Skills: []installedSkillRow{
			{Name: "ox-cli-session-review", Provenance: provenanceOx,
				Description: strings.Repeat("a very long description that keeps going ", 8)},
			{Name: strings.Repeat("long-skill-name-", 6), Provenance: provenanceLocal, Description: "short"},
		},
		Problems: []string{},
		Guidance: "next action here",
	}

	var buf strings.Builder
	require.NoError(t, emitSkillsList(&buf, out, false))

	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(stripANSI(line))), skillsTableWidth,
			"a row wraps on an 80-column terminal: %q", line)
	}
	require.Contains(t, stripANSI(buf.String()), "…", "a clipped cell must say it was clipped")
	require.Contains(t, buf.String(), "next action here")
}

// TestSkillsListJSON_AlwaysAnswersEveryQuestionItCanAnswer, asserted against the
// BYTES rather than the struct that produced them.
func TestSkillsListJSON_AlwaysAnswersEveryQuestionItCanAnswer(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, emitSkillsList(&buf, collectInstalledSkills(t.TempDir(), nil), true))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got), "output was not JSON: %q", buf.String())

	for _, key := range []string{"roots", "skills", "problems"} {
		raw, ok := got[key]
		require.True(t, ok, "the %q key is absent, so a reader cannot tell empty from unreported: %v", key, got)
		require.NotNil(t, raw, "%q was null rather than an empty array: %v", key, got)
	}
	require.NotEmpty(t, got["guidance"], "guidance must travel in the payload for the agents that read it")
}

// TestSkillsListGuidance_NamesCommandsThatExist: promising a command that does
// not exist is worse than silence.
func TestSkillsListGuidance_NamesCommandsThatExist(t *testing.T) {
	repo := t.TempDir()

	t.Run("nothing selected", func(t *testing.T) {
		requireAdviceResolves(t, collectInstalledSkills(repo, nil).Guidance)
	})
	t.Run("selected but empty", func(t *testing.T) {
		requireAdviceResolves(t, collectInstalledSkills(repo, []string{".claude/skills"}).Guidance)
	})
	t.Run("populated", func(t *testing.T) {
		writeSkillDir(t, repo, ".claude/skills", "mine", manifestWithDescription("mine", "d"))
		got := collectInstalledSkills(repo, []string{".claude/skills"})
		requireAdviceResolves(t, got.Guidance)
		require.Contains(t, got.Guidance, "1 skill installed", "the count is wrong or unpluralized: %q", got.Guidance)
	})
}
