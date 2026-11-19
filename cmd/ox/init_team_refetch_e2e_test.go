//go:build !short

package main

import (
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
)

// countTeamFetches counts GET /api/v1/cli/repos calls made before the first
// POST /api/v1/repo/init, and lets a test change the team list served from the
// Nth fetch on.
func countTeamFetches(env *oxE2E, afterFetch map[int][]api.TeamMembership) func() int {
	var mu sync.Mutex
	fetches, registered := 0, false
	env.OnRequest(func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/repo/init":
			registered = true
		case "/api/v1/cli/repos":
			if registered {
				return
			}
			fetches++
			if teams, ok := afterFetch[fetches]; ok {
				env.SetTeams(teams...)
			}
		}
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return fetches
	}
}

// TestRunInit_TeamFlag_RefetchesOnceBeforePassingThrough covers #1335: right after a
// fresh login the team list can come back empty or without the team just joined, so
// `ox init --team <slug>` sent the slug unresolved and the server said "team name not
// found". init now asks for the list once more before it passes the value through.
func TestRunInit_TeamFlag_RefetchesOnceBeforePassingThrough(t *testing.T) {
	newTeam := api.TeamMembership{ID: "team-e2e-new", Name: "New Team", Slug: "new-team", Role: "member"}

	for _, tc := range []struct {
		name  string
		first []api.TeamMembership
	}{
		{name: "first list is empty", first: []api.TeamMembership{}},
		{name: "first list lacks the team", first: []api.TeamMembership{{ID: "team-e2e", Name: "E2E Team", Slug: "e2e-team"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOxE2E(t)
			withInitFlags(t, "new-team")
			env.SetTeams(tc.first...)
			fetches := countTeamFetches(env, map[int][]api.TeamMembership{2: append(append([]api.TeamMembership{}, tc.first...), newTeam)})

			require.NoError(t, runInit())

			assert.Equal(t, 2, fetches(), "init must fetch the team list exactly once more before registering")
			reqs := env.InitRequests()
			require.Len(t, reqs, 1)
			assert.Equal(t, []string{"team-e2e-new"}, reqs[0].Teams, "the slug must resolve against the second list")
		})
	}
}

// TestRunInit_TeamFlag_RejectedAfterRefetchShowsCandidates: when the second list
// still lacks the value and the server rejects it, the error names the teams that
// would have worked, so the user is not left with a bare "team name not found".
func TestRunInit_TeamFlag_RejectedAfterRefetchShowsCandidates(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, "team-typo")
	fetches := countTeamFetches(env, nil)
	env.OnRegister(func(api.RepoInitRequest) (int, any) {
		return http.StatusBadRequest, map[string]any{"success": false, "error": `team name not found: "team-typo"`}
	})

	var err error
	captureStderr(t, func() { err = runInit() })

	require.Error(t, err)
	assert.Equal(t, 2, fetches(), "init must re-fetch once before passing the value through")
	assert.Contains(t, err.Error(), "failed to register with SageOx API")
	assert.Contains(t, err.Error(), "Other Team (other-team, team-e2e-other)", "the error must list the candidates")
	assert.Contains(t, err.Error(), "E2E Team (e2e-team, team-e2e)")
}
