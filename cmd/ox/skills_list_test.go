package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/sageox/agentx"
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
	// An ordinary, unprefixed name — the shape an ox catalog skill takes — but
	// carrying NO ownership evidence. A catalog-shaped NAME is availability, not
	// provenance: this is deliberately a local skill until a verified install
	// stamp proves ox wrote these bytes.
	writeSkillDir(t, repo, root, unprefixedSkillName, manifestWithDescription(unprefixedSkillName, "curated facts"))

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
	require.Equal(t, provenanceLocal, byName[unprefixedSkillName].Provenance,
		"an unprefixed name alone claimed a hand-authored skill")

	require.Equal(t, "mine, hand written", byName["my-own-skill"].Description,
		"the description column is empty, so a reader cannot tell what a local skill is for")
	require.Empty(t, got.Problems)
}

// TestCollectInstalledSkills_UnprefixedOxSkillNeedsOwnershipEvidence proves the
// other side of the catalog-name boundary: an older repository-scoped install is
// still attributable after local state is lost because its verified in-band
// stamp proves who wrote the bytes.
func TestCollectInstalledSkills_UnprefixedOxSkillNeedsOwnershipEvidence(t *testing.T) {
	repo := t.TempDir()
	const root = ".agents/skills"
	preamble := "---\nname: " + unprefixedSkillName + "\ndescription: curated facts\n---\n"
	body := []byte("\nmanaged body\n")
	manifest := preamble + string(agentx.StampedContent(body, "0.16.0", "ox"))
	writeSkillDir(t, repo, root, unprefixedSkillName, manifest)

	got := collectInstalledSkills(repo, []string{root})

	require.Len(t, got.Skills, 1)
	require.Equal(t, provenanceOx, got.Skills[0].Provenance)
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

// TestCollectInstalledSkills_WillNotReadThroughARootOutsideTheRepository.
//
// The customer failure this prevents: `ox skills list` in a repository someone
// else authored reads, and PRINTS, SKILL.md files from somewhere else on the
// reader's machine. A skill root is repository-controlled — it comes out of the
// committed lockfile — and a repository can ship that root as a symlink. Only
// the root STRING is validated anywhere, so the escape happens at resolution
// time, after every check has already passed.
func TestCollectInstalledSkills_WillNotReadThroughARootOutsideTheRepository(t *testing.T) {
	const stolenDescription = "secrets from outside the repository"

	// plantEscapingRoot returns a repo whose only selected skill root is a
	// symlink to a directory outside it holding a real, readable skill.
	plantEscapingRoot := func(t *testing.T, absolute bool) string {
		t.Helper()
		base := t.TempDir()
		repo, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "stolen"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "stolen", "SKILL.md"),
			[]byte(manifestWithDescription("stolen", stolenDescription)), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude"), 0o755))

		target := outside
		if !absolute {
			// The realistic shape: a relative link a repository can actually commit.
			target = filepath.Join("..", "..", "outside")
		}
		require.NoError(t, os.Symlink(target, filepath.Join(repo, ".claude", "skills")))
		return repo
	}

	for _, tc := range []struct {
		name     string
		absolute bool
	}{
		{name: "absolute symlink", absolute: true},
		{name: "relative symlink", absolute: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectInstalledSkills(plantEscapingRoot(t, tc.absolute), []string{".claude/skills"})

			require.Empty(t, got.Skills,
				"ox followed a skill root out of the repository and inventoried what it found there")
			require.Len(t, got.Problems, 1,
				"a root ox refuses to read must be reported: refusing silently renders the same as an empty root")

			for _, asJSON := range []bool{false, true} {
				var buf strings.Builder
				require.NoError(t, emitSkillsList(&buf, got, asJSON, false))
				require.NotContains(t, buf.String(), stolenDescription,
					"json=%v: a SKILL.md description from outside the repository reached the reader", asJSON)
				require.NotContains(t, buf.String(), "stolen",
					"json=%v: a skill name from outside the repository reached the reader", asJSON)
			}
		})
	}
}

// TestEmitSkillsList_RepositoryControlledTextCannotForgeTerminalOutput.
//
// The customer failure this prevents: running `ox skills list` in a repository
// someone else authored lets that repository write arbitrary escape sequences
// to the terminal. A skill directory name and a lockfile skill root are both
// chosen by the repository, and both are printed verbatim. On a POSIX terminal
// an embedded OSC can rewrite the window title or push text into the clipboard,
// and a CSI can erase and reforge the rest of the row — so the table stops being
// evidence of what is installed.
func TestEmitSkillsList_RepositoryControlledTextCannotForgeTerminalOutput(t *testing.T) {
	// An OSC (title set, BEL-terminated) followed by a CSI (erase line): between
	// them, everything a hostile name needs to both act and hide.
	const hostileName = "evil\x1b]0;pwned\x07\x1b[2Kskill"

	rowContaining := func(t *testing.T, rendered, needle string) string {
		t.Helper()
		for _, line := range strings.Split(rendered, "\n") {
			if strings.Contains(line, needle) {
				return line
			}
		}
		require.FailNowf(t, "row not found",
			"the skill vanished from the table instead of being rendered safely: %q", rendered)
		return ""
	}

	t.Run("a hostile skill directory name is scrubbed at the cell", func(t *testing.T) {
		repo := t.TempDir()
		writeSkillDir(t, repo, ".claude/skills", hostileName, manifestWithDescription("evil", "looks harmless"))

		got := collectInstalledSkills(repo, []string{".claude/skills"})

		require.Len(t, got.Skills, 1)
		require.Equal(t, hostileName, got.Skills[0].Name,
			"the STORED name must stay the real directory name: it is a map key here and is what "+
				"publish matches a user's argument against")

		var buf strings.Builder
		require.NoError(t, emitSkillsList(&buf, got, false, false))

		row := rowContaining(t, buf.String(), "evil")
		for _, r := range row {
			require.True(t, unicode.IsPrint(r),
				"the rendered row carries control byte %q, so the repository can forge terminal output: %q", r, row)
		}
		// The inert remains of the sequences stay: sanitizeCell drops the bytes a
		// terminal ACTS on and keeps everything a reader can see, so the row still
		// identifies the directory rather than turning into a blank.
		require.Contains(t, row, "evil]0;pwned[2Kskill",
			"the row must still name the skill — only the control bytes should be gone")
	})

	t.Run("control bytes are removed before the cell is clipped", func(t *testing.T) {
		// Clipping first spends the whole name column on bytes that render as
		// nothing, so the reader sees a name cut far shorter than the column is
		// wide — and the clip can land in the middle of an escape sequence.
		name := strings.Repeat("\x1b", nameColumn) + "visible-name"
		repo := t.TempDir()
		writeSkillDir(t, repo, ".claude/skills", name, manifestWithDescription("v", "d"))

		var buf strings.Builder
		require.NoError(t, emitSkillsList(&buf, collectInstalledSkills(repo, []string{".claude/skills"}), false, false))

		require.Contains(t, rowContaining(t, buf.String(), "visible"), "visible-name",
			"the name was clipped before it was sanitized, so the column budget went to invisible bytes")
	})

	t.Run("a hostile skill root cannot forge the problem line", func(t *testing.T) {
		repo := t.TempDir()
		root := "evil\x1b]0;pwned\x07root"
		// A regular file where a directory belongs: the existing "root ox cannot
		// read" path, reached with a repository-controlled name.
		require.NoError(t, os.WriteFile(filepath.Join(repo, root), []byte("not a directory"), 0o644))

		got := collectInstalledSkills(repo, []string{root})

		require.Len(t, got.Problems, 1)
		for _, r := range got.Problems[0] {
			require.True(t, unicode.IsPrint(r) || r == ' ',
				"the problem line carries control byte %q: %q", r, got.Problems[0])
		}
	})

	t.Run("a hostile skill root cannot forge the guidance line", func(t *testing.T) {
		// A selected root that was never materialized: no problem, but the guidance
		// names the roots so the reader knows where ox looked.
		got := collectInstalledSkills(t.TempDir(), []string{"evil\x1b]0;pwned\x07/skills"})

		require.Empty(t, got.Problems)
		for _, r := range got.Guidance {
			require.True(t, unicode.IsPrint(r) || r == ' ',
				"the guidance line carries control byte %q: %q", r, got.Guidance)
		}
	})
}

// TestManifestDescription_ReadsTextButReportsShape.
//
// Two callers need different halves of this one read: the listings want the
// TEXT, and `ox skills publish` needs to know the value was a block
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
			// provenanceTeam, not provenanceOx: an uncurated ox skill is hidden
			// by default (see TestEmitSkillsList_CuratesOxSkillsByDefault), which
			// would make this row vanish from the table this test renders and
			// prove nothing about width.
			{Name: "team-skill-review", Provenance: provenanceTeam,
				Description: strings.Repeat("a very long description that keeps going ", 8)},
			{Name: strings.Repeat("long-skill-name-", 6), Provenance: provenanceLocal, Description: "short"},
		},
		Problems: []string{},
		Guidance: "next action here",
	}

	var buf strings.Builder
	require.NoError(t, emitSkillsList(&buf, out, false, false))

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
	require.NoError(t, emitSkillsList(&buf, collectInstalledSkills(t.TempDir(), nil), true, false))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got), "output was not JSON: %q", buf.String())

	for _, key := range []string{"roots", "skills", "problems"} {
		raw, ok := got[key]
		require.True(t, ok, "the %q key is absent, so a reader cannot tell empty from unreported: %v", key, got)
		require.NotNil(t, raw, "%q was null rather than an empty array: %v", key, got)
	}
	require.NotEmpty(t, got["guidance"], "guidance must travel in the payload for the agents that read it")
}

// TestRunSkillsList_DrivesTheRealCommand guards the repository and lockfile
// boundary that the collector tests intentionally bypass.
func TestRunSkillsList_DrivesTheRealCommand(t *testing.T) {
	t.Run("json success", func(t *testing.T) {
		stageInstallRepo(t)
		out, err := runSkillsChange(t, skillsListCmd, "--json")
		require.NoError(t, err)
		var got skillsListOutput
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		require.NotEmpty(t, got.Roots)
		require.NotEmpty(t, got.Skills)
	})

	t.Run("outside a repository", func(t *testing.T) {
		chdirOutsideGit(t)
		_, err := runSkillsChange(t, skillsListCmd)
		require.ErrorContains(t, err, "not inside a git repository")
	})

	t.Run("unreadable selection", func(t *testing.T) {
		repo := stageInstallRepo(t)
		require.NoError(t, os.WriteFile(skillmanager.LockPath(repo), []byte("{broken"), 0o644))
		_, err := runSkillsChange(t, skillsListCmd)
		require.Error(t, err)
	})
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

func TestSkillsListHelpers_CoverDefensiveAndFormattingBoundaries(t *testing.T) {
	t.Run("an invalid repository root becomes a reported problem", func(t *testing.T) {
		rows, problems := inventorySkillRoots(filepath.Join(t.TempDir(), "missing"), []string{".agents/skills"})
		require.Empty(t, rows)
		require.Len(t, problems, 1)
		require.Contains(t, problems[0], "could not open this repository")
	})

	t.Run("mixed ownership across roots is conservatively local", func(t *testing.T) {
		repo := t.TempDir()
		const name = "post-cutoff"
		writeSkillDir(t, repo, ".agents/skills", name, manifestWithDescription(name, "local"))
		owned := manifestWithDescription(name, "managed") + string(agentx.StampedContent([]byte("managed body"), "0.16.0", "ox"))
		writeSkillDir(t, repo, ".claude/skills", name, owned)

		got := collectInstalledSkills(repo, []string{".agents/skills", ".claude/skills"})
		require.Len(t, got.Skills, 1)
		require.Equal(t, provenanceLocal, got.Skills[0].Provenance)
		require.Len(t, got.Skills[0].Roots, 2)
	})

	t.Run("renderer separates rows problems and guidance", func(t *testing.T) {
		out := skillsListOutput{
			Skills:   []installedSkillRow{{Name: "mine", Provenance: provenanceLocal, Description: "local skill"}},
			Problems: []string{"one selected directory was unreadable"},
			Guidance: "Run ox doctor to repair it.",
		}
		var buf strings.Builder
		require.NoError(t, emitSkillsList(&buf, out, false, false))
		rendered := stripANSI(buf.String())
		require.Contains(t, rendered, "Directories ox could not read")
		require.Contains(t, rendered, "Run ox doctor")
		require.Contains(t, rendered, "\n\n")
	})

	t.Run("manifest and cell helpers stop at their exact boundaries", func(t *testing.T) {
		description, folded := manifestDescription([]byte("---\ndescription: >-\n  first line\nnext: key\n  ignored\n---\n"))
		require.Equal(t, "first line", description)
		require.True(t, folded)
		require.Equal(t, "a b", sanitizeCell("a\tb"))
		require.Equal(t, "a", truncateCell("abc", 1))
		require.Equal(t, []string{"a", "b"}, dedupeNames([]string{"a", "a", "b"}))
	})

	t.Run("missing skill directory is not a skill", func(t *testing.T) {
		repo, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		defer func() { require.NoError(t, repo.Close()) }()

		description, owned, isSkill := skillManifestDescription(repo, "missing")
		require.Empty(t, description)
		require.False(t, owned)
		require.False(t, isSkill)
	})
}

// TestSkillsList_AnUnreadableManifestCostsTheDescriptionNotTheRow.
//
// A regular SKILL.md is what makes a directory a skill, so once the listing has
// seen one, every later failure must cost the DESCRIPTION and never the row.
// Dropping the skill instead would tell a coworker their skill is not installed
// — over a permission bit — and send them to reinstall something already there.
func TestSkillsList_AnUnreadableManifestCostsTheDescriptionNotTheRow(t *testing.T) {
	// Chmod is the only lever that reaches this branch: a non-regular SKILL.md is
	// refused earlier, at the Lstat, so it exercises a different path entirely.
	// Windows maps only the read-only bit, so the file would stay readable and
	// this test would pass while asserting nothing.
	if runtime.GOOS == "windows" {
		t.Skip("Chmod(0o000) does not remove read permission on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file, so the permission branch is unreachable")
	}
	repo := t.TempDir()
	dir := writeSkillDir(t, repo, approvalTargetRoot, "unreadable",
		manifestWithDescription("unreadable", "you will never see this"))
	manifest := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.Chmod(manifest, 0o000))
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o644) })

	got := collectInstalledSkills(repo, []string{approvalTargetRoot})

	require.Len(t, got.Skills, 1, "the skill was dropped from the listing over a permission bit")
	require.Equal(t, "unreadable", got.Skills[0].Name)
	require.Empty(t, got.Skills[0].Description, "an unreadable manifest cannot yield a description")
}

// TestResolveSkillRoots_FallsBackToDetectionOnlyWhenTheLockfileIsSilent.
//
// The committed lockfile is the authority, because it records what `ox init`
// selected. Adapter detection shells out to whatever ox-adapter-* binaries are
// on PATH, so it is a property of the developer's MACHINE, not of the
// repository — it is the fallback for a repo that never ran `ox init`, and
// nothing more.
//
// Deliberately asserts the contract and not a specific root: which adapters are
// installed differs between a laptop and CI, and a test that pinned that would
// fail for a reason that has nothing to do with this code.
func TestResolveSkillRoots_FallsBackToDetectionOnlyWhenTheLockfileIsSilent(t *testing.T) {
	t.Run("lockfile wins and detection never runs", func(t *testing.T) {
		repo := stageInstallRepo(t)
		roots, err := resolveSkillRoots(repo)
		require.NoError(t, err)
		require.Contains(t, roots, approvalTargetRoot,
			"the committed selection must be what this command reports")
	})

	t.Run("no selection falls through to detection without failing", func(t *testing.T) {
		roots, err := resolveSkillRoots(t.TempDir())
		require.NoError(t, err, "a repository that never ran `ox init` is not an error")
		require.Equal(t, dedupeNames(roots), roots, "the fallback must still be deduplicated")
	})
}
