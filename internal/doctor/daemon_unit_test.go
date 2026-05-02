package doctor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSplitCompositeID(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		wantRepo      string
		wantWorkspace string
	}{
		{
			name:          "standard composite id",
			id:            "repo_abc123_a1b2c3d4",
			wantRepo:      "repo_abc123",
			wantWorkspace: "a1b2c3d4",
		},
		{
			name:          "repo id without underscores",
			id:            "repoabc_workspace",
			wantRepo:      "repoabc",
			wantWorkspace: "workspace",
		},
		{
			name:          "multiple underscores in repo id",
			id:            "my_repo_name_ws123",
			wantRepo:      "my_repo_name",
			wantWorkspace: "ws123",
		},
		{
			name:          "no underscore returns empty",
			id:            "nounderscore",
			wantRepo:      "",
			wantWorkspace: "",
		},
		{
			name:          "empty string returns empty",
			id:            "",
			wantRepo:      "",
			wantWorkspace: "",
		},
		{
			name:          "leading underscore only returns empty",
			id:            "_workspace",
			wantRepo:      "",
			wantWorkspace: "",
		},
		{
			name:          "trailing underscore only returns empty",
			id:            "repo_",
			wantRepo:      "",
			wantWorkspace: "",
		},
		{
			name:          "single underscore in middle",
			id:            "a_b",
			wantRepo:      "a",
			wantWorkspace: "b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, workspace := splitCompositeID(tt.id)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantWorkspace, workspace)
		})
	}
}

func TestFormatDuration_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"zero seconds", 0, "0s"},
		{"one second", time.Second, "1s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"exactly 1 minute", time.Minute, "1m"},
		{"1 minute 30 seconds truncates to minutes", 90 * time.Second, "1m"},
		{"59 minutes", 59 * time.Minute, "59m"},
		{"exactly 1 hour", time.Hour, "1h"},
		{"1 hour 30 minutes truncates to hours", 90 * time.Minute, "1h"},
		{"23 hours", 23 * time.Hour, "23h"},
		{"exactly 24 hours", 24 * time.Hour, "1d"},
		{"48 hours", 48 * time.Hour, "2d"},
		{"36 hours truncates to days", 36 * time.Hour, "1d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDaemonDirtyTeamContextCheck_Name(t *testing.T) {
	check := NewDaemonDirtyTeamContextCheck()
	assert.Equal(t, "team context clean", check.Name())
}

func TestDaemonHeartbeatCheck_UnknownType(t *testing.T) {
	check := NewDaemonHeartbeatCheck("bogus_type", "some_id", "test", "sageox.ai")
	result := check.Run(context.Background(), false)

	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "unknown type", result.Message)
}

func TestDaemonHeartbeatCheck_InvalidWorkspaceIdentifier(t *testing.T) {
	// workspace type requires composite ID with underscore
	check := NewDaemonHeartbeatCheck("workspace", "nounderscore", "test", "sageox.ai")
	result := check.Run(context.Background(), false)

	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "invalid identifier format", result.Message)
}

func TestDaemonHeartbeatCheck_InvalidLedgerIdentifier(t *testing.T) {
	check := NewDaemonHeartbeatCheck("ledger", "nounderscore", "test", "sageox.ai")
	result := check.Run(context.Background(), false)

	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "invalid identifier format", result.Message)
}

func TestDaemonHeartbeatCheck_NameFormat(t *testing.T) {
	tests := []struct {
		displayName string
		wantName    string
	}{
		{"my-project", "heartbeat (my-project)"},
		{"ledger", "heartbeat (ledger)"},
		{"", "heartbeat ()"},
	}

	for _, tt := range tests {
		t.Run(tt.displayName, func(t *testing.T) {
			check := NewDaemonHeartbeatCheck("workspace", "id_ws", tt.displayName, "ep")
			assert.Equal(t, tt.wantName, check.Name())
		})
	}
}

func TestDaemonHeartbeatCheck_BothEmptySkip(t *testing.T) {
	// both empty identifier and endpoint should skip
	check := NewDaemonHeartbeatCheck("workspace", "", "", "")
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}
