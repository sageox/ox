package skillmanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// TestTeamSkillsSurviveABlindTeamCheckout is the guard against the worst thing
// this feature can do: delete a team's whole skill library because ox could not
// see it for a moment.
//
// A team skill absent from discovery has two completely different causes that
// produce the identical empty result — the team retired it, or the checkout was
// missing, mid-clone, or sparse-excluded (GH #862). The prune loop cannot tell
// them apart, so the catalog source has to say which happened. Only a genuine
// retirement may delete.
//
// Each case installs the skill for real, then blinds the checkout in a different
// way, then reconciles again. The skill must still be on disk.
func TestTeamSkillsSurviveABlindTeamCheckout(t *testing.T) {
	t.Parallel()

	const skillName = "deploy"
	installed := filepath.Join(".agents", "skills", TeamPrefix+skillName, "SKILL.md")

	blindings := map[string]func(t *testing.T, teamPath string){
		// The daemon owns this checkout. A reclone, a GC, or a machine restore
		// can take it away underneath a session already in flight.
		"whole checkout disappears": func(t *testing.T, teamPath string) {
			require.NoError(t, os.RemoveAll(teamPath))
		},
		// GH #862: the sparse set omits agents/, so the directory is simply not
		// materialized. Discovery walks an empty tree and returns no error.
		"agents/ is not materialized": func(t *testing.T, teamPath string) {
			require.NoError(t, os.RemoveAll(filepath.Join(teamPath, "agents")))
		},
	}

	for name, blind := range blindings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			teamPath := t.TempDir()
			writeTeamSkill(t, teamPath, skillName, "", nil)
			stageTeamWiredProject(t, repo, teamPath)

			target := sharedTarget()
			_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(repo, installed), "setup failed: the skill never installed")
			require.NoError(t, os.Remove(StatePath(repo)),
				"fixture must prove the reserved-namespace recovery scan also honors blindness")

			blind(t, teamPath)

			plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
			require.NoError(t, err)

			require.NotEmpty(t, plan.RetainedTeamReason(),
				"ox could not see the team checkout but reported no reason, so nothing suppressed the prune")
			require.Empty(t, plan.RemovedPaths(),
				"a blind team checkout was treated as a retirement and the skill was scheduled for deletion")
			require.FileExists(t, filepath.Join(repo, installed),
				"the team's skill was deleted because ox could not see the checkout")

			// The CLI's own skills must be unaffected either way — retention is
			// scoped to team-owned paths, not a blanket freeze on the inventory.
			require.FileExists(t, filepath.Join(repo, ".agents", "skills", "ox-cli-plan", "SKILL.md"))
		})
	}
}

// TestRetiredTeamSkillIsStillRemovedWhenTheCheckoutIsVisible is the other half.
// Retention must be narrow: when ox CAN see the team checkout and the skill is
// genuinely gone from it, the promise is that it disappears from every machine.
// A guard that never deletes would be safe and useless.
func TestRetiredTeamSkillIsStillRemovedWhenTheCheckoutIsVisible(t *testing.T) {
	t.Parallel()

	const skillName = "deploy"
	installed := filepath.Join(".agents", "skills", TeamPrefix+skillName, "SKILL.md")

	repo := t.TempDir()
	teamPath := t.TempDir()
	writeTeamSkill(t, teamPath, skillName, "", nil)
	stageTeamWiredProject(t, repo, teamPath)

	target := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(repo, installed))

	// The team retires the skill: agents/skills/ still exists and is readable,
	// this one entry is gone. That is an authoritative answer.
	require.NoError(t, os.RemoveAll(filepath.Join(teamPath, "agents", "skills", skillName)))

	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)

	require.Empty(t, plan.RetainedTeamReason(),
		"a readable checkout was misreported as blind, which would make retirement impossible")
	require.NoFileExists(t, filepath.Join(repo, installed),
		"a skill the team retired is still on disk; the mirror only adds")
}

// TestTeamSkillsSurviveRepositorySlugFallback covers a subtler form of
// blindness than a missing checkout. RepoSlug deliberately falls back to the
// directory name for offline/local repositories, but that fallback is not an
// authoritative value for a repos: filter. Treating it as one makes every
// targeted team skill disappear from discovery and turns "origin is briefly
// unavailable" into a mass retirement.
func TestTeamSkillsSurviveRepositorySlugFallback(t *testing.T) {
	t.Parallel()

	const skillName = "deploy"
	installed := filepath.Join(".agents", "skills", TeamPrefix+skillName, "SKILL.md")

	repo := t.TempDir()
	teamPath := t.TempDir()
	writeTeamSkill(t, teamPath, skillName, "repos: [\"acme/api\"]\n", nil)
	stageTeamWiredProject(t, repo, teamPath)

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	target := sharedTarget()
	_, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(repo, installed), "setup failed: targeted team skill never installed")

	// Simulate a local clone whose canonical origin is temporarily unavailable.
	// RepoSlug still returns the directory basename, but that must be reported as
	// degraded rather than used as an authoritative negative repos: match.
	git("remote", "remove", "origin")
	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)

	require.Contains(t, plan.RetainedTeamReason(), "slug",
		"repository slug fallback was not surfaced as degraded")
	require.Empty(t, plan.RemovedPaths(),
		"repository slug fallback was treated as an authoritative retirement")
	require.FileExists(t, filepath.Join(repo, installed),
		"targeted team skill was deleted when the canonical repository slug disappeared")
}

// An unknown slug hides only repos:-targeted skills. Untargeted skills require
// no repository identity and must still be added while removals stay guarded.
func TestUntargetedTeamSkillsStillInstallWithoutRepositorySlug(t *testing.T) {
	t.Parallel()

	const skillName = "team-wide"
	repo := t.TempDir()
	teamPath := t.TempDir()
	writeTeamSkill(t, teamPath, skillName, "", nil)
	stageTeamWiredProject(t, repo, teamPath)

	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "remove origin: %s", out)

	target := sharedTarget()
	plan, err := Reconcile(repo, "1.0.0", desiredFor(target), []adapterprotocol.SkillTarget{target})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(repo, ".agents", "skills", TeamPrefix+skillName, "SKILL.md"),
		"an untargeted skill was withheld even though it needs no repository slug")
	require.Contains(t, plan.RetainedTeamReason(), "slug",
		"targeted removals were not guarded while repository identity was unknown")
}

// TestLegacyCoworkersRootIsNotBlindness is a regression test for a bug that
// shipped: the blindness check stat'd only `agents/`, but discovery walks
// `agents/skills` AND the legacy `coworkers/skills`.
//
// A team that predates the agents/ migration keeps everything under coworkers/,
// so every one of them was judged permanently blind. The consequence is quiet
// and bad in the other direction from a mass delete: retirement is suppressed
// forever, so a skill the team deletes never leaves anyone's machine and nothing
// explains why. Found by running `ox skills status` against a real legacy team.
func TestLegacyCoworkersRootIsNotBlindness(t *testing.T) {
	t.Parallel()

	teamPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(teamPath, "coworkers", "skills"), 0o755))

	require.True(t, anySkillRootOnDisk(teamPath),
		"a team whose skills live under the legacy coworkers/ root was judged blind, which suppresses retirement for them forever")
	require.Empty(t, unseeableTeamSkills(teamPath, "acme/api"),
		"a legacy-rooted team context was reported as unseeable")
}

// TestNoSkillRootAtAllIsBlindness is the counterweight: the check must still
// catch the real GH #862 shape, where neither root materialized.
func TestNoSkillRootAtAllIsBlindness(t *testing.T) {
	t.Parallel()

	teamPath := t.TempDir()
	require.False(t, anySkillRootOnDisk(teamPath))
	require.NotEmpty(t, unseeableTeamSkills(teamPath, "acme/api"),
		"a team context with no skills directory at all was treated as authoritative, which permits a mass delete")
}
