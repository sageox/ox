package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure half of HTTP-only skill and rule materialization: what reaches
// disk, and what prime is told, when a write cannot complete.

// requireNoSkillStages fails if a stage or swap-aside directory survived a
// materialization. Discovery never walks them, but each one is a full copy of a
// skill left on disk until some later prime happens to clear it.
func requireNoSkillStages(t *testing.T, teamCtxDir string) {
	t.Helper()
	entries, err := os.ReadDir(teamCtxDir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), httpSkillStagePrefix),
			"stage directory %s survived the materialization", entry.Name())
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// A crashed prime's half-written stage is cleared, and a skill the server lists
// twice is written once — from its first listing — never last-writer-wins.
func TestMaterializeTeamSkills_ClearsCrashedStagesAndRefusesADuplicate(t *testing.T) {
	t.Parallel()
	teamCtxDir := t.TempDir()
	writeFixture(t, filepath.Join(teamCtxDir, httpSkillStagePrefix+"123", "SKILL.md"), "half-written")

	duplicate := proseSkill()
	duplicate.Files = []api.TeamContextSkillFile{{Path: "SKILL.md", Content: "---\nname: lint-guide\n---\n\nThe second listing.\n"}}
	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Skills: []api.TeamContextSkill{proseSkill(), duplicate},
	}))

	requireNoSkillStages(t, teamCtxDir)
	assert.Equal(t, proseSkillManifest,
		mustReadString(t, filepath.Join(teamCtxDir, "agents", "skills", "lint-guide", "SKILL.md")),
		"a skill listed twice must keep its first listing")
}

// A rule the team deletes leaves the environment on the next complete bundle,
// in either rules root and at any depth, and so does a crashed rule write's
// temp file. A non-markdown file under a rules root is not a rule and stays.
// Failure prevented: a deleted rule lived on in every HTTP-primed environment
// and kept being fed to the agent.
func TestMaterializeTeamSkills_RetiresRulesTheServerNoLongerLists(t *testing.T) {
	t.Parallel()
	teamCtxDir := t.TempDir()
	rule := func(path, content string) api.TeamContextRule {
		return api.TeamContextRule{Name: filepath.Base(path), Path: path, Content: content}
	}
	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Rules: []api.TeamContextRule{
			rule("agents/rules/kept.md", "# v1\n"),
			rule("agents/rules/guides/retired.md", "# retired\n"),
			rule("coworkers/rules/legacy.md", "# legacy\n"),
		},
	}))
	writeFixture(t, filepath.Join(teamCtxDir, "agents", "rules", "crashed.md"+httpRuleTempSuffix), "partial")
	writeFixture(t, filepath.Join(teamCtxDir, "agents", "rules", "notes.txt"), "not a rule")

	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Rules: []api.TeamContextRule{rule("agents/rules/kept.md", "# v2\n")},
	}))

	rel := func(p string) string { return filepath.Join(teamCtxDir, filepath.FromSlash(p)) }
	assert.Equal(t, "# v2\n", mustReadString(t, rel("agents/rules/kept.md")))
	assert.NoFileExists(t, rel("agents/rules/guides/retired.md"), "a nested retired rule survived")
	assert.NoFileExists(t, rel("coworkers/rules/legacy.md"), "a retired rule in the legacy root survived")
	assert.NoFileExists(t, rel("agents/rules/crashed.md"+httpRuleTempSuffix), "a crashed rule write's temp file survived")
	assert.FileExists(t, rel("agents/rules/notes.txt"), "a non-rule file under a rules root is not ox's to delete")
}

// Every write that cannot complete is an error prime hears about, and never a
// half-installed skill or a stray temp file on disk. Each case blocks one step
// with a file where a directory must go, or the reverse.
func TestMaterializeTeamSkills_ReportsEveryFailedWrite(t *testing.T) {
	t.Parallel()
	withFiles := func(files ...api.TeamContextSkillFile) api.TeamContextSkill {
		skill := proseSkill()
		skill.Files = append(skill.Files, files...)
		return skill
	}
	styleRule := []api.TeamContextRule{{Name: "style", Path: "agents/rules/style.md", Content: "# style\n"}}
	snapshot := filepath.FromSlash(skillmanager.HTTPSnapshotFile)

	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		resp    *api.TeamContextContentResponse
		wantErr string
		check   func(t *testing.T, dir string)
	}{
		{
			name:    "team-context dir does not exist",
			setup:   func(t *testing.T, dir string) { require.NoError(t, os.Remove(dir)) },
			resp:    &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{proseSkill()}},
			wantErr: "open team-context dir",
			check: func(t *testing.T, dir string) {
				assert.NoDirExists(t, dir, "materialization must not create the team-context dir itself")
			},
		},
		{
			name: "a skill file sits where the skill needs a directory",
			resp: &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{withFiles(
				api.TeamContextSkillFile{Path: "references", Content: "a file"},
				api.TeamContextSkillFile{Path: "references/runbook.md", Content: "nested"},
			)}},
			wantErr: "write team skill agents/skills/lint-guide",
			check: func(t *testing.T, dir string) {
				assert.NoDirExists(t, filepath.Join(dir, "agents", "skills", "lint-guide"))
			},
		},
		{
			name: "a skill directory sits where the skill needs a file",
			resp: &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{withFiles(
				api.TeamContextSkillFile{Path: "references/runbook.md", Content: "nested"},
				api.TeamContextSkillFile{Path: "references", Content: "a file"},
			)}},
			wantErr: "write team skill agents/skills/lint-guide",
			check: func(t *testing.T, dir string) {
				assert.NoDirExists(t, filepath.Join(dir, "agents", "skills", "lint-guide"))
			},
		},
		{
			name:    "the skills root is a file",
			setup:   func(t *testing.T, dir string) { writeFixture(t, filepath.Join(dir, "agents", "skills"), "not a dir") },
			resp:    &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{proseSkill()}},
			wantErr: "write team skill agents/skills/lint-guide",
			check: func(t *testing.T, dir string) {
				assert.Equal(t, "not a dir", mustReadString(t, filepath.Join(dir, "agents", "skills")))
			},
		},
		{
			// With nothing to write, the sweep is the first thing to touch the root.
			name:    "the skills root is a file and the team has no skills",
			setup:   func(t *testing.T, dir string) { writeFixture(t, filepath.Join(dir, "agents", "skills"), "not a dir") },
			resp:    &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{}},
			wantErr: "read agents/skills",
			check: func(t *testing.T, dir string) {
				assert.NoFileExists(t, filepath.Join(dir, snapshot),
					"a skill set that was never swept must not be recorded as the current one")
			},
		},
		{
			name:    "the snapshot marker cannot be written",
			setup:   func(t *testing.T, dir string) { writeFixture(t, filepath.Join(dir, ".sageox"), "not a dir") },
			resp:    &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{proseSkill()}},
			wantErr: ".sageox",
			check: func(t *testing.T, dir string) {
				assert.FileExists(t, filepath.Join(dir, "agents", "skills", "lint-guide", "SKILL.md"),
					"the skill itself was complete before the marker failed")
			},
		},
		{
			name: "a rule directory is blocked by a file",
			setup: func(t *testing.T, dir string) {
				writeFixture(t, filepath.Join(dir, "agents", "rules", "guides"), "not a dir")
			},
			resp:    &api.TeamContextContentResponse{Rules: []api.TeamContextRule{{Path: "agents/rules/guides/style.md", Content: "x"}}},
			wantErr: "write team rule agents/rules/guides/style.md",
		},
		{
			name: "a rule's temp path is a directory",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "rules", "style.md"+httpRuleTempSuffix), 0o755))
			},
			resp:    &api.TeamContextContentResponse{Rules: styleRule},
			wantErr: "write team rule agents/rules/style.md",
			check: func(t *testing.T, dir string) {
				assert.NoFileExists(t, filepath.Join(dir, "agents", "rules", "style.md"))
			},
		},
		{
			name: "a rule's destination is a directory",
			setup: func(t *testing.T, dir string) {
				writeFixture(t, filepath.Join(dir, "agents", "rules", "style.md", "keep.txt"), "x")
			},
			resp:    &api.TeamContextContentResponse{Rules: styleRule},
			wantErr: "write team rule agents/rules/style.md",
			check: func(t *testing.T, dir string) {
				assert.NoFileExists(t, filepath.Join(dir, "agents", "rules", "style.md"+httpRuleTempSuffix),
					"a failed rule rename must not leave its temp file behind")
				assert.FileExists(t, filepath.Join(dir, "agents", "rules", "style.md", "keep.txt"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "team")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			if tt.setup != nil {
				tt.setup(t, dir)
			}

			err := materializeTeamSkillsAndRules(dir, tt.resp)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			if tt.check != nil {
				tt.check(t, dir)
			}
			if _, statErr := os.Stat(dir); statErr == nil {
				requireNoSkillStages(t, dir)
			}
		})
	}
}

// skipUnlessPermissionsBite skips where directory modes do not stop a write:
// Windows ignores them and root bypasses them.
func skipUnlessPermissionsBite(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict this user")
	}
}

// chmodForTest sets mode on path and restores a writable mode at cleanup, so
// t.TempDir can remove the tree.
func chmodForTest(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// A skill that cannot be swapped into place leaves the previous version
// intact and no stage behind; the caller gets the error.
// Failure prevented: a failed swap left a skill half old, half new — or gone —
// for discovery and the approval gate to classify.
func TestMaterializeTeamSkills_AFailedSwapKeepsThePreviousVersion(t *testing.T) {
	t.Parallel()
	skipUnlessPermissionsBite(t)
	edited := proseSkill()
	edited.Files = []api.TeamContextSkillFile{{Path: "SKILL.md", Content: "---\nname: lint-guide\n---\n\nThe new linter.\n"}}

	tests := []struct {
		name    string
		seed    []api.TeamContextSkill // materialized before permissions change
		lock    string                 // team-context-relative dir to restrict
		mode    os.FileMode
		resp    []api.TeamContextSkill
		wantErr string
		check   func(t *testing.T, dir string)
	}{
		{
			name: "no room to stage", lock: ".", mode: 0o555,
			resp: []api.TeamContextSkill{proseSkill()}, wantErr: "stage skill",
			check: func(t *testing.T, dir string) { assert.NoDirExists(t, filepath.Join(dir, "agents")) },
		},
		{
			name: "the existing skill cannot be moved aside", seed: []api.TeamContextSkill{proseSkill()},
			lock: "agents/skills", mode: 0o555,
			resp: []api.TeamContextSkill{edited}, wantErr: "write team skill agents/skills/lint-guide",
			check: func(t *testing.T, dir string) {
				assert.Equal(t, proseSkillManifest, mustReadString(t, filepath.Join(dir, "agents", "skills", "lint-guide", "SKILL.md")),
					"the installed version must survive a failed swap untouched")
			},
		},
		{
			name: "a new skill cannot be placed", seed: []api.TeamContextSkill{scriptSkill()},
			lock: "agents/skills", mode: 0o555,
			resp: []api.TeamContextSkill{proseSkill()}, wantErr: "write team skill agents/skills/lint-guide",
			check: func(t *testing.T, dir string) {
				assert.NoDirExists(t, filepath.Join(dir, "agents", "skills", "lint-guide"))
				assert.DirExists(t, filepath.Join(dir, "agents", "skills", "deploy"),
					"a pass that failed mid-write must not go on to retire anything")
			},
		},
		{
			name: "the skill location cannot be inspected", seed: []api.TeamContextSkill{scriptSkill()},
			lock: "agents/skills", mode: 0o000,
			resp: []api.TeamContextSkill{proseSkill()}, wantErr: "write team skill agents/skills/lint-guide",
		},
		{
			name: "an unlisted skill cannot be retired", seed: []api.TeamContextSkill{scriptSkill()},
			lock: "agents/skills", mode: 0o555,
			resp: []api.TeamContextSkill{}, wantErr: "retire agents/skills/deploy",
			check: func(t *testing.T, dir string) {
				assert.DirExists(t, filepath.Join(dir, "agents", "skills", "deploy"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tt.seed != nil {
				require.NoError(t, materializeTeamSkillsAndRules(dir, &api.TeamContextContentResponse{Skills: tt.seed}))
			}
			marker := filepath.Join(dir, filepath.FromSlash(skillmanager.HTTPSnapshotFile))
			chmodForTest(t, filepath.Join(dir, filepath.FromSlash(tt.lock)), tt.mode)

			err := materializeTeamSkillsAndRules(dir, &api.TeamContextContentResponse{Skills: tt.resp})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			requireNoSkillStages(t, dir)
			assert.NoFileExists(t, marker, "a failed pass must leave no snapshot, so the next prime reconciles instead of trusting a partial tree")
			if tt.check != nil {
				tt.check(t, dir)
			}
		})
	}
}

// A stale rule ox cannot delete is logged and skipped: it must not fail the
// whole bundle, and the rules beside it are still retired.
func TestMaterializeTeamSkills_AnUndeletableStaleRuleIsLoggedNotFatal(t *testing.T) {
	skipUnlessPermissionsBite(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	teamCtxDir := t.TempDir()
	locked := filepath.Join(teamCtxDir, "agents", "rules", "locked")
	writeFixture(t, filepath.Join(locked, "stuck.md"), "# stuck\n")
	writeFixture(t, filepath.Join(teamCtxDir, "agents", "rules", "gone.md"), "# gone\n")
	chmodForTest(t, locked, 0o555)

	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Rules: []api.TeamContextRule{},
	}))

	assert.NoFileExists(t, filepath.Join(teamCtxDir, "agents", "rules", "gone.md"))
	assert.FileExists(t, filepath.Join(locked, "stuck.md"))
	assert.Contains(t, logs.String(), "failed to retire stale rule")
	assert.Contains(t, logs.String(), "path=agents/rules/locked/stuck.md")
}

// When the bundle cannot be fully written, prime still reads whatever reached
// disk, so the fallback must say so at WARN rather than fail silently.
// Failure prevented: an environment primed from half a bundle — docs present,
// skills missing — with nothing in the log to explain why.
func TestTryHTTPTeamContextFallback_WarnsWhenTeamContextIsOnlyPartlyWritten(t *testing.T) {
	h := newEphemeralSkillsHarness(t)
	h.resp.Store(&api.TeamContextContentResponse{
		TeamID: "team_abc", TeamName: "Team ABC", AgentsMD: "team agents",
		Skills: []api.TeamContextSkill{proseSkill()},
	})
	writeFixture(t, filepath.Join(h.teamDir, "agents", "skills"), "not a dir")

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	info, err := tryHTTPTeamContextFallback(context.Background(), h.project, "team_abc")

	require.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "failed to write team context")
	assert.Contains(t, err.Error(), "write team skill agents/skills/lint-guide")
	assert.Equal(t, "team agents", mustReadString(t, filepath.Join(h.teamDir, "AGENTS.md")),
		"docs land before skills, which is why the partial write must be reported")
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "team context only partly written")
	assert.Contains(t, logs.String(), "team_id=team_abc")
	assert.Contains(t, logs.String(), `impact="prime may read a partial team context from disk"`)
}

// A prime whose skill write fails partway must not leave the previous snapshot
// marker behind: the marker is what lets the next prime skip reconciliation, so
// a stale one would freeze a partial skill tree in place.
func TestMaterializeTeamSkills_AFailedWriteInvalidatesTheSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, filepath.FromSlash(skillmanager.HTTPSnapshotFile))
	require.NoError(t, materializeTeamSkillsAndRules(dir, &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{proseSkill()}}))
	require.FileExists(t, marker, "fixture: a successful prime records its snapshot")

	broken := proseSkill()
	broken.Files = append(broken.Files,
		api.TeamContextSkillFile{Path: "references", Content: "a file"},
		api.TeamContextSkillFile{Path: "references/runbook.md", Content: "nested"})
	err := materializeTeamSkillsAndRules(dir, &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{broken}})
	require.Error(t, err)
	require.NoFileExists(t, marker, "a failed prime kept the old snapshot, so the next prime would skip reconciliation")
}
