package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/stretchr/testify/require"
)

// skills_claude_org_test.go — what a coworker is told about team skills their
// Claude organization delivers, and the publish-time nudge that keeps a skill's
// runtime explicit.

// --- A. `ox skills publish` warns about undeclared runtimes ---------------

// TestPublishRuntimeWarning names exactly the skills SageOx will keep to coding
// agents by inference, and nothing else.
// Failure prevented: an author publishes a skill that runs `ox sync`, sees
// "Coding agents" in the console with no idea why — or is nagged about a skill
// that merely mentions ox in prose.
func TestPublishRuntimeWarning(t *testing.T) {
	const fence = "```"
	for _, tt := range []struct {
		name     string
		manifest string
		want     []string // substrings; nil means no warning
	}{
		{name: "runs an ox verb", manifest: "---\nname: s\n---\nRun `ox sync` first.\n",
			want: []string{"runs `ox sync`", "requires: [ox-cli]", "requires: []"}},
		{name: "bash fence", manifest: "---\nname: s\n---\n" + fence + "bash\nmake test\n" + fence + "\n",
			want: []string{"shell code block (" + fence + "bash)", "requires: [shell]"}},
		{name: "both", manifest: "---\nname: s\n---\n" + fence + "sh\nox agent prime\n" + fence + "\n",
			want: []string{"runs `ox agent`", "and has a shell code block", "requires: [ox-cli, shell]"}},
		{name: "ox line in an untagged fence", manifest: "---\nname: s\n---\n" + fence + "\n$ ox plan save\n" + fence + "\n",
			want: []string{"runs `ox plan`"}},
		{name: "untagged fence is not flagged", manifest: "---\nname: s\n---\n" + fence + "\nnpm test\n" + fence + "\n"},
		{name: "prose mention of ox is not flagged", manifest: "---\nname: s\n---\nox keeps the team in sync; install the `ox` CLI.\n"},
		{name: "non-shell fence is not flagged", manifest: "---\nname: s\n---\n" + fence + "python\nprint(1)\n" + fence + "\n"},
		{name: "declared requires silences it", manifest: "---\nname: s\nrequires: [ox-cli]\n---\nRun `ox sync`.\n"},
		{name: "explicit requires: [] silences it", manifest: "---\nname: s\nrequires: []\n---\n" + fence + "bash\necho example\n" + fence + "\n"},
		{name: "block-form requires silences it", manifest: "---\nname: s\nrequires:\n  - shell\n---\n" + fence + "bash\nls\n" + fence + "\n"},
		{
			// Contract v2 R3: the PyYAML default style. The author DID declare it;
			// telling them to "add requires:" would be wrong.
			name: "zero-indent block requires silences it", manifest: "---\nname: s\nrequires:\n- shell\n---\n" + fence + "bash\nls\n" + fence + "\n",
		},
		{
			// Contract v2 R4: a long folded description pushes requires: past line 30.
			name:     "requires past line 30 silences it",
			manifest: "---\nname: s\ndescription: >-\n" + strings.Repeat("  words\n", 40) + "requires: [ox-cli]\n---\nRun `ox sync`.\n",
		},
		{name: "fence with trailing space still finds requires", manifest: "--- \nname: s\nrequires: [shell]\n---  \n" + fence + "bash\nls\n" + fence + "\n"},
		{
			// A bare `requires:` declares nothing, so the inference stands and the
			// author still needs telling.
			name: "bare requires: does not silence it", manifest: "---\nname: s\nrequires:\n---\nRun `ox sync`.\n",
			want: []string{"runs `ox sync`"},
		},
		{name: "description mentioning a command is not the body", manifest: "---\nname: s\ndescription: Wraps `ox sync`.\n---\nPlain steps.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := publishRuntimeWarning("s", []byte(tt.manifest))
			if tt.want == nil {
				require.Empty(t, got)
				return
			}
			require.True(t, strings.HasPrefix(got, "s "), "the warning does not name the skill: %q", got)
			for _, want := range tt.want {
				require.Contains(t, got, want)
			}
		})
	}
}

// TestSkillsPublish_WarnsButPublishes drives the real command: the warning
// reaches both renderings, publishing still succeeds, and JSON always carries
// the key so a reader can tell "no warnings" from "this ox does not warn".
func TestSkillsPublish_WarnsButPublishes(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stage := func(name, body string) {
		dir := installedSkillDirPath(repo, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, skills.SkillFileName),
			[]byte("---\nname: "+name+"\ndescription: A skill.\n---\n\n"+body), 0o644))
	}
	stage("sync-first", "Run `ox sync` before you start.\n")
	stage("plain-notes", "Write the notes down.\n")

	out, err := runSkillsChange(t, skillsPublishCmd, "--json", "plain-notes")
	require.NoError(t, err, "output: %s", out)
	require.Contains(t, out, `"warnings": []`, "the warnings key must be present and empty")

	out, err = runSkillsChange(t, skillsPublishCmd, "--json", "sync-first")
	require.NoError(t, err, "a runtime warning blocked the publish: %s", out)
	var got skillsChangeOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Warnings, 1)
	require.Contains(t, got.Warnings[0], "sync-first runs `ox sync`")
	require.FileExists(t, filepath.Join(team, "agents", "skills", "sync-first", skills.SkillFileName))

	// The human rendering must carry it too.
	stage("sync-second", "Then `ox status`.\n")
	human, err := runSkillsChange(t, skillsPublishCmd, "sync-second")
	require.NoError(t, err)
	require.Contains(t, human, "sync-second runs `ox status`")
}

// TestPublishManifest_ByteOrderMark: a manifest saved with a BOM is one the
// Team Context reads with its frontmatter (teamdocs trimBOM). Publish must
// agree, or it refuses a skill ("frontmatter name is \"\"") the team side would
// accept, and its description column reads empty.
func TestPublishManifest_ByteOrderMark(t *testing.T) {
	manifest := []byte("\ufeff---\r\nname: release-check\r\ndescription: Verify a release.\r\n---\r\nRun `ox sync`.\r\n")
	require.NoError(t, validatePublishManifest("release-check",
		[]skills.File{{Path: skills.SkillFileName, Content: manifest}}))
	description, folded := manifestDescription(manifest)
	require.False(t, folded)
	require.Equal(t, "Verify a release.", description)
	require.Contains(t, publishRuntimeWarning("release-check", manifest), "runs `ox sync`")
}

// TestPublishRuntimeWarning_StripsControlBytes: the evidence quoted in the
// warning comes from the skill body, which a teammate wrote. Printed raw, an
// escape sequence in it would repaint the publisher's terminal.
func TestPublishRuntimeWarning_StripsControlBytes(t *testing.T) {
	manifest := []byte("---\nname: s\ndescription: d\n---\nRun `ox sync \x1b[2J\x1b[31mnow`.\n\n```bash\x1b]0;title\x07\nox status\n```\n")
	got := publishRuntimeWarning("s", manifest)
	require.NotEmpty(t, got)
	for _, r := range got {
		require.Truef(t, unicode.IsPrint(r), "warning carries non-printable %U: %q", r, got)
	}
}

// --- B. `ox skills status` and `ox doctor` report sources -----------------

// recordClaudeOrg writes what the daemon records after GET /api/v1/cli/repos
// for stageApprovalRepo's team.
func recordClaudeOrg(t *testing.T, repo string, at time.Time, synced ...string) {
	t.Helper()
	require.NoError(t, teamskills.RecordClaudeOrgSync(endpoint.GetForProject(repo), []teamskills.ClaudeOrgTeam{{
		TeamID: "team_approve_test", TeamSlug: "approve-test-team", SyncedSkills: synced,
	}}, at))
}

// isolateClaudeOrg gives the test its own org sync cache, Claude Code config
// dir and ox user config, so the developer's real daemon record, ~/.claude
// and skills.claude_org_dedupe never reach a fixture.
func isolateClaudeOrg(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", "")
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
}

// stageClaudeCodeOrgPlugin makes this machine's Claude Code signed in to a
// claude.ai account whose synced approve-test-team plugin carries skillNames,
// in the layout Claude Code writes (see internal/skillmanager/claudeplugin.go).
func stageClaudeCodeOrgPlugin(t *testing.T, skillNames ...string) {
	t.Helper()
	const org, account, slug = "0f8b7a6e-1111-4222-8333-944455556666", "a1b2c3d4-5555-4666-8777-988899990000", "approve-test-team"
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	require.NotEmpty(t, dir, "call isolateClaudeOrg first")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"oauthAccount":{"organizationUuid":"`+org+`","accountUuid":"`+account+`"}}`), 0o600))
	bucket := filepath.Join(dir, "plugins", "synced", org+"_"+account)
	require.NoError(t, os.MkdirAll(filepath.Join(bucket, slug), 0o755))
	for _, name := range skillNames {
		skillDir := filepath.Join(bucket, slug, "skills", name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, skills.SkillFileName), []byte("---\nname: "+name+"\n---\nbody\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(bucket, slug+".meta.json"), []byte(`{"marketplace_name":"sageox-`+slug+`"}`), 0o644))
}

// TestSkillsStatus_NoOrgSyncLooksExactlyAsBefore: with no record (old server,
// or nobody connected an org) there is no new section and no new JSON key.
func TestSkillsStatus_NoOrgSyncLooksExactlyAsBefore(t *testing.T) {
	isolateClaudeOrg(t)
	repo, _ := stageApprovalRepo(t, "grill-me", nil)

	out := collectSkillsStatus(repo)
	require.Nil(t, out.ClaudeOrg)
	wire, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(wire), "claude_org")
	var human strings.Builder
	renderSkillsStatus(&human, out)
	require.NotContains(t, human.String(), "claude org")
	require.NotContains(t, human.String(), "Sources")
}

// TestSkillsStatus_ReportsWhereClaudeCodeGetsEachSkill walks the whole life of a
// coworker's repo through an org connection: the duplicate exists, status says
// so and how to clear it, the reconcile clears it, and status then says the org
// is the source — without calling the deferred skill "pending" forever.
func TestSkillsStatus_ReportsWhereClaudeCodeGetsEachSkill(t *testing.T) {
	isolateClaudeOrg(t)
	repo, team := stageApprovalRepo(t, "grill-me", nil)
	writeTeamSkillFiles(t, team, "notes", nil)
	_, err := reconcileExactSelectedSkills(repo)
	require.NoError(t, err)
	recordClaudeOrg(t, repo, time.Now(), "grill-me")
	stageClaudeCodeOrgPlugin(t, "grill-me")

	before := collectSkillsStatus(repo)
	require.NotNil(t, before.ClaudeOrg)
	require.Equal(t, []string{"grill-me"}, before.ClaudeOrg.Duplicates,
		"ox's copy is still on disk beside the org's: Claude Code lists it twice right now")
	require.Contains(t, strings.Join(before.Problems, "\n"), "twice until ox removes its own copies")

	_, err = reconcileExactSelectedSkills(repo)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(installedSkillDir(repo, "grill-me"), skills.SkillFileName))

	out := collectSkillsStatus(repo)
	require.Equal(t, &claudeOrgStatus{
		Connected: true, PluginHere: true,
		ClaudeCode: "Claude Code here has the approve-test-team plugin from your Claude organization · ox skips the 1 skill it carries",
		TeamSlug:   "approve-test-team", Synced: 1, OxCopies: 1, Withheld: 0,
		Duplicates: []string{}, LastReported: out.ClaudeOrg.LastReported,
	}, out.ClaudeOrg)
	require.NotEmpty(t, out.ClaudeOrg.LastReported)
	for _, row := range out.TeamSkills {
		switch row.Name {
		case "grill-me":
			require.True(t, row.ClaudeOrg)
			require.Equal(t, skillFromClaudeOrg, row.State, "a skill the org delivers was reported %q", row.State)
			require.Contains(t, row.Detail, "approve-test-team")
		case "notes":
			require.False(t, row.ClaudeOrg)
			require.Equal(t, skillInstalled, row.State)
		}
	}
	require.Empty(t, out.Problems)
	require.NotContains(t, out.Guidance, "pending")

	var human strings.Builder
	renderSkillsStatus(&human, out)
	text := human.String()
	require.Contains(t, text, "claude org sync connected · 1 skill arrives as approve-test-team")
	require.Contains(t, text, "claude code  Claude Code here has the approve-test-team plugin")
	require.Contains(t, text, "ox copies    1 skill")
	require.Contains(t, text, "duplicates   none")

	wire, err := json.Marshal(out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(wire, &payload))
	org, ok := payload["claude_org"].(map[string]any)
	require.True(t, ok, "no claude_org object in JSON: %s", wire)
	require.Equal(t, float64(1), org["synced"])
	require.Equal(t, float64(1), org["ox_copies"])
	require.Equal(t, []any{}, org["duplicates"])
}

// TestSkillsStatus_StaleOrgRecordSaysWhy: when ox stops believing the record,
// it copies everything again — and the coworker must be told why Claude Code
// may now list skills twice, rather than see a silent regression.
func TestSkillsStatus_StaleOrgRecordSaysWhy(t *testing.T) {
	isolateClaudeOrg(t)
	repo, _ := stageApprovalRepo(t, "grill-me", nil)
	recordClaudeOrg(t, repo, time.Now().Add(-48*time.Hour), "grill-me")
	stageClaudeCodeOrgPlugin(t, "grill-me")

	out := collectSkillsStatus(repo)
	require.NotNil(t, out.ClaudeOrg)
	require.False(t, out.ClaudeOrg.Connected)
	require.Equal(t, []string{"grill-me"}, out.ClaudeOrg.Duplicates)
	require.Contains(t, strings.Join(out.Problems, "\n"), "last confirmed 2d ago")

	var human strings.Builder
	renderSkillsStatus(&human, out)
	require.Contains(t, human.String(), "claude org sync not confirmed recently")
}

// TestSkillsStatus_CorruptOrgRecordIsAProblem: an unreadable record must not
// read as "no org" — the coworker would never learn why the dedupe stopped.
func TestSkillsStatus_CorruptOrgRecordIsAProblem(t *testing.T) {
	isolateClaudeOrg(t)
	repo, _ := stageApprovalRepo(t, "grill-me", nil)
	recordClaudeOrg(t, repo, time.Now(), "grill-me")
	file := filepathOfOrgRecord(repo)
	require.NoError(t, os.WriteFile(file, []byte("{ truncated"), 0o600))

	out := collectSkillsStatus(repo)
	require.Nil(t, out.ClaudeOrg)
	require.Contains(t, strings.Join(out.Problems, "\n"), "could not read its record of the skills your Claude organization delivers")
}

func filepathOfOrgRecord(repo string) string {
	return filepath.Join(os.Getenv("XDG_CACHE_HOME"), "sageox", endpoint.NormalizeSlug(endpoint.GetForProject(repo)), "claude-org-sync.json")
}

// TestCheckClaudeSkills_ReportsClaudeOrgSources: `ox doctor` names the split so
// a coworker who sees fewer -team skills in .claude/skills learns why, and says
// nothing new when there is no org sync.
func TestCheckClaudeSkills_ReportsClaudeOrgSources(t *testing.T) {
	isolateClaudeOrg(t)
	repo, team := stageApprovalRepo(t, "grill-me", nil)
	writeTeamSkillFiles(t, team, "notes", nil)
	_, err := reconcileExactSelectedSkills(repo)
	require.NoError(t, err)

	plain := checkClaudeSkills(false)
	require.True(t, plain.passed, "fixture: %+v", plain)
	require.NotContains(t, plain.message, "claude org")

	recordClaudeOrg(t, repo, time.Now(), "grill-me")
	stageClaudeCodeOrgPlugin(t, "grill-me")
	fixed := checkClaudeSkills(true)
	require.True(t, fixed.passed, "%+v", fixed)
	require.Contains(t, fixed.message, "claude org sync connected · 1 skill arrives as approve-test-team · ox copies 1 skill")

	again := checkClaudeSkills(false)
	require.True(t, again.passed, "%+v", again)
	require.Contains(t, again.message, "ox copies 1 skill")
}

// TestSkillsStatus_NoPluginHereSaysOxKeepsCopying: the org delivers grill-me,
// but this coworker's Claude Code does not have the plugin (bead 13.3). Status
// and doctor must each say, in one line, that ox still copies every team skill
// and why — and must not report duplicates that Claude Code is not showing.
// Failure prevented: the coworker reads "1 skill arrives" and goes looking
// for a plugin that will never appear here, or deletes ox's copy by hand.
func TestSkillsStatus_NoPluginHereSaysOxKeepsCopying(t *testing.T) {
	isolateClaudeOrg(t)
	repo, _ := stageApprovalRepo(t, "grill-me", nil)
	recordClaudeOrg(t, repo, time.Now(), "grill-me")
	_, err := reconcileExactSelectedSkills(repo)
	require.NoError(t, err)

	out := collectSkillsStatus(repo)
	require.NotNil(t, out.ClaudeOrg)
	require.True(t, out.ClaudeOrg.Connected, "the server's report is current; only this machine lacks the plugin")
	require.False(t, out.ClaudeOrg.PluginHere)
	require.Equal(t, "ox copies every team skill: Claude Code is not signed in to a claude.ai account here", out.ClaudeOrg.ClaudeCode)
	require.Empty(t, out.ClaudeOrg.Duplicates, "nothing is listed twice when Claude Code has no org plugin")
	require.Empty(t, out.Problems)
	for _, row := range out.TeamSkills {
		if row.Name == "grill-me" {
			require.False(t, row.ClaudeOrg)
			require.Equal(t, skillInstalled, row.State)
		}
	}
	require.FileExists(t, filepath.Join(installedSkillDir(repo, "grill-me"), skills.SkillFileName))

	var human strings.Builder
	renderSkillsStatus(&human, out)
	require.Contains(t, human.String(), "claude code  ox copies every team skill: Claude Code is not signed in to a claude.ai account here")

	wire, err := json.Marshal(out)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(wire, &payload))
	org, ok := payload["claude_org"].(map[string]any)
	require.True(t, ok, "no claude_org object in JSON: %s", wire)
	require.Equal(t, false, org["plugin_here"])
	require.Equal(t, out.ClaudeOrg.ClaudeCode, org["claude_code"])

	doctor := checkClaudeSkills(false)
	require.True(t, doctor.passed, "%+v", doctor)
	require.Contains(t, doctor.message, " · claude org sync connected · ox copies every team skill: Claude Code is not signed in to a claude.ai account here")
	require.NotContains(t, doctor.message, "arrives as", "doctor claims the org delivers skills Claude Code here never receives")
}
