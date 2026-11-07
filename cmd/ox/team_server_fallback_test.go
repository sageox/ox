package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/testguard"
)

// serverTeamsFixture isolates HOME/XDG, logs in to a loopback mock server that
// reports two teams on /api/v1/cli/repos, and leaves every local team source
// (daemon, config, credentials) empty. This is the state of a fresh machine
// whose coworker is logged in but has never synced a team context.
func serverTeamsFixture(t *testing.T) {
	t.Helper()
	noInputCLIEnv(t)
	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/repos" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"teams":[` +
			`{"id":"team_alpha111","name":"Alpha Team","slug":"alpha-team","role":"owner"},` +
			`{"id":"team_beta2222","name":"Beta Team","slug":"beta-team","role":"member"}]}`))
	}))
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
		AccessToken: "test-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
	}))
	t.Chdir(t.TempDir()) // not a project: forces the global discovery path
}

func teamsListCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().Bool("json", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	return cmd, &out
}

// Failure prevented: `ox team list` said "No teams found" for a logged-in
// coworker whose teams exist on the server but are not synced to this machine
// yet, while `ox status` listed them. The coworker could not discover a team ID.
func TestRunTeams_ListsServerTeamsWhenNothingSyncedLocally(t *testing.T) {
	serverTeamsFixture(t)
	cmd, out := teamsListCmd()

	require.NoError(t, runTeams(cmd, nil))

	var got teamsOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output: %s", out.String())
	ids := make([]string, 0, len(got.Teams))
	for _, team := range got.Teams {
		ids = append(ids, team.TeamID)
	}
	assert.ElementsMatch(t, []string{"team_alpha111", "team_beta2222"}, ids)
}

// Failure prevented: `ox team show <name>` echoed the typed name back as the
// team ID when the team was not synced locally, so the real ID was only
// discoverable from verbose logs.
func TestResolveTeamCard_ResolvesUnsyncedTeamAgainstServer(t *testing.T) {
	tests := []struct {
		name   string
		ref    string
		wantID string
	}{
		{name: "by name", ref: "Alpha Team", wantID: "team_alpha111"},
		{name: "by slug", ref: "beta-team", wantID: "team_beta2222"},
		{name: "by id", ref: "team_alpha111", wantID: "team_alpha111"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverTeamsFixture(t)

			card, local, err := resolveTeamCard(t.TempDir(), tt.ref)

			require.NoError(t, err)
			assert.False(t, local, "a server-resolved card is not local state")
			assert.Equal(t, tt.wantID, card.teamID)
		})
	}

	t.Run("unknown ref stays unresolved", func(t *testing.T) {
		serverTeamsFixture(t)

		card, _, err := resolveTeamCard(t.TempDir(), "Nonexistent")

		require.NoError(t, err)
		assert.Equal(t, "Nonexistent", card.teamID, "unresolved refs pass through for the roster check to reject")
	})
}
