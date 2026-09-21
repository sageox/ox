package skillmanager

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// candidateNames is the set `ox skills approve` would offer a human.
func candidateNames(t *testing.T, repo string) []string {
	t.Helper()
	candidates, err := ClassifyTeamSkills(repo)
	require.NoError(t, err)
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		require.NoError(t, c.LoadErr, "fixture skill %s could not be read", c.Name)
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// installedTeamSkills is the set the shipped reconcile path actually put on
// disk, read back from the repository rather than from the plan — the plan is
// one of the two things under comparison here.
func installedTeamSkills(t *testing.T, repo string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo, ".agents", "skills"))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if rest, ok := strings.CutPrefix(e.Name(), TeamPrefix); ok {
			names = append(names, rest)
		}
	}
	sort.Strings(names)
	return names
}

// TestClassifyTeamSkills_AgreesWithTheReconcilePathWithoutAnOrigin is the
// degraded sibling of TestClassifyTeamSkills_AgreesWithTheReconcilePath.
//
// That test stages a repository with a canonical origin, because
// stageTeamWiredProject supplies one so fixtures "do not accidentally exercise
// the degraded directory-name slug fallback." The bug the agreement test exists
// to catch lived ONLY in that fallback — approval derived the slug one way and
// the reconcile path derived it another — so the guard and the defect were
// staged never to meet. This runs the same agreement with the origin removed.
//
// The failure it prevents: in an origin-less checkout, `ox skills approve`
// offers a repos:-targeted skill that the reconcile path will never install, so
// the human approves something that cannot arrive and nothing anywhere says
// why. The repository directory is named `api` and the targeted skill lists
// both "api" and "acme/api", so a slug derived from the directory NAME would
// classify it while the origin-derived identity does not — the two halves would
// visibly disagree rather than both quietly answering "no".
func TestClassifyTeamSkills_AgreesWithTheReconcilePathWithoutAnOrigin(t *testing.T) {
	t.Parallel()

	// The directory name a fallback slug would produce, and the owner/repo slug
	// the canonical origin produces. Inline list: the block form parses as EMPTY,
	// which means "every repository", i.e. the exact opposite of a filter.
	const dirName = "api"
	const targeted = "api-only"
	const untargeted = "team-wide"

	stage := func(t *testing.T, origin string) string {
		t.Helper()
		repo := filepath.Join(t.TempDir(), dirName)
		require.NoError(t, os.MkdirAll(repo, 0o755))
		team := t.TempDir()
		writeTeamSkill(t, team, targeted, "repos: [\""+dirName+"\", \"acme/"+dirName+"\"]\n", nil)
		// Executable on purpose: an approval has to round-trip through the
		// digest for this to prove the two loaders agree. A prose skill installs
		// with no gate and would prove nothing about the digest.
		writeTeamSkill(t, team, untargeted, "", map[string]string{
			"scripts/run.sh": "#!/bin/sh\necho deploying\n",
		})
		stageTeamWiredProjectWithOrigin(t, repo, team, origin)
		return repo
	}

	t.Run("positive control: with an origin both halves see the targeted skill", func(t *testing.T) {
		t.Parallel()
		repo := stage(t, "https://github.com/acme/"+dirName+".git")

		require.Equal(t, []string{targeted, untargeted}, candidateNames(t, repo),
			"an origin whose slug matches the repos: entry must make the targeted skill approvable")
		reconcileOnce(t, repo)
		require.Equal(t, []string{targeted, untargeted}, installedTeamSkills(t, repo),
			"the reconcile path did not install a skill the approval surface offered")
	})

	t.Run("no origin: both halves must withhold the targeted skill", func(t *testing.T) {
		t.Parallel()
		repo := stage(t, "")

		require.Equal(t, []string{untargeted}, candidateNames(t, repo),
			"approval matched a repos: entry against the working directory's name; the reconcile path resolves an empty slug and would never install it")

		plan := reconcileOnce(t, repo)
		require.Equal(t, []string{untargeted}, installedTeamSkills(t, repo),
			"the reconcile path and the approval surface disagree about which skills this origin-less repository gets")
		require.NotEmpty(t, plan.RetainedTeamReason(),
			"an unresolvable slug was treated as authoritative, so a targeted skill already on disk would be retired")

		// The digest half of the agreement, in the degraded state: a verdict
		// classified by ClassifyTeamSkills must satisfy the reconcile path. If
		// the two loaders collected different files the digests would differ and
		// the script would stay withheld forever with the approval recorded.
		script := filepath.Join(repo, ".agents", "skills", TeamPrefix+untargeted, "scripts", "run.sh")
		require.NoFileExists(t, script, "setup failed: an unapproved script reached the repository")
		require.Len(t, reconcileOnce(t, repo).WithheldTeamSkills(), 1,
			"setup failed: the executable skill was never withheld, so approving it proves nothing")

		approveViaStore(t, repo, untargeted, true)
		require.Empty(t, reconcileOnce(t, repo).WithheldTeamSkills(),
			"a digest from ClassifyTeamSkills did not match the one the reconcile path computes when the repository has no origin")
		require.FileExists(t, script,
			"the approval was recorded and satisfied, but the script the human approved never landed")
	})
}

// TestCatalogForRepo_InstallsOnlyThisProjectsTeamAmongSeveral is the degraded
// sibling of the "exactly one team context" shape every fixture in this package
// stages.
//
// stageTeamWiredProject writes a single TeamContext whose TeamID matches the
// project's, so FindRepoTeamContext's team_id match is never actually asked to
// choose. catalogForRepo's own comment states the promise this pins — it "never
// guesses a cross-team context, so a machine with several teams cannot leak one
// team's skills into another team's repository" — and nothing in this package
// has been in a position to observe it.
//
// The failure it prevents: a consultant, or anyone on two teams, gets another
// team's skills materialized into this repository, committed-adjacent and
// executable by a coding agent. Both halves are checked, because an approval
// surface that offers the other team's skill is the same leak one step earlier.
func TestCatalogForRepo_InstallsOnlyThisProjectsTeamAmongSeveral(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	mine := t.TempDir()
	theirs := t.TempDir()
	writeTeamSkill(t, mine, "deploy", "", nil)
	writeTeamSkill(t, theirs, "other-team-runbook", "", nil)

	// Wire the git identity through the shared helper, then overwrite the
	// configs with a two-team local config the helper cannot express.
	stageTeamWiredProject(t, repo, mine)
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		ProjectID: "proj_two_teams", WorkspaceID: "ws_two_teams",
		TeamID: "team_mine", TeamName: "Mine",
	}))
	require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			// Listed first, so a resolver that takes the head of the list rather
			// than matching team_id picks the wrong one.
			{TeamID: "team_theirs", TeamName: "Theirs", Slug: "theirs", Path: theirs},
			{TeamID: "team_mine", TeamName: "Mine", Slug: "mine", Path: mine},
		},
	}))

	reconcileOnce(t, repo)
	require.Equal(t, []string{"deploy"}, installedTeamSkills(t, repo),
		"another team's skill was materialized into this repository")
	require.Equal(t, []string{"deploy"}, candidateNames(t, repo),
		"the approval surface offered a skill belonging to a team this project is not on")
}

// TestReconcile_TeamSkillReachesEveryTargetAndLeavesNoneBehind covers the other
// count every fixture holds at one: the number of skill targets.
//
// sharedTarget/desiredFor stage exactly one target, so a real repository with
// both an .agents and a .claude skill root has never been reconciled here. The
// failure it prevents is asymmetric retirement: a team skill that installs into
// two roots but is retired from only one leaves an orphan the team believes it
// deleted, still loadable by whichever agent reads that root.
func TestReconcile_TeamSkillReachesEveryTargetAndLeavesNoneBehind(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	team := t.TempDir()
	writeTeamSkill(t, team, "deploy", "", nil)
	stageTeamWiredProject(t, repo, team)

	agents := sharedTarget()
	claude := adapterprotocol.SkillTarget{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	targets := []adapterprotocol.SkillTarget{agents, claude}
	desired := DesiredSkills{Bundles: []BundleRef{{ID: "core"}}, Targets: []string{agents.Key, claude.Key}}

	_, err := Reconcile(repo, "1.0.0", desired, targets)
	require.NoError(t, err)
	manifests := []string{
		filepath.Join(repo, ".agents", "skills", TeamPrefix+"deploy", "SKILL.md"),
		filepath.Join(repo, ".claude", "skills", TeamPrefix+"deploy", "SKILL.md"),
	}
	for _, m := range manifests {
		require.FileExists(t, m, "a team skill reached only some of the configured targets")
	}

	// The team retires it. agents/skills/ is still readable, so this is an
	// authoritative deletion, not blindness.
	require.NoError(t, os.RemoveAll(filepath.Join(team, "agents", "skills", "deploy")))
	plan, err := Reconcile(repo, "1.0.0", desired, targets)
	require.NoError(t, err)
	require.Empty(t, plan.RetainedTeamReason(),
		"a readable checkout was misreported as blind, so this asserts nothing about retirement")
	for _, m := range manifests {
		require.NoFileExists(t, m,
			"a retired team skill survived in one target root; the team deleted it and one agent still loads it")
	}
}

// TestClassifyTeamSkills_CorruptApprovalsAreNotItsConcern pins the one asymmetry
// between the approval surface and the reconcile path that is deliberate.
//
// The reconcile path refuses to act on an unreadable approval store, because
// guessing "empty" there both materializes unapproved content and retires what
// is already installed. ClassifyTeamSkills never reads the store at all — it
// reports what the bytes in the team checkout ARE, which is exactly the
// information a human needs in order to repair the store. If it ever started
// failing in sympathy, a corrupt approvals file would leave the human with no
// surface able to tell them what they are approving.
func TestClassifyTeamSkills_CorruptApprovalsAreNotItsConcern(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	team := t.TempDir()
	writeTeamSkill(t, team, "deploy", "", map[string]string{"scripts/run.sh": "#!/bin/sh\n"})
	stageTeamWiredProject(t, repo, team)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(teamskills.ApprovalPath(repo), []byte("{ truncated"), 0o644))

	require.Equal(t, []string{"deploy"}, candidateNames(t, repo),
		"a corrupt approval store blinded the only surface that can tell a human what needs approving")

	target := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.ErrorContains(t, err, "refusing",
		"positive control: the reconcile path must still refuse the same unreadable store")
}
