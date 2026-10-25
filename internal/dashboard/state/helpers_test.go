package state

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivityEntriesFiltersMurmursWithoutHidingOtherActivity(t *testing.T) {
	now := time.Now()
	s := Store{
		MurmurTopic: domain.MurmurFilterBlocked,
		MurmurQuery: "database",
		Murmurs: []domain.MurmurEntry{
			{AgentID: "team/a", Author: "Alice", Topic: "blocked", Content: "Database unavailable", Timestamp: now},
			{AgentID: "team/b", Author: "Bob", Topic: "wip", Content: "Database migration", Timestamp: now.Add(time.Minute)},
			{AgentID: "team/c", Author: "Carol", Topic: "blocked", Content: "Waiting for review", Timestamp: now.Add(2 * time.Minute)},
		},
		WhisperHistory: []domain.WhisperHistoryEntry{
			{AgentID: "team/d", Content: "independent event", CreatedAt: now.Add(3 * time.Minute), Delivered: true},
		},
	}

	entries := ActivityEntries(&s)
	require.Len(t, entries, 2)
	assert.Equal(t, domain.TimelineMurmur, entries[0].Kind)
	assert.Equal(t, "Alice", entries[0].Actor)
	assert.Equal(t, domain.TimelineAgent, entries[1].Kind, "murmur filters must not hide non-murmur activity")
	assert.Contains(t, entries[1].Summary, "✓")
}

func TestDaemonHealthLevelUsesWorstIssue(t *testing.T) {
	tests := []struct {
		name   string
		status *daemon.StatusData
		want   domain.HealthLevel
	}{
		{name: "not loaded", status: nil, want: domain.HealthUnknown},
		{name: "offline", status: &daemon.StatusData{}, want: domain.HealthError},
		{name: "healthy", status: &daemon.StatusData{Running: true}, want: domain.HealthOK},
		{name: "warning", status: &daemon.StatusData{Running: true, Issues: []daemon.DaemonIssue{{Severity: "warning"}}}, want: domain.HealthWarn},
		{name: "error wins", status: &daemon.StatusData{Running: true, Issues: []daemon.DaemonIssue{{Severity: "warning"}, {Severity: "error"}}}, want: domain.HealthError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DaemonHealthLevel(&Store{DaemonStatus: tt.status}))
		})
	}
}

func TestActiveMurmurCountsIgnoreExpiredEntriesAndDeduplicate(t *testing.T) {
	now := time.Now()
	s := Store{Murmurs: []domain.MurmurEntry{
		{AgentID: "team-a/one", Author: "Alice", Timestamp: now.Add(-time.Minute)},
		{AgentID: "team-a/one", Author: "Alice", Timestamp: now.Add(-2 * time.Minute)},
		{AgentID: "team-a/two", Author: "Bob", Timestamp: now.Add(-3 * time.Minute)},
		{AgentID: "old/agent", Author: "Old", Timestamp: now.Add(-31 * time.Minute)},
	}}

	assert.Equal(t, 2, ActiveMurmurCoworkers(&s))
	assert.Equal(t, 1, ActiveMurmurTeams(&s))
}

func TestBuildNavUsesStableWorkspaceOrder(t *testing.T) {
	s := Store{DaemonStatus: &daemon.StatusData{
		Running: true,
		Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"team-context": {{ID: "tc", TeamName: "Team context"}},
			"ledger":       {{ID: "ledger", TeamName: "Ledger"}},
		},
	}}

	first := BuildNav(&s)
	second := BuildNav(&s)
	require.Equal(t, first, second)

	var workspaceIDs []string
	for _, node := range first {
		if node.Kind == domain.NavNodeWorkspace {
			workspaceIDs = append(workspaceIDs, node.ID)
		}
	}
	assert.Equal(t, []string{"workspace-ledger", "workspace-tc"}, workspaceIDs)
}
