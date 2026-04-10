package main

import "testing"

// TestBuildDiscussionURL covers the failure modes that would silently produce
// broken citation links: missing inputs, prefixed endpoints, and edge characters.
func TestBuildDiscussionURL(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		teamID      string
		recordingID string
		want        string
	}{
		{
			name:        "happy path with https endpoint",
			endpoint:    "https://sageox.ai",
			teamID:      "team_jihjpfkt8b",
			recordingID: "rec_019d69d9-0243-706a-8455-080a47ffcba0",
			want:        "https://sageox.ai/team/team_jihjpfkt8b/media/recordings/rec_019d69d9-0243-706a-8455-080a47ffcba0",
		},
		{
			name:        "endpoint with api prefix is normalized",
			endpoint:    "https://api.sageox.ai",
			teamID:      "team_abc",
			recordingID: "rec_xyz",
			want:        "https://sageox.ai/team/team_abc/media/recordings/rec_xyz",
		},
		{
			name:        "endpoint with www prefix is normalized",
			endpoint:    "https://www.sageox.ai",
			teamID:      "team_abc",
			recordingID: "rec_xyz",
			want:        "https://sageox.ai/team/team_abc/media/recordings/rec_xyz",
		},
		{
			name:        "self-hosted endpoint preserved",
			endpoint:    "https://sageox.example.com",
			teamID:      "team_abc",
			recordingID: "rec_xyz",
			want:        "https://sageox.example.com/team/team_abc/media/recordings/rec_xyz",
		},
		{
			name:        "missing team_id returns empty (audio-only / legacy)",
			endpoint:    "https://sageox.ai",
			teamID:      "",
			recordingID: "rec_xyz",
			want:        "",
		},
		{
			name:        "missing recording_id returns empty",
			endpoint:    "https://sageox.ai",
			teamID:      "team_abc",
			recordingID: "",
			want:        "",
		},
		{
			name:        "missing endpoint returns empty",
			endpoint:    "",
			teamID:      "team_abc",
			recordingID: "rec_xyz",
			want:        "",
		},
		{
			name:        "all empty returns empty",
			endpoint:    "",
			teamID:      "",
			recordingID: "",
			want:        "",
		},
		{
			name:        "team_id with unusual characters is escaped",
			endpoint:    "https://sageox.ai",
			teamID:      "team with spaces",
			recordingID: "rec_xyz",
			want:        "https://sageox.ai/team/team%20with%20spaces/media/recordings/rec_xyz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDiscussionURL(tt.endpoint, tt.teamID, tt.recordingID)
			if got != tt.want {
				t.Errorf("buildDiscussionURL(%q, %q, %q)\n got: %q\nwant: %q",
					tt.endpoint, tt.teamID, tt.recordingID, got, tt.want)
			}
		})
	}
}
