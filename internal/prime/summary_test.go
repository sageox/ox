package prime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShortenPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "short path", path: "a/b/c", want: "a/b/c"},
		{name: "long path", path: "/home/user/.local/share/sageox/ledger", want: ".../share/sageox/ledger"},
		{name: "exact 3 parts", path: "one/two/three", want: "one/two/three"},
		{name: "single part", path: "file.txt", want: "file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortenPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildHumanSummary(t *testing.T) {
	tests := []struct {
		name     string
		output   Output
		contains []string
	}{
		{
			name: "minimal output",
			output: Output{
				AgentID: "agent-42",
				Status:  "fresh",
			},
			contains: []string{"agent-42", "fresh"},
		},
		{
			name: "with session and team context",
			output: Output{
				AgentID:        "agent-99",
				SessionID:      "sess-123",
				AgentType:      "claude-code",
				Status:         "fresh",
				AgentSupported: true,
				Session: &SessionStatus{
					Recording: true,
					Mode:      "infra",
				},
				TeamContext: &TeamContextInfo{
					TeamID: "team-abc",
				},
			},
			contains: []string{"agent-99", "sess-123", "claude-code", "supported", "Recording", "infra", "team-abc"},
		},
		{
			name: "with guidance and token estimate",
			output: Output{
				AgentID:       "agent-1",
				Status:        "fresh",
				TokenEstimate: 5000,
				Guidance: &Guidance{
					Commands: []IntentCommand{
						{Intent: "test", Command: "ox test"},
						{Intent: "check", Command: "ox check"},
					},
				},
			},
			contains: []string{"5000", "2 intent-to-command"},
		},
		{
			name: "with doctor and hooks",
			output: Output{
				AgentID:            "agent-1",
				Status:             "fresh",
				NeedsDoctorAgent:   true,
				DoctorHint:         "Run ox agent doctor",
				HooksInstalled:     true,
				HooksRestartNotice: "Restart required",
			},
			contains: []string{"Doctor Attention Needed", "Hooks Installed"},
		},
		{
			name: "with update available",
			output: Output{
				AgentID:         "agent-1",
				Status:          "fresh",
				UpdateAvailable: true,
				UpdateHint:      "v0.8.0 -> v0.9.0",
			},
			contains: []string{"Update Available", "v0.8.0 -> v0.9.0"},
		},
		{
			name: "ledger not provisioned",
			output: Output{
				AgentID: "agent-1",
				Status:  "fresh",
				Ledger:  &LedgerInfo{Exists: false},
			},
			contains: []string{"Not provisioned"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildHumanSummary(tt.output)
			for _, c := range tt.contains {
				assert.Contains(t, got, c)
			}
		})
	}
}
