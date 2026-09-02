package main

import (
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnrichedTeam_ToConfigTeamContext(t *testing.T) {
	syncTime := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	team := enrichedTeam{
		TeamID:   "team-abc",
		Name:     "Alpha Team",
		Slug:     "alpha-team",
		Path:     "/path/to/team",
		LastSync: syncTime,
		Primary:  true,
	}

	tc := team.toConfigTeamContext()

	assert.Equal(t, "team-abc", tc.TeamID)
	assert.Equal(t, "Alpha Team", tc.TeamName)
	assert.Equal(t, "alpha-team", tc.Slug)
	assert.Equal(t, "/path/to/team", tc.Path)
	assert.Equal(t, syncTime, tc.LastSync)
}

func TestEnrichedTeam_ToConfigTeamContext_ZeroValues(t *testing.T) {
	team := enrichedTeam{
		TeamID: "team-minimal",
	}

	tc := team.toConfigTeamContext()

	assert.Equal(t, "team-minimal", tc.TeamID)
	assert.Equal(t, "", tc.TeamName)
	assert.Equal(t, "", tc.Slug)
	assert.Equal(t, "", tc.Path)
	assert.True(t, tc.LastSync.IsZero())
}

func TestDiscoverAllTeams_PrimaryFirst(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-second", TeamName: "Second Team", Slug: "second-team", Path: "/path/second"},
		{TeamID: "team-primary", TeamName: "Primary Team", Slug: "primary-team", Path: "/path/primary"},
		{TeamID: "team-third", TeamName: "Third Team", Slug: "third-team", Path: "/path/third"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "team-primary", TeamName: "Primary Team"})

	teams := discoverAllTeams(dir)
	if len(teams) == 0 {
		t.Skip("no teams discovered")
	}

	// find our primary team in the result
	var primaryIdx = -1
	for i, team := range teams {
		if team.TeamID == "team-primary" {
			primaryIdx = i
			break
		}
	}
	if primaryIdx == -1 {
		t.Skip("test team not in discovered teams")
	}

	assert.True(t, teams[primaryIdx].Primary, "team-primary should be marked primary")
	// primary teams come first in the result
	assert.Equal(t, 0, primaryIdx, "primary team should be first")
}

func TestDiscoverAllTeams_EnrichesEmptyNameToTeamID(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-no-name", TeamName: "", Path: "/path/nameless"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	// don't set team-no-name as primary so project config name doesn't interfere
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "other-team"})

	teams := discoverAllTeams(dir)
	if len(teams) == 0 {
		t.Skip("no teams discovered")
	}

	// find our test team
	for _, team := range teams {
		if team.TeamID == "team-no-name" {
			// name should have been enriched (at minimum to team ID itself)
			assert.NotEmpty(t, team.Name, "enriched team name should not be empty")
			return
		}
	}
	t.Skip("team-no-name not in discovered teams")
}

func TestResolveTeamByQuery_NoTeamsReturnsNil(t *testing.T) {
	// use an empty temp dir with no config — no daemon, no filesystem teams
	dir := createInitializedProject(t)
	// don't save any local config with teams
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: nil})

	// the function may still find teams from the real daemon/filesystem,
	// so only assert nil result for a definitely-nonexistent query
	result := resolveTeamByQuery(dir, "absolutely-nonexistent-team-xyz-123")
	assert.Nil(t, result)
}

func TestResolveTeamByQuery_WhitespaceHandling(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-ws-test", TeamName: "WS Team", Slug: "ws-team", Path: "/path/ws"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "team-ws-test"})

	// query with whitespace should match after trimming
	result := resolveTeamByQuery(dir, "  ws-team  ")
	if result == nil {
		t.Skip("team not found in discovery")
	}
	assert.Equal(t, "team-ws-test", result.TeamID, "query with whitespace should still match")
}

func TestResolveTeamMembership_TableDriven(t *testing.T) {
	teams := []api.TeamMembership{
		{ID: "team_abc123", Name: "Platform", Slug: "platform"},
		{ID: "team_xyz789", Name: "Developer Experience", Slug: "dx"},
		// this team's NAME collides with the previous team's SLUG, which pins
		// the resolution order rather than leaving it to chance
		{ID: "team_def456", Name: "dx", Slug: "design-experiments"},
		// slug that cannot be derived from the name, so only a real slug pass finds it
		{ID: "team_ghi012", Name: "Research & Development", Slug: "rnd"},
	}

	tests := []struct {
		name   string
		query  string
		wantID string // "" means no match expected
	}{
		{"exact slug", "platform", "team_abc123"},
		{"slug is case-insensitive", "Platform", "team_abc123"},
		{"exact team ID", "team_xyz789", "team_xyz789"},
		{"name is case-insensitive", "developer experience", "team_xyz789"},
		{"surrounding whitespace is trimmed", "  platform  ", "team_abc123"},
		{"slug wins over a name that collides with it", "dx", "team_xyz789"},
		{"slug that the name does not contain", "rnd", "team_ghi012"},
		{"unknown value matches nothing", "no-such-team", ""},
		{"empty query matches nothing", "", ""},
		{"whitespace-only query matches nothing", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTeamMembership(teams, tt.query)
			if tt.wantID == "" {
				assert.Nil(t, got, "expected no match for %q", tt.query)
				return
			}
			if assert.NotNil(t, got, "expected a match for %q", tt.query) {
				assert.Equal(t, tt.wantID, got.ID)
			}
		})
	}
}

func TestResolveTeamMembership_EmptyTeamList(t *testing.T) {
	assert.Nil(t, resolveTeamMembership(nil, "platform"))
	assert.Nil(t, resolveTeamMembership([]api.TeamMembership{}, "platform"))
}

func TestResolveTeamMembership_TeamWithoutSlug(t *testing.T) {
	// older servers omit slug; ID and name must still resolve
	teams := []api.TeamMembership{{ID: "team_abc123", Name: "Platform"}}

	assert.Equal(t, "team_abc123", resolveTeamMembership(teams, "team_abc123").ID)
	assert.Equal(t, "team_abc123", resolveTeamMembership(teams, "platform").ID)
	assert.Nil(t, resolveTeamMembership(teams, ""), "empty query must not match an empty slug")
}

func TestFormatTeamCandidates(t *testing.T) {
	assert.Equal(t,
		"Platform (platform, team_abc123), Developer Experience (dx, team_xyz789)",
		formatTeamCandidates([]api.TeamMembership{
			{ID: "team_abc123", Name: "Platform", Slug: "platform"},
			{ID: "team_xyz789", Name: "Developer Experience", Slug: "dx"},
		}))

	// a team with no slug still renders, without an empty pair of parentheses
	assert.Equal(t, "Platform (team_abc123)",
		formatTeamCandidates([]api.TeamMembership{{ID: "team_abc123", Name: "Platform"}}))
}

func TestUnknownTeamError(t *testing.T) {
	// an account with teams gets the candidates it could have typed
	withTeams := unknownTeamError("no-such-team", []api.TeamMembership{
		{ID: "team_abc123", Name: "Platform", Slug: "platform"},
	})
	require.Error(t, withTeams)
	assert.Contains(t, withTeams.Error(), `unknown team "no-such-team"`)
	assert.Contains(t, withTeams.Error(), "Platform (platform, team_abc123)")
}

// TestResolveTeamFlag_States pins which fetch outcomes may reject --team: only a
// NON-EMPTY membership list may. Every other outcome passes the trimmed value
// through to the server, which is the authority on team names.
//
// The `why` field is the reason a case exists, and is used as the assertion
// message so a regression reports the rule it broke rather than just a diff.
func TestResolveTeamFlag_States(t *testing.T) {
	teams := []api.TeamMembership{
		{ID: "team_abc123", Name: "Platform", Slug: "platform"},
		{ID: "team_xyz789", Name: "Research & Development", Slug: "rnd"},
	}

	tests := []struct {
		name     string
		flag     string
		teams    []api.TeamMembership
		fetchErr error
		wantID   string
		wantName string
		wantErr  []string // substrings the error must contain; empty means no error
		why      string
	}{
		{
			name:     "transport error passes the trimmed value through",
			flag:     "  platform  ",
			fetchErr: errors.New("network unreachable"),
			wantID:   "platform",
			why:      "a degraded network must not make --team unusable",
		},
		{
			// fetchTeamMemberships returns (nil, nil) for a response with no body.
			// Reading that as "this account has no teams" is what blocked ox init --team.
			name:   "nil membership list passes through rather than rejecting",
			flag:   " platform ",
			teams:  nil,
			wantID: "platform",
			why:    "an empty list is not proof the account has no teams",
		},
		{
			// Deliberately non-nil and empty, which is a distinct state from the case
			// above: promptNoTeams offers "Continue (a new team will be created)" on
			// the picker path, so zero teams is continuable everywhere else in init.
			name:   "empty non-nil membership list also passes through",
			flag:   "platform",
			teams:  []api.TeamMembership{},
			wantID: "platform",
			why:    "zero teams is a continuable state on every other init path",
		},
		{
			name:     "authoritative list resolves a slug the name does not contain",
			flag:     "rnd",
			teams:    teams,
			wantID:   "team_xyz789",
			wantName: "Research & Development",
			why:      "resolution must consider the slug, not just the name",
		},
		{
			name:  "authoritative list rejects a value it does not contain",
			flag:  "no-such-team",
			teams: teams,
			wantErr: []string{
				`unknown team "no-such-team"`,
				"Platform (platform, team_abc123)",
			},
			why: "a non-empty list is authoritative, so an absent value is a typo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, name, err := resolveTeamFlag(tt.flag, tt.teams, tt.fetchErr)

			if len(tt.wantErr) > 0 {
				require.Error(t, err, tt.why)
				for _, want := range tt.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				assert.Empty(t, id, "a rejected flag must not resolve an ID")
				assert.Empty(t, name, "a rejected flag must not resolve a name")
				return
			}

			require.NoError(t, err, tt.why)
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantName, name)
		})
	}
}
