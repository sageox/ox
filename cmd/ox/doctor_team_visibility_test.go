package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
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
