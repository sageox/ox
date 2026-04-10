package main

import (
	"fmt"
	"net/url"

	"github.com/sageox/ox/internal/endpoint"
)

// buildDiscussionURL constructs the canonical web URL for viewing a discussion
// recording. Returns empty string if any required input is missing — including
// recording_id, which is absent for legacy or audio-only discussions.
//
// Mirrors buildSessionURL in cmd/ox/session_url.go. Used by the citation
// pipeline to render clickable links in distilled summaries.
//
// URL shape: <endpoint>/team/<team_id>/media/recordings/<recording_id>
func buildDiscussionURL(ep, teamID, recordingID string) string {
	if teamID == "" || recordingID == "" {
		return ""
	}
	ep = endpoint.NormalizeEndpoint(ep)
	if ep == "" {
		return ""
	}
	return fmt.Sprintf("%s/team/%s/media/recordings/%s",
		ep,
		url.PathEscape(teamID),
		url.PathEscape(recordingID),
	)
}
