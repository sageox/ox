package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/testguard"
)

// loggedInTeamsServer logs in to a loopback server running handler, with no
// SAGEOX_ENDPOINT override and no project, so endpoint selection must come from
// the stored login alone.
func loggedInTeamsServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	noInputCLIEnv(t)
	server := testguard.SafeMockServer(t, handler)
	t.Setenv("SAGEOX_ENDPOINT", "")
	require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
		AccessToken: "test-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
	}))
	t.Chdir(t.TempDir())
}

func runTeamsJSON(t *testing.T) teamsOutput {
	t.Helper()
	cmd, out := teamsListCmd()
	require.NoError(t, runTeams(cmd, nil))
	var got teamsOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output: %s", out.String())
	return got
}

// Failure prevented: a coworker whose server lookup failed was told "No teams
// found, run ox login then ox init" although ox never checked the account.
func TestRunTeams_ServerLookupFailureIsNotReportedAsNoTeams(t *testing.T) {
	loggedInTeamsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	got := runTeamsJSON(t)

	assert.Empty(t, got.Teams)
	assert.NotEmpty(t, got.LookupError, "failed lookup must be flagged in the JSON document")
	assert.NotContains(t, got.Guidance, "ox login", "must not recommend login when the account was never checked")
}

// Failure prevented: a coworker logged in only to a non-default endpoint got no
// teams outside a project, because the fallback asked the default endpoint.
func TestRunTeams_NoProjectUsesTheLoggedInEndpoint(t *testing.T) {
	loggedInTeamsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/repos" || r.Header.Get("Authorization") != "Bearer test-access" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"teams":[{"id":"team_alpha111","name":"Alpha Team","slug":"alpha-team"}]}`))
	})

	got := runTeamsJSON(t)

	require.Len(t, got.Teams, 1)
	assert.Equal(t, "team_alpha111", got.Teams[0].TeamID)
	assert.Empty(t, got.LookupError)
}

// Failure prevented: `ox team list` showed a derived slug that `ox team show`
// could not resolve, so the displayed reference led to an unresolved-ID lookup.
func TestRunTeams_ServerTeamWithoutSlugShowsResolvableReference(t *testing.T) {
	loggedInTeamsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"teams":[{"id":"team_gamma333","name":"Gamma Team"}]}`))
	})

	got := runTeamsJSON(t)

	require.Len(t, got.Teams, 1)
	assert.Equal(t, "team_gamma333", got.Teams[0].Slug)
}

// Logged out must stay the old, non-failing "No teams found" answer.
func TestRunTeams_LoggedOutKeepsLoginGuidance(t *testing.T) {
	noInputCLIEnv(t)
	t.Setenv("SAGEOX_ENDPOINT", "")
	t.Chdir(t.TempDir())

	got := runTeamsJSON(t)

	assert.Empty(t, got.Teams)
	assert.Empty(t, got.LookupError)
	assert.Contains(t, got.Guidance, "ox login")
}
