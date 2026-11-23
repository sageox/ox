package main

import (
	"errors"
	"testing"

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
