package skillmanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// claudeorg_test.go — Claude Code must not list a team skill twice when the
// team's Claude organization already delivers it, and the dedupe must never
// cost a coworker a skill.
//
// These tests set XDG_CACHE_HOME (the org sync record is machine state), so
// none of them run in parallel.

const orgSyncTeamID = "team_wiring_test" // stageTeamWiredProject's team

func claudeTarget() adapterprotocol.SkillTarget {
	return adapterprotocol.SkillTarget{
		Key: ClaudeProjectTargetKey, Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
}

// isolateOrgSync gives the test its own cache, so a record written by the
// developer's real daemon can never reach a fixture.
func isolateOrgSync(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// recordOrgSync writes the record the daemon writes after GET /api/v1/cli/repos.
func recordOrgSync(t *testing.T, repo string, at time.Time, synced ...string) {
	t.Helper()
	require.NoError(t, teamskills.RecordClaudeOrgSync(endpoint.GetForProject(repo), []teamskills.ClaudeOrgTeam{{
		TeamID: orgSyncTeamID, TeamSlug: "wiring-test-team", SyncedSkills: synced,
	}}, at))
}

// writeTeamManifest writes a team skill with an exact SKILL.md, for tests where
// the body itself is under test.
func writeTeamManifest(t *testing.T, team, name, manifest string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(team, "agents", "skills", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skills.SkillFileName), []byte(manifest), 0o644))
	for rel, content := range extra {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

func bothTargets() []adapterprotocol.SkillTarget {
	return []adapterprotocol.SkillTarget{claudeTarget(), sharedTarget()}
}

func desiredForTargets(targets []adapterprotocol.SkillTarget) DesiredSkills {
	desired := DesiredSkills{Bundles: []BundleRef{{ID: "core"}}}
	for _, target := range targets {
		desired.Targets = append(desired.Targets, target.Key)
	}
	return desired
}

func reconcileTargets(t *testing.T, repo string, targets []adapterprotocol.SkillTarget) *ReconcilePlan {
	t.Helper()
	plan, err := Reconcile(repo, "1.0.0", desiredForTargets(targets), targets)
	require.NoError(t, err)
	require.Empty(t, plan.Warnings)
	return plan
}

func claudeCopy(repo, name string) string {
	return filepath.Join(repo, ".claude", "skills", name+TeamSuffix, skills.SkillFileName)
}

func agentsCopy(repo, name string) string {
	return filepath.Join(repo, ".agents", "skills", name+TeamSuffix, skills.SkillFileName)
}

func decisionFor(t *testing.T, plan *ReconcilePlan, name string) TeamSkillDecision {
	t.Helper()
	for _, d := range plan.TeamSkills {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no decision recorded for %q", name)
	return TeamSkillDecision{}
}

// stageOrgSyncRepo wires a repo (origin acme/api) to a team that publishes two
// prose skills: grill-me, which the fixture's org delivers, and notes.
func stageOrgSyncRepo(t *testing.T) (repo, team string) {
	t.Helper()
	repo, team = t.TempDir(), t.TempDir()
	writeTeamSkill(t, team, "grill-me", "description: Interview me.\n", nil)
	writeTeamSkill(t, team, "notes", "description: Take notes.\n", map[string]string{"references/style.md": "# Style\n"})
	stageTeamWiredProject(t, repo, team)
	return repo, team
}

func pathsUnder(actions []FileAction, prefix string) []string {
	var out []string
	for _, a := range actions {
		if strings.HasPrefix(a.Path, prefix) {
			out = append(out, a.Path)
		}
	}
	sort.Strings(out)
	return out
}

// --- A. The dedupe itself -------------------------------------------------

// TestClaudeOrgDedupe_DeliveredSkillIsNotCopiedForClaudeCode is the feature:
// the org copy wins in Claude Code, and nothing else moves.
// Failure prevented: Claude Code lists grill-me twice, once as the org
// plugin's skill and once as ox's grill-me-team.
func TestClaudeOrgDedupe_DeliveredSkillIsNotCopiedForClaudeCode(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "grill-me")

	plan := reconcileTargets(t, repo, bothTargets())

	require.NoFileExists(t, claudeCopy(repo, "grill-me"), "ox copied a skill Claude Code already gets from the org")
	require.FileExists(t, agentsCopy(repo, "grill-me"), "Codex and the other .agents/skills readers lost a skill they have no other way to get")
	require.FileExists(t, claudeCopy(repo, "notes"), "a skill the org does NOT deliver was dropped from Claude Code")
	require.FileExists(t, filepath.Join(repo, ".claude", "skills", "ox-cli-plan", skills.SkillFileName),
		"ox's own skills must never be affected by a team's org sync")

	d := decisionFor(t, plan, "grill-me")
	require.True(t, d.ClaudeOrgDelivered)
	require.Equal(t, "grill-me"+TeamSuffix, d.InstalledAs, "the other targets still install it under the usual name")
	require.Contains(t, d.Reason, "Claude organization")
	require.False(t, decisionFor(t, plan, "notes").ClaudeOrgDelivered)
}

// TestClaudeOrgDedupe_RetiresTheCopyAlreadyInClaudeCode: a coworker who had
// ox's copy before the admin connected the org must lose the DUPLICATE, through
// the ordinary retirement path, and keep everything else.
// Failure prevented: the duplicate lives on forever because only new installs
// were suppressed.
func TestClaudeOrgDedupe_RetiresTheCopyAlreadyInClaudeCode(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	reconcileTargets(t, repo, bothTargets())
	require.FileExists(t, claudeCopy(repo, "grill-me"), "fixture: the pre-org copy was never installed")
	agentBefore, err := os.ReadFile(agentsCopy(repo, "grill-me"))
	require.NoError(t, err)

	recordOrgSync(t, repo, time.Now(), "grill-me")
	plan := reconcileTargets(t, repo, bothTargets())

	require.Equal(t, []string{".claude/skills/grill-me-team/SKILL.md"}, pathsUnder(plan.Removes, ".claude/"),
		"exactly the duplicate's files leave Claude Code's directory")
	require.Empty(t, pathsUnder(plan.Removes, ".agents/"), "retirement reached a non-Claude target")
	require.NoFileExists(t, claudeCopy(repo, "grill-me"))
	require.FileExists(t, claudeCopy(repo, "notes"))
	agentAfter, err := os.ReadFile(agentsCopy(repo, "grill-me"))
	require.NoError(t, err)
	require.Equal(t, agentBefore, agentAfter, "the .agents/skills copy changed")

	again := reconcileTargets(t, repo, bothTargets())
	require.Empty(t, again.Creates, "the duplicate came back on the next reconcile")
	require.Empty(t, again.Removes)
}

// TestClaudeOrgDedupe_RetiresWithoutLocalState: .sageox/cache is disposable.
// With the lockfile's machine-local half gone, the ox stamp on the manifest is
// the only ownership record, and retirement must still find the duplicate.
func TestClaudeOrgDedupe_RetiresWithoutLocalState(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	reconcileTargets(t, repo, bothTargets())
	require.NoError(t, os.Remove(StatePath(repo)))

	recordOrgSync(t, repo, time.Now(), "grill-me")
	reconcileTargets(t, repo, bothTargets())

	require.NoFileExists(t, claudeCopy(repo, "grill-me"), "a cache wipe made the duplicate permanent")
	require.FileExists(t, agentsCopy(repo, "grill-me"))
}

// --- B. Blind sources never retire ----------------------------------------

// TestClaudeOrgDedupe_BlindTeamSourceNeverRetires: when ox cannot see the team's
// skills, an absent skill means nothing, so the copy already in Claude Code is
// held — even though the org record says to drop it. A stale skill is
// recoverable; deleting a coworker's skill on a guess is not.
func TestClaudeOrgDedupe_BlindTeamSourceNeverRetires(t *testing.T) {
	for _, tt := range []struct {
		name  string
		blind func(t *testing.T, repo, team string)
	}{
		{"skills directory not materialized", func(t *testing.T, _, team string) {
			require.NoError(t, os.RemoveAll(filepath.Join(team, "agents")))
		}},
		{"team checkout not on disk", func(t *testing.T, _, team string) {
			require.NoError(t, os.RemoveAll(team))
		}},
		{"repository identity unknown", func(t *testing.T, repo, _ string) {
			cmd := exec.Command("git", "remote", "remove", "origin")
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateOrgSync(t)
			repo, team := stageOrgSyncRepo(t)
			reconcileTargets(t, repo, bothTargets())
			require.FileExists(t, claudeCopy(repo, "grill-me"))

			recordOrgSync(t, repo, time.Now(), "grill-me")
			tt.blind(t, repo, team)
			plan := reconcileTargets(t, repo, bothTargets())

			require.NotEmpty(t, plan.RetainedTeamReason(), "fixture: the source was not blind")
			require.Empty(t, pathsUnder(plan.Removes, ".claude/skills/grill-me"),
				"a blind source retired a coworker's skill")
			require.FileExists(t, claudeCopy(repo, "grill-me"))
		})
	}
}

// --- C. Old servers, stale and lagging lists ------------------------------

// TestClaudeOrgDedupe_NoRecordBehavesExactlyAsBefore: no org sync anywhere (the
// common case), or a server that predates the field. Every team skill reaches
// Claude Code, and the recorded revision has no org component, so prime's fast
// path is byte-for-byte what it was.
func TestClaudeOrgDedupe_NoRecordBehavesExactlyAsBefore(t *testing.T) {
	isolateOrgSync(t)
	for _, oldServer := range []bool{false, true} {
		repo, team := stageOrgSyncRepo(t)
		if oldServer {
			recordOrgSync(t, repo, time.Now()) // teams reported, field absent
		}
		plan := reconcileTargets(t, repo, bothTargets())

		require.FileExists(t, claudeCopy(repo, "grill-me"))
		require.FileExists(t, claudeCopy(repo, "notes"))
		for _, d := range plan.TeamSkills {
			require.False(t, d.ClaudeOrgDelivered, d.Name)
			require.Empty(t, d.Reason, d.Name)
		}
		base, err := skills.Digest()
		require.NoError(t, err)
		want := base + "+team:" + teamRevision(team)
		got, err := ExpectedRevision(repo)
		require.NoError(t, err)
		require.Equal(t, want, got, "the revision changed shape for a repository with no org sync")
		recorded, _, _ := InstalledSource(repo)
		require.Equal(t, want, recorded)
	}
}

// TestClaudeOrgDedupe_StaleListNamingAMissingSkillChangesNothing: the server's
// list can name a skill this checkout does not have (deleted since, renamed, or
// published after the last pull). It must match nothing and retire nothing.
func TestClaudeOrgDedupe_StaleListNamingAMissingSkillChangesNothing(t *testing.T) {
	isolateOrgSync(t)
	baseline, _ := stageOrgSyncRepo(t)
	want := reconcileTargets(t, baseline, bothTargets())

	repo, _ := stageOrgSyncRepo(t)
	reconcileTargets(t, repo, bothTargets())
	recordOrgSync(t, repo, time.Now(), "ghost", "grill-me-old")
	plan := reconcileTargets(t, repo, bothTargets())

	require.Empty(t, plan.Removes, "a name that matches nothing retired something")
	require.Empty(t, plan.Creates)
	require.FileExists(t, claudeCopy(repo, "grill-me"))
	require.FileExists(t, claudeCopy(repo, "notes"))
	require.Len(t, plan.TeamSkills, len(want.TeamSkills))
}

// TestClaudeOrgDedupe_SkillDeletedFromTheTeamIsRetiredNormally: the list still
// names grill-me after the team deleted it. The copies leave because the TEAM
// retired the skill, exactly as without org sync, and nothing crashes.
func TestClaudeOrgDedupe_SkillDeletedFromTheTeamIsRetiredNormally(t *testing.T) {
	isolateOrgSync(t)
	repo, team := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "grill-me")
	reconcileTargets(t, repo, bothTargets())
	require.FileExists(t, agentsCopy(repo, "grill-me"))

	require.NoError(t, os.RemoveAll(filepath.Join(team, "agents", "skills", "grill-me")))
	plan := reconcileTargets(t, repo, bothTargets())

	require.NoFileExists(t, agentsCopy(repo, "grill-me"))
	require.FileExists(t, claudeCopy(repo, "notes"), "a stale list cost Claude Code an unrelated skill")
	require.Empty(t, pathsUnder(plan.Removes, ".claude/skills/notes"))
}

// TestClaudeOrgDedupe_ListLaggingTheCheckoutKeepsOxsCopy: the server lists a
// skill as portable, but THIS checkout's version can no longer travel in the
// org plugin (it gained a script, a repos: filter, a shell requirement). The
// plugin still carries the OLD prose; deferring would hand Claude Code a skill
// missing the thing that changed. Ox keeps its copy: a duplicate for one export
// cycle, never a missing skill.
func TestClaudeOrgDedupe_ListLaggingTheCheckoutKeepsOxsCopy(t *testing.T) {
	const fence = "```"
	for _, tt := range []struct {
		name     string
		manifest string
		extra    map[string]string
		deferred bool
		// withheld: the manifest itself became runnable, so the trust gate holds
		// the whole skill for approval before dedupe is ever considered.
		withheld bool
	}{
		{name: "gained a bundled script",
			manifest: "---\nname: grill-me\n---\nbody\n", extra: map[string]string{"scripts/run.sh": "#!/bin/sh\necho hi\n"}},
		{name: "became repo-scoped (this repo included)",
			manifest: "---\nname: grill-me\nrepos: [\"acme/api\"]\n---\nbody\n"},
		{name: "declares requires: shell",
			manifest: "---\nname: grill-me\nrequires: [shell]\n---\nbody\n"},
		{name: "declares an unknown requirement",
			manifest: "---\nname: grill-me\nrequires: [docker]\n---\nbody\n"},
		{name: "declares requires as a block list",
			manifest: "---\nname: grill-me\nrequires:\n  - ox-cli\n---\nbody\n"},
		{name: "declares requires as a zero-indent block list (contract v2 R3)",
			manifest: "---\nname: grill-me\nrequires:\n- shell\n---\nbody\n"},
		{name: "declares requires past line 30 (contract v2 R4)",
			manifest: "---\nname: grill-me\ndescription: >-\n" + strings.Repeat("  words\n", 40) + "requires: [shell]\n---\nbody\n"},
		{name: "gained a mid-line inline command (contract v2 R1)",
			manifest: "---\nname: grill-me\n---\n- Current branch: !`git branch --show-current`\n", withheld: true},
		{name: "gained a grant behind a fence with trailing space (contract v2 R2)",
			manifest: "--- \nname: grill-me\nallowed-tools: Bash(*)\n---\nbody\n", withheld: true},
		{name: "body has a bash fence and no requires",
			manifest: "---\nname: grill-me\n---\n" + fence + "bash\nmake test\n" + fence + "\n"},
		{name: "body runs an ox verb and no requires",
			manifest: "---\nname: grill-me\n---\nRun `ox sync` first.\n"},
		{name: "explicit requires: [] overrides an illustrative fence",
			manifest: "---\nname: grill-me\nrequires: []\n---\n" + fence + "bash\necho example\n" + fence + "\n",
			deferred: true},
		{name: "prose that mentions ox is still portable",
			manifest: "---\nname: grill-me\n---\nox keeps the team in sync; the `ox` CLI is optional here.\n",
			deferred: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateOrgSync(t)
			repo, team := stageOrgSyncRepo(t)
			writeTeamManifest(t, team, "grill-me", tt.manifest, tt.extra)
			recordOrgSync(t, repo, time.Now(), "grill-me")

			plan := reconcileTargets(t, repo, bothTargets())

			d := decisionFor(t, plan, "grill-me")
			require.Equal(t, tt.deferred, d.ClaudeOrgDelivered)
			if tt.withheld {
				require.True(t, d.NeedsApprove, "a runnable manifest was not held for approval")
				require.Empty(t, d.InstalledAs)
				require.NoFileExists(t, claudeCopy(repo, "grill-me"))
				require.NoFileExists(t, agentsCopy(repo, "grill-me"))
				return
			}
			if tt.deferred {
				require.NoFileExists(t, claudeCopy(repo, "grill-me"))
			} else {
				require.FileExists(t, claudeCopy(repo, "grill-me"),
					"ox deferred to an org copy that cannot carry this version of the skill")
			}
		})
	}
}

// TestClaudeOrgDedupe_CaseVariantInTheListDefersNothing: matching is exact.
func TestClaudeOrgDedupe_CaseVariantInTheListDefersNothing(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "Grill-Me", "GRILL-ME", "grill-me.")
	reconcileTargets(t, repo, bothTargets())
	require.FileExists(t, claudeCopy(repo, "grill-me"))
}

// TestClaudeOrgDedupe_StaleRecordStopsDeferring: a record nobody has refreshed
// in a day is not evidence the org still delivers the skill. Ox copies it again.
// Failure prevented: the daemon stops, the admin revokes org sync, and the
// coworker's Claude Code has the skill from nowhere.
func TestClaudeOrgDedupe_StaleRecordStopsDeferring(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "grill-me")
	reconcileTargets(t, repo, bothTargets())
	require.NoFileExists(t, claudeCopy(repo, "grill-me"))

	recordOrgSync(t, repo, time.Now().Add(-teamskills.ClaudeOrgSyncMaxAge-time.Hour), "grill-me")
	reconcileTargets(t, repo, bothTargets())
	require.FileExists(t, claudeCopy(repo, "grill-me"), "a day-old record kept a skill out of Claude Code")
}

// --- D. Other adapters, and the prime fast path ----------------------------

// TestClaudeOrgDedupe_NonClaudeTargetsAreUnaffected: Codex, Cursor and every
// other adapter read .agents/skills, where no org plugin lands. Their plan must
// be identical with and without an org record.
func TestClaudeOrgDedupe_NonClaudeTargetsAreUnaffected(t *testing.T) {
	isolateOrgSync(t)
	agentsOnly := []adapterprotocol.SkillTarget{sharedTarget()}

	baseline, _ := stageOrgSyncRepo(t)
	want := reconcileTargets(t, baseline, agentsOnly)

	repo, _ := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "grill-me", "notes")
	got := reconcileTargets(t, repo, agentsOnly)

	require.Equal(t, want.WrittenPaths(), got.WrittenPaths())
	require.Equal(t, want.RemovedPaths(), got.RemovedPaths())
	require.Equal(t, want.DesiredFileCount, got.DesiredFileCount)
	for _, name := range []string{"grill-me", "notes"} {
		wantBytes, err := os.ReadFile(agentsCopy(baseline, name))
		require.NoError(t, err)
		gotBytes, err := os.ReadFile(agentsCopy(repo, name))
		require.NoError(t, err)
		require.Equal(t, wantBytes, gotBytes, name)
	}
	require.NoDirExists(t, filepath.Join(repo, ".claude"))
}

// TestClaudeOrgDedupe_PrimeFastPathTracksTheList: prime skips planning when the
// recorded revision equals ExpectedRevision. With an org record they must still
// agree after a reconcile (or prime re-plans every session), and must DISAGREE
// once the delivered set changes (or the change waits for an unrelated commit).
func TestClaudeOrgDedupe_PrimeFastPathTracksTheList(t *testing.T) {
	isolateOrgSync(t)
	repo, _ := stageOrgSyncRepo(t)
	recordOrgSync(t, repo, time.Now(), "grill-me")
	reconcileTargets(t, repo, bothTargets())

	recorded, _, _ := InstalledSource(repo)
	expected, err := ExpectedRevision(repo)
	require.NoError(t, err)
	require.Equal(t, expected, recorded, "prime would re-plan on every session start")
	require.Contains(t, expected, "+claude-org:")

	recordOrgSync(t, repo, time.Now(), "grill-me", "notes")
	moved, err := ExpectedRevision(repo)
	require.NoError(t, err)
	require.NotEqual(t, recorded, moved, "a new org-delivered skill would not be deduplicated until an unrelated team commit")

	reconcileTargets(t, repo, bothTargets())
	require.NoFileExists(t, claudeCopy(repo, "notes"))
	recorded, _, _ = InstalledSource(repo)
	require.Equal(t, moved, recorded)
}
