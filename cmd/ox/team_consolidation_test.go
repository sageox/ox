package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	friction "github.com/sageox/frictionax"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// --- A. The plural/singular trap ---
//
// `ox teams members` used to print the whole team LIST and silently ignore
// `members`, because `teams` was a flat leaf command with no `members`
// subcommand and no Args guard. Now `teams` is an alias of the `team` parent and
// `members` is a real subcommand, so the plural form must resolve to the roster.

// TestTeamsMembers_RoutesToRoster_NotList is the red-first proof of the trap fix.
// Failure prevented: `ox teams members` dumping the team list instead of the roster.
func TestTeamsMembers_RoutesToRoster_NotList(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"teams", "members"})
	require.NoError(t, err)
	require.Equal(t, "members", cmd.Name(),
		"`ox teams members` must resolve to the members subcommand, not the team lister")
}

// TestTeams_IsAliasOfTeamParent proves the plural form is an alias of the single
// canonical parent (so every `ox team <verb>` also works as `ox teams <verb>`).
func TestTeams_IsAliasOfTeamParent(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"teams"})
	require.NoError(t, err)
	require.Equal(t, "team", cmd.Name())
}

// TestTeamHasConsolidatedSubcommands proves the one-home surface: every verb the
// consolidation promised is wired under `ox team`, including the re-homed invite.
func TestTeamHasConsolidatedSubcommands(t *testing.T) {
	for _, want := range []string{"list", "members", "show", "open", "invite"} {
		cmd, _, err := teamCmd.Find([]string{want})
		require.NoError(t, err)
		require.Equal(t, want, cmd.Name(), "ox team %s must be a subcommand", want)
	}
}

// TestTeamContext_IsNotAConsolidatedVerb proves `ox team context` was NOT added:
// the AI-consumption surface stays canonically at `ox agent team-ctx`, so the
// human tree carries no duplicate. `Find` on a bare-RunE parent returns the
// parent for an unknown token, so identity is the check, not the error.
func TestTeamContext_IsNotAConsolidatedVerb(t *testing.T) {
	cmd, _, err := teamCmd.Find([]string{"context"})
	require.NoError(t, err)
	require.Equal(t, "team", cmd.Name(), "`ox team context` must not exist as its own subcommand")
}

// TestTeamMembers_CoworkersAlias keeps the user-facing "coworker" vocabulary
// reachable as a command alias.
func TestTeamMembers_CoworkersAlias(t *testing.T) {
	cmd, _, err := teamCmd.Find([]string{"coworkers"})
	require.NoError(t, err)
	require.Equal(t, "members", cmd.Name())
}

// --- B. The strict dispatcher ---

// TestTeamDispatch_UnknownSubcommandErrors proves a stray token fails loudly
// rather than falling through to the lister (the essence of the trap fix).
func TestTeamDispatch_UnknownSubcommandErrors(t *testing.T) {
	err := runTeamDispatch(teamCmd, []string{"bogus"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown subcommand")
}

// --- C. `ox team show` ---

// TestRenderTeamShow renders a one-team card and asserts each field lands,
// including the primary badge and coworker count.
func TestRenderTeamShow(t *testing.T) {
	c := teamCard{teamID: "team_abc", name: "Acme", slug: "acme", primary: true, path: "/ctx/path", lastSync: "2h"}

	var buf bytes.Buffer
	renderTeamShow(&buf, c, 3, true, "https://x/team/team_abc")
	out := buf.String()
	require.Contains(t, out, "Acme")
	require.Contains(t, out, "team_abc")
	require.Contains(t, out, "(this repo)")
	require.Contains(t, out, "3")
	require.Contains(t, out, "/ctx/path")

	// Unknown coworker count degrades to a word, never a misleading "0".
	var degraded bytes.Buffer
	renderTeamShow(&degraded, c, 0, false, "")
	require.Contains(t, degraded.String(), "unavailable")
}

// TestWriteTeamShowJSON pins the stable envelope a JSON/agent consumer relies on.
func TestWriteTeamShowJSON(t *testing.T) {
	c := teamCard{teamID: "team_abc", name: "Acme", slug: "acme", primary: true, path: "/p", lastSync: "2h"}

	var known bytes.Buffer
	require.NoError(t, writeTeamShowJSON(&known, c, 3, true, "https://x/team/team_abc"))
	var got map[string]any
	require.NoError(t, json.Unmarshal(known.Bytes(), &got))
	require.Equal(t, float64(3), got["coworker_count"])
	require.Equal(t, true, got["coworkers_available"])
	require.Equal(t, "team_abc", got["team_id"])
	require.Equal(t, "https://x/team/team_abc", got["dashboard_url"])

	// When the roster is unavailable, the count is present but flagged, never
	// silently rendered as a real 0.
	var unknown bytes.Buffer
	require.NoError(t, writeTeamShowJSON(&unknown, c, 0, false, ""))
	require.NoError(t, json.Unmarshal(unknown.Bytes(), &got))
	require.Equal(t, false, got["coworkers_available"])
}

// --- D. Old commands point to their new homes via the friction catalog ---

// TestFrictionCatalog_TeamRedirects proves the embedded catalog carries the
// old->new command mappings so a coworker who types the old form is redirected.
// Failure prevented: a rename that leaves `ox view team` / `ox team-ctx` dead
// ends with no pointer to the new command.
func TestFrictionCatalog_TeamRedirects(t *testing.T) {
	var data friction.CatalogData
	require.NoError(t, json.Unmarshal(defaultCatalogJSON, &data),
		"embedded default_catalog.json must be valid CatalogData")

	byPattern := make(map[string]friction.CommandMapping, len(data.Commands))
	for _, m := range data.Commands {
		byPattern[m.Pattern] = m
	}

	for pattern, wantTarget := range map[string]string{
		"view team": "team open",      // ox view team -> ox team open
		"team-ctx":  "agent team-ctx", // ox team-ctx -> ox agent team-ctx (agent UX)
		"invite":    "team invite",    // ox invite -> ox team invite
	} {
		m, ok := byPattern[pattern]
		require.Truef(t, ok, "friction catalog must map %q to its new home", pattern)
		require.Equalf(t, wantTarget, m.Target, "%q should redirect to %q", pattern, wantTarget)
		require.Truef(t, m.AutoExecute && m.Confidence >= 0.85,
			"%q redirect should auto-execute (confidence >= 0.85)", pattern)
		require.Falsef(t, strings.HasPrefix(m.Target, "ox "),
			"catalog targets are cli-relative; %q should not start with 'ox '", pattern)
	}
}

// --- E. Positional team selector ---

// newTeamFlagCmd builds a command carrying the --team flag the selector reads.
func newTeamFlagCmd() *cobra.Command {
	c := &cobra.Command{Use: "show"}
	c.Flags().String("team", "", "")
	return c
}

// TestTeamRefFromArgs_PositionalWins is the red-first proof of the ignored-selector
// bug: `ox team show acme` advertised by the list hints must resolve `acme`, not
// silently fall back to the primary team. Positional takes precedence over --team.
// Failure prevented: `ox team show <slug>` showing the wrong team.
func TestTeamRefFromArgs_PositionalWins(t *testing.T) {
	// positional present -> used
	require.Equal(t, "acme", teamRefFromArgs(newTeamFlagCmd(), []string{"acme"}))

	// positional beats --team
	c := newTeamFlagCmd()
	require.NoError(t, c.Flags().Set("team", "beta"))
	require.Equal(t, "acme", teamRefFromArgs(c, []string{"acme"}))

	// no positional -> --team
	c2 := newTeamFlagCmd()
	require.NoError(t, c2.Flags().Set("team", "beta"))
	require.Equal(t, "beta", teamRefFromArgs(c2, nil))

	// neither -> empty (caller falls back to the primary team)
	require.Equal(t, "", teamRefFromArgs(newTeamFlagCmd(), nil))
}

// TestValidTeamRef_RejectsRouteChangingRefs proves a team reference that could
// retarget a /team/<ref> route or corrupt terminal output is refused before it
// reaches a URL. Failure prevented: `..`/separators/control bytes reaching the
// dashboard path or the roster endpoint.
func TestValidTeamRef_RejectsRouteChangingRefs(t *testing.T) {
	for _, bad := range []string{"", " ", ".", "..", "a/b", "a\\b", "acme\x00", "team\x1b[31m"} {
		require.Falsef(t, validTeamRef(bad), "%q must be rejected", bad)
	}
	for _, ok := range []string{"acme", "team_01H", "acme-corp", "Acme.Team"} {
		require.Truef(t, validTeamRef(ok), "%q must be accepted", ok)
	}
}

// --- F. The invite alias never triggers the friction redirect ---

// TestInviteAlias_ResolvesSoFrictionNeverFires proves the hidden `ox invite`
// alias is a real, routable command. Friction recovery runs ONLY when
// rootCmd.Execute() returns an error (see executeWithFrictionRecovery); because
// `invite` resolves cleanly, cobra never emits an "unknown command" error, so the
// catalog's invite->team invite redirect can only ever apply to a genuine error
// path — a successful `ox invite <email>` is never re-dispatched.
// Failure prevented: a working `ox invite` being double-executed via the redirect.
func TestInviteAlias_ResolvesSoFrictionNeverFires(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"invite", "alice@acme.com"})
	require.NoError(t, err)
	require.Equal(t, "invite", cmd.Name())
	require.True(t, cmd.Hidden, "the alias is hidden; the visible surface is `ox team invite`")
}
