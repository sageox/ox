package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/require"
)

// TestTeamVisibilityDetail_NamesTheBoundTeamAndTheAlternatives covers the
// reported failure: sessions record fine, but the repo is bound to a team the
// signed-in account cannot see, so the dashboard the user is looking at stays
// empty and they conclude ox is broken. The detail text has to say all three
// things — where it IS going, that capture is still working, and what they
// could switch to.
func TestTeamVisibilityDetail_NamesTheBoundTeamAndTheAlternatives(t *testing.T) {
	cfg := &config.ProjectConfig{TeamID: "team_absent", TeamName: "Acme Corp"}
	teams := []api.TeamMembership{
		{ID: "team_personal", Name: "Sam's Private Team", Personal: true},
		{ID: "team_other", Name: "Widgets Inc"},
	}

	got := teamVisibilityDetail(cfg, teams, "https://sageox.ai")

	for _, want := range []string{
		"Acme Corp (team_absent)",            // the team it actually records to
		"still being captured",               // recording is not the problem
		"https://sageox.ai/team/team_absent", // where to go look
		"Sam's Private Team (personal)",      // personal teams marked as such
		"Widgets Inc",                        // the real alternative
		"ox init",                            // how to change it
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestTeamVisibilityDetail_NoTeamsAtAll is the same symptom with a different
// cause: the account belongs to no teams whatsoever.
func TestTeamVisibilityDetail_NoTeamsAtAll(t *testing.T) {
	cfg := &config.ProjectConfig{TeamID: "team_absent"}
	got := teamVisibilityDetail(cfg, nil, "https://sageox.ai")

	if !strings.Contains(got, "belongs to no teams") {
		t.Errorf("expected the no-memberships explanation, got:\n%s", got)
	}
	if strings.Contains(got, "Teams you belong to") {
		t.Errorf("should not list memberships when there are none, got:\n%s", got)
	}
}

func TestTeamDashboardURL(t *testing.T) {
	tests := []struct {
		name   string
		ep     string
		teamID string
		want   string
	}{
		{"builds the link", "https://sageox.ai", "team_abc", "https://sageox.ai/team/team_abc"},
		{"tolerates a trailing slash", "https://sageox.ai/", "team_abc", "https://sageox.ai/team/team_abc"},
		{"no endpoint means no link", "", "team_abc", ""},
		{"no team means no link", "https://sageox.ai", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := teamDashboardURL(tc.ep, tc.teamID); got != tc.want {
				t.Errorf("teamDashboardURL(%q, %q) = %q, want %q", tc.ep, tc.teamID, got, tc.want)
			}
		})
	}
}

// TestTeamVisibility_EmptyTeamsIsAuthoritative pins the nil-vs-empty
// distinction, which JSON preserves and which decides whether the user gets a
// diagnosis at all.
//
// An omitted "teams" key means we learned nothing and must stay quiet — warning
// there would mark healthy repos broken, and a check that cries wolf is a check
// people learn to skip. But "teams": [] is the server stating this account
// belongs to no teams, which — with the repo bound to one — is exactly the
// failure this check exists to report.
func TestTeamVisibility_EmptyTeamsIsAuthoritative(t *testing.T) {
	var omitted, empty api.ReposResponse
	require.NoError(t, json.Unmarshal([]byte(`{"repos":{}}`), &omitted))
	require.NoError(t, json.Unmarshal([]byte(`{"repos":{},"teams":[]}`), &empty))

	require.Nil(t, omitted.Teams, "an omitted key must decode to nil — we learned nothing")
	require.NotNil(t, empty.Teams, `"teams": [] must decode to a non-nil empty slice — the server answered`)
	require.Empty(t, empty.Teams)

	// And the answer, once we have it, has to be actionable.
	cfg := &config.ProjectConfig{TeamID: "team_absent", TeamName: "Acme Corp"}
	detail := teamVisibilityDetail(cfg, empty.Teams, "https://sageox.ai")
	require.Contains(t, detail, "belongs to no teams")
	require.Contains(t, detail, "Acme Corp (team_absent)")
}

// TestTeamVisibilityVerdict covers the decision itself, including the
// nil-vs-empty branch that decides whether the user is diagnosed at all.
func TestTeamVisibilityVerdict(t *testing.T) {
	bound := &config.ProjectConfig{TeamID: "team_acme", TeamName: "Acme Corp"}

	t.Run("omitted memberships: stay quiet, we learned nothing", func(t *testing.T) {
		r := teamVisibilityVerdict(bound, nil, "https://sageox.ai")
		require.True(t, r.skipped, "warning on no data marks healthy repos broken")
	})

	t.Run("explicit empty list: diagnose, the server answered", func(t *testing.T) {
		r := teamVisibilityVerdict(bound, []api.TeamMembership{}, "https://sageox.ai")
		require.False(t, r.skipped, `"teams": [] is an answer, not an absence — it must not be skipped`)
		require.True(t, r.warning)
		require.Contains(t, r.detail, "belongs to no teams")
	})

	t.Run("bound team present: pass, named not id'd", func(t *testing.T) {
		r := teamVisibilityVerdict(bound, []api.TeamMembership{{ID: "team_acme", Name: "Acme Corp"}}, "https://sageox.ai")
		require.True(t, r.passed)
		require.False(t, r.warning, "the bound team is visible — nothing to warn about")
		require.Contains(t, r.message, "Acme Corp")
	})

	t.Run("bound to a personal team: warn, teammates cannot see it", func(t *testing.T) {
		r := teamVisibilityVerdict(bound, []api.TeamMembership{{ID: "team_acme", Name: "Acme Corp", Personal: true}}, "https://sageox.ai")
		require.True(t, r.warning, "a personal team means teammates never see these sessions")
		require.Contains(t, r.message, "personal team")
	})

	t.Run("bound team absent from a non-empty list: mismatch", func(t *testing.T) {
		r := teamVisibilityVerdict(bound, []api.TeamMembership{{ID: "team_other", Name: "Widgets"}}, "https://sageox.ai")
		require.True(t, r.warning)
		require.Contains(t, r.detail, "Widgets")
	})
}
