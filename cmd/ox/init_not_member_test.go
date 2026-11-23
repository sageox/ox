package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
)

func TestNotTeamMemberInitError(t *testing.T) {
	t.Parallel()
	cause := errors.New("HTTP 403 from https://sageox.ai/api/v1/cli/repo/init: you don't have access to this repository")
	tests := []struct {
		name    string
		teamID  string
		wantURL string
	}{
		{"real team id links the team page", "team_abc123", "https://sageox.ai/team/team_abc123"},
		{"no team id falls back to asking an admin", "", ""},
		// The id comes from a committed config anyone can edit; anything that
		// is not a team id must never become a printed link.
		{"forged id is not linked", "team_x/../../evil", ""},
		{"non-team id is not linked", "https://evil.example", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := notTeamMemberInitError(cause, "https://sageox.ai", tt.teamID)
			msg := err.Error()
			assert.Contains(t, msg, "not a member of the SageOx team this repository belongs to")
			assert.Contains(t, msg, "coding sessions in this repository will not be uploaded")
			assert.Contains(t, msg, "run ox init again")
			assert.ErrorIs(t, err, cause, "the server's message stays wrapped for debugging")
			if tt.wantURL != "" {
				assert.Contains(t, msg, "Open "+tt.wantURL+" and ask to join")
			} else {
				assert.NotContains(t, msg, "Open ")
				assert.Contains(t, msg, "Ask a team admin to add you")
			}
			if tt.teamID != "" && tt.wantURL == "" {
				assert.NotContains(t, msg, tt.teamID)
			}
		})
	}
}

// The 403 explanation must win even when --team matched none of the person's
// teams (the common outsider case: they name the repository's team, or a
// team they are not on), and the candidate hint must still be there.
func TestRegistrationFailureError(t *testing.T) {
	t.Parallel()
	forbidden := fmt.Errorf("HTTP 403 from https://sageox.ai/api/v1/cli/repo/init: no access: %w", api.ErrForbidden)
	other := errors.New("HTTP 500 from https://sageox.ai/api/v1/cli/repo/init: boom")
	candidates := []api.TeamMembership{{ID: "team_mine1", Name: "Mine"}}
	tests := []struct {
		name           string
		err            error
		candidates     []api.TeamMembership
		wantNotMember  bool
		wantCandidates bool
	}{
		{"403 with unmatched --team", forbidden, candidates, true, true},
		{"403 without --team candidates", forbidden, nil, true, false},
		{"other failure with unmatched --team", other, candidates, false, true},
		{"other failure", other, nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := registrationFailureError(tt.err, "https://sageox.ai", "team_abc123", " team_theirs ", tt.candidates)
			msg := err.Error()
			assert.ErrorIs(t, err, tt.err)
			assert.Equal(t, tt.wantNotMember, strings.Contains(msg, "will not be uploaded"))
			assert.Equal(t, !tt.wantNotMember, strings.HasPrefix(msg, "failed to register with SageOx API: "))
			assert.Equal(t, tt.wantCandidates, strings.Contains(msg, `--team "team_theirs" matched none of your teams`))
			if tt.wantCandidates {
				assert.Contains(t, msg, "Mine")
			}
		})
	}
}
