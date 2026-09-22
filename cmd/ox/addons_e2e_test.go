package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/addons"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/stretchr/testify/require"
)

// addons_e2e_test.go — the customer journey, end to end, through the real
// command surface and the real delivery path.
//
// Everything else about add-ons is tested one layer at a time: the catalog
// resolves bytes, the transaction writes a Team Context, convergence projects
// Team Skills into a repository. Each of those can pass while the journey is
// broken, and two of them did earlier on this branch — convergence rejected
// every add-on artifact as "unknown origin", and origin resolution reported
// add-on skills as hand-authored, both silently. Only a test that walks the
// whole path catches that class.

// TestAddonsInstall_ReachesACoworkerEndToEnd is the promise: a team installs an
// add-on once, and the skill shows up in a repository where an AI coworker will
// actually read it — with no repository-side step, because ADR-032 D1 says
// there is none.
//
// Failure prevented: any layer in install → Team Context → convergence →
// projection silently dropping add-on content, which is invisible from inside
// any single layer.
func TestAddonsInstall_ReachesACoworkerEndToEnd(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)

	// Give the fixture the REAL deny-all .sageox/.gitignore a production Team
	// Context carries, so this test walks the ignore rules instead of an empty
	// directory. `.sageox/` must exist FIRST: EnsureCheckoutGitignore is a
	// no-op when it does not, which is exactly how an earlier version of this
	// fixture silently proved nothing — deleting the ADR-032 D5 allow-list
	// entry left this test green, because there were no ignore rules to
	// violate. The MkdirAll is the load-bearing line, not the Ensure.
	require.NoError(t, os.MkdirAll(filepath.Join(team, ".sageox"), 0o755))
	require.NoError(t, gitserver.EnsureCheckoutGitignore(team))
	require.FileExists(t, filepath.Join(team, ".sageox", ".gitignore"),
		"fixture must carry real ignore rules, or the D5 assertion below is decoration")

	const addon = "post-cutoff"
	installed := filepath.Join(repo, ".claude", "skills", "sageox-team-"+addon, "SKILL.md")
	require.NoFileExists(t, installed, "fixture must start with nothing installed, or this proves nothing")

	// --- the team chooses the add-on, through the real command path ---
	result, err := addons.Install(context.Background(), team, addons.NewEmbeddedProvider(), addon, "")
	require.NoError(t, err)
	require.Equal(t, addon, result.Addon)
	require.NotEmpty(t, result.Version, "an install must pin a concrete version")
	require.NotEmpty(t, result.Written)

	// The Team Context now holds the content AND the selection...
	require.FileExists(t, filepath.Join(team, "agents", "skills", addon, "SKILL.md"))
	require.FileExists(t, filepath.Join(team, addons.LockRelativePath))

	// ...and both are TRACKED, not merely present. An untracked file in a Team
	// Context checkout wedges blue-green GC permanently (ADR-032 D5), and a
	// lock that is never committed never reaches a teammate — the exact silent
	// failure the .sageox/.gitignore allow-list exists to prevent. os.Stat
	// cannot tell the difference; git can.
	tracked := gitOutput(t, team, "ls-files")
	require.Contains(t, tracked, "agents/skills/"+addon+"/SKILL.md",
		"add-on content must be committed, or teammates never receive it")
	require.Contains(t, tracked, addons.LockRelativePath,
		"the selection lock must be committed, or the team's choice evaporates silently")

	// --- ox sync distributes it; nobody touched the repository ---
	convergeAfterSessionBoundary(repo)

	require.FileExists(t, installed,
		"the add-on's skill must reach the repository through ordinary convergence — "+
			"installing an add-on takes no repository-side step (ADR-032 D1)")

	body, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.NotEmpty(t, bytes.TrimSpace(body), "a projected skill with no body reaches nobody")
}

// TestAddonsInstall_ConvergenceAttributesTheAddon proves the provenance half.
//
// Delivery alone is not enough: if convergence reports add-on content as
// hand-authored, nobody can tell what the catalog owns, `ox addons update`
// cannot explain what it is about to overwrite, and retirement has nothing to
// retire from. That was a real bug on this branch — the lock records FILES
// while discovery describes a skill by its DIRECTORY, so exact-match ownership
// answered "loose" for every add-on skill.
func TestAddonsInstall_ConvergenceAttributesTheAddon(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	const addon = "post-cutoff"

	_, err := addons.Install(context.Background(), team, addons.NewEmbeddedProvider(), addon, "")
	require.NoError(t, err)

	report, err := teamconverge.Converge(context.Background(), teamconverge.Request{
		ProjectRoot: repo, TeamPath: team, Mode: teamconverge.ModeExplicit,
	})
	require.NoError(t, err)

	var found bool
	for _, o := range report.Outcomes {
		if o.Name != addon {
			continue
		}
		found = true
		require.Equal(t, teamconverge.OriginAddon, o.Origin.Kind,
			"convergence must attribute %q to the Add-on Catalog, not to the team", addon)
		require.Equal(t, addon, o.Origin.Addon)
		require.NotEmpty(t, o.Origin.AddonVersion, "provenance without a version cannot answer 'which version do we have'")
		require.NotEmpty(t, o.Origin.Digest, "provenance without a digest cannot answer 'did someone edit this'")
	}
	require.True(t, found, "convergence reported no outcome for %q at all", addon)
}

// TestAddonsRemove_TakesBackOnlyWhatItOwns closes the journey: a team can undo
// its choice, and the undo is precise. Removing an add-on must not touch a
// hand-authored neighbor that happens to live in the same root — ADR-032 D3
// puts add-on and hand-authored content in the SAME directories, so ownership
// comes from the lock and nothing else.
func TestAddonsRemove_TakesBackOnlyWhatItOwns(t *testing.T) {
	_, team := stageTeamPublishRepo(t)
	const addon = "post-cutoff"

	_, err := addons.Install(context.Background(), team, addons.NewEmbeddedProvider(), addon, "")
	require.NoError(t, err)

	// A hand-authored skill the team wrote, sitting beside the add-on.
	handDir := filepath.Join(team, "agents", "skills", "our-own-playbook")
	require.NoError(t, os.MkdirAll(handDir, 0o755))
	handFile := filepath.Join(handDir, "SKILL.md")
	const handBody = "---\nname: our-own-playbook\ndescription: ours\n---\nOURS\n"
	require.NoError(t, os.WriteFile(handFile, []byte(handBody), 0o644))

	_, err = addons.Remove(context.Background(), team, addon)
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(team, "agents", "skills", addon, "SKILL.md"),
		"remove must delete the paths the lock owns")

	got, readErr := os.ReadFile(handFile)
	require.NoError(t, readErr, "remove must not delete a hand-authored neighbor")
	require.Equal(t, handBody, string(got), "remove must not alter a hand-authored neighbor")

	lock, err := addons.LoadLock(team)
	require.NoError(t, err)
	_, still := lock.Find(addon)
	require.False(t, still, "remove must drop the lock record, or the add-on looks installed forever")
}

// TestAddonsCLI_ListReportsTheBuiltInAddon drives the command surface itself,
// so the JSON contract another tool would parse is exercised rather than
// assumed. No flag setup: `ox addons` is an ordinary command now.
func TestAddonsCLI_ListReportsTheBuiltInAddon(t *testing.T) {
	var out bytes.Buffer
	cmd := addonsListCmd
	cmd.SetOut(&out)
	t.Cleanup(func() { cmd.SetOut(nil) })
	require.NoError(t, cmd.Flags().Set("json", "true"))
	t.Cleanup(func() { _ = cmd.Flags().Set("json", "false") })

	require.NoError(t, runAddonsList(cmd, nil))

	require.Contains(t, out.String(), `"name": "post-cutoff"`,
		"the built-in add-on must appear in `ox addons list --json`")
	require.Contains(t, out.String(), `"update_available"`,
		"update_available must always be present: an absent key cannot be told apart from an ox too old to report it")
}
