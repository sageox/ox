package read

import (
	"strings"
	"testing"
)

func TestShow(t *testing.T) {
	tests := []struct {
		name             string
		id               string
		wantTitle        string
		wantAvailable    bool
		wantSummarySub   string
		wantReason       string
		wantParticipants []string
	}{
		{
			name:          "summary.json with participants",
			id:            fullCnv,
			wantTitle:     "2026-08-11-22-32-full", // index + metadata titles empty → folder name (D13)
			wantAvailable: true, wantSummarySub: "forward deployed engineer",
			wantParticipants: []string{"Galex Yen", "Ryan"}, // summary participants win, unnamed row skipped
		},
		{
			name:          "rec id form works identically",
			id:            fullRec,
			wantTitle:     "2026-08-11-22-32-full",
			wantAvailable: true, wantSummarySub: "forward deployed engineer",
			wantParticipants: []string{"Galex Yen", "Ryan"},
		},
		{
			name:          "summary.md fallback for pre-JSON folders",
			id:            legacyCnv,
			wantTitle:     "Legacy Era Discussion",
			wantAvailable: true, wantSummarySub: "Hand-written era summary",
			wantParticipants: []string{"Casey"},
		},
		{
			name:       "missing summary is data not error",
			id:         bothCnv,
			wantTitle:  "Both Manifests",
			wantReason: SummaryReasonNotYetGenerated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testReader(t).Show(tt.id)
			if !env.Success {
				t.Fatalf("Show(%s) failed: %+v", tt.id, env.Error)
			}
			data := env.Data.(*ShowData)
			if data.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", data.Title, tt.wantTitle)
			}
			if data.Summary.Available != tt.wantAvailable {
				t.Errorf("Summary.Available = %v, want %v", data.Summary.Available, tt.wantAvailable)
			}
			if tt.wantSummarySub != "" && !strings.Contains(data.Summary.HumanSummary, tt.wantSummarySub) {
				t.Errorf("HumanSummary %q missing %q", data.Summary.HumanSummary, tt.wantSummarySub)
			}
			if data.Summary.Reason != tt.wantReason {
				t.Errorf("Summary.Reason = %q, want %q", data.Summary.Reason, tt.wantReason)
			}
			if len(tt.wantParticipants) > 0 {
				if len(data.Participants) != len(tt.wantParticipants) {
					t.Fatalf("Participants = %v, want %v", data.Participants, tt.wantParticipants)
				}
				for i, p := range tt.wantParticipants {
					if data.Participants[i] != p {
						t.Errorf("Participants = %v, want %v", data.Participants, tt.wantParticipants)
					}
				}
			}
			if data.ConversationID == "" || data.RecordingID == "" {
				t.Errorf("id twins missing: %+v", data)
			}
			if env.Guidance == "" || !strings.Contains(env.Guidance, data.ConversationID) {
				t.Errorf("guidance %q does not teach the next rung for %s", env.Guidance, data.ConversationID)
			}
		})
	}
}

func TestShowTypedErrors(t *testing.T) {
	r := testReader(t)
	tests := []struct {
		name     string
		id       string
		wantCode string
	}{
		{name: "invalid id", id: "not-an-id", wantCode: ErrCodeInvalidID},
		{name: "valid but unindexed", id: unknownCnv, wantCode: ErrCodeNotIndexed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := r.Show(tt.id)
			if env.Success || env.Error == nil {
				t.Fatalf("Show(%q) succeeded, want %s", tt.id, tt.wantCode)
			}
			if env.Error.Code != tt.wantCode {
				t.Errorf("code = %s, want %s", env.Error.Code, tt.wantCode)
			}
		})
	}
}
