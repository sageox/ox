package main

import "fmt"

// notTeamMemberInitError explains a 403 from repo registration: the person is
// signed in but is not a member of the team this repository belongs to, so
// SageOx will not take their coding sessions for it. The committed team name is
// deliberately not printed and the id is printed only when it is a well-formed
// team id: both come from the committed .sageox/config.json, which anyone who
// can push to the repository controls. The link is a path on the endpoint ox
// just called. The server's own message stays at the end for debugging.
func notTeamMemberInitError(cause error, ep, teamID string) error {
	join := "Ask a team admin to add you to the team"
	if teamID != "" && teamIDPattern.MatchString(teamID) {
		if u := teamDashboardURL(ep, teamID); u != "" {
			join = fmt.Sprintf("Open %s and ask to join, or ask a team admin to add you", u)
		}
	}
	return fmt.Errorf("you are not a member of the SageOx team this repository belongs to.\n"+
		"Your coding sessions in this repository will not be uploaded to SageOx until you join.\n"+
		"%s, then run ox init again.\n\n"+
		"(server said: %w)", join, cause)
}
