package main

import (
	"context"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/teamaccess"
)

// The Team Context checkout on disk outlives the right to read it: it stays
// after `ox logout`, after a login expires, and after someone leaves the team
// (until the daemon's next discovery pass removes it). A pasted link or id
// must never be enough to read it, so every command that surfaces a team's
// discussions asks teamaccess first and refuses before touching a file.

// checkTeamAccess is teamaccess.Check; a variable so command tests can script
// verdicts without a server.
var checkTeamAccess = teamaccess.Check

// teamAccessGate returns nil when the signed-in principal may read tc's
// content from this repo's endpoint, or the typed refusal otherwise.
func teamAccessGate(ctx context.Context, projectRoot string, tc *config.TeamContext) *read.Error {
	v := checkTeamAccess(ctx, endpoint.GetForProject(projectRoot), tc.TeamID)
	if v.Allowed() {
		return nil
	}
	label := tc.TeamName
	if label == "" {
		label = tc.TeamID
	}
	return read.NewError(teamAccessErrorCode(v.Status), v.Message(label))
}

// teamAccessErrorCode maps a refusal to its envelope code.
func teamAccessErrorCode(s teamaccess.Status) string {
	switch s {
	case teamaccess.NoAccess:
		return read.ErrCodeNoTeamAccess
	case teamaccess.Unverified:
		return read.ErrCodeAccessUnverified
	default:
		// NotSignedIn, Expired, EnvTokenMalformed: the credential is the
		// problem.
		return read.ErrCodeNotAuthenticated
	}
}

// conversationAccessGate gates the repo's team before a Reader exists. With
// no resolvable checkout there is nothing to read, and read.Open reports
// no_team_context on its own.
func conversationAccessGate(projectRoot string) *read.Error {
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
		return nil
	}
	return teamAccessGate(context.Background(), projectRoot, tc)
}
