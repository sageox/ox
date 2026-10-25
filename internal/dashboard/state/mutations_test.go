package state

import (
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyDaemonStatusTracksSuccessAndFailure(t *testing.T) {
	oldLoadedAt := time.Unix(123, 0)
	initial := Store{IsLoading: true, DaemonLoadedAt: oldLoadedAt}
	status := &daemon.StatusData{Running: true, Pid: 42}

	before := time.Now()
	got := ApplyDaemonStatus(initial, status, nil)
	after := time.Now()

	require.Same(t, status, got.DaemonStatus)
	assert.NoError(t, got.DaemonErr)
	assert.False(t, got.IsLoading)
	assert.False(t, got.DaemonLoadedAt.Before(before))
	assert.False(t, got.DaemonLoadedAt.After(after))
	assert.True(t, initial.IsLoading, "mutation must not alter the input value")
	assert.Equal(t, oldLoadedAt, initial.DaemonLoadedAt)

	wantErr := errors.New("daemon unavailable")
	failed := ApplyDaemonStatus(initial, nil, wantErr)
	assert.ErrorIs(t, failed.DaemonErr, wantErr)
	assert.True(t, failed.IsLoading, "a failed initial load must remain visibly loading")
	assert.Equal(t, oldLoadedAt, failed.DaemonLoadedAt, "failures must not claim fresh data")
}

func TestApplyDataResultsPreservePayloadAndError(t *testing.T) {
	wantErr := errors.New("load failed")
	sessions := []session.SessionInfo{{Filename: "session.jsonl"}}
	murmurs := []domain.MurmurEntry{{AgentID: "agent-1"}}
	discussions := []domain.TeamDiscussion{{Path: "memory/decision.md"}}
	instances := []daemon.InstanceInfo{{AgentID: "agent-1"}}
	storedErrors := []daemon.StoredError{{Code: "sync_failed"}}
	contexts := []domain.TeamContextEntry{{TeamSlug: "sageox"}}
	stats := &daemon.CodeDBStats{Commits: 7}
	whispers := []domain.WhisperHistoryEntry{{AgentID: "agent-1"}}

	s := ApplySessions(Store{}, sessions, wantErr)
	assert.Equal(t, sessions, s.Sessions)
	assert.ErrorIs(t, s.SessionsErr, wantErr)
	assert.True(t, s.SessionsLoadedAt.IsZero(), "failed loads must not advance freshness")

	s = ApplyMurmurs(s, murmurs, wantErr)
	s = ApplyDiscussions(s, discussions, wantErr)
	s = ApplyInstances(s, instances, wantErr)
	s = ApplyStoredErrors(s, storedErrors, wantErr)
	s = ApplyTeamContexts(s, contexts, wantErr)
	s = ApplyCodeIndexStats(s, stats, wantErr)
	s = ApplyWhisperHistory(s, whispers, wantErr)

	assert.Equal(t, murmurs, s.Murmurs)
	assert.ErrorIs(t, s.MurmursErr, wantErr)
	assert.Equal(t, discussions, s.Discussions)
	assert.ErrorIs(t, s.DiscussionsErr, wantErr)
	assert.Equal(t, instances, s.Instances)
	assert.ErrorIs(t, s.InstancesErr, wantErr)
	assert.Equal(t, storedErrors, s.StoredErrors)
	assert.ErrorIs(t, s.StoredErrorsErr, wantErr)
	assert.Equal(t, contexts, s.TeamContexts)
	assert.ErrorIs(t, s.TeamContextsErr, wantErr)
	require.Same(t, stats, s.CodeIndexStats)
	assert.ErrorIs(t, s.CodeIndexStatsErr, wantErr)
	assert.Equal(t, whispers, s.WhisperHistory)
	assert.ErrorIs(t, s.WhisperHistoryErr, wantErr)
}

func TestCursorMutationsClampToAvailableRows(t *testing.T) {
	tests := []struct {
		name  string
		start int
		delta int
		max   int
		want  int
	}{
		{name: "moves within range", start: 1, delta: 1, max: 4, want: 2},
		{name: "clamps below zero", start: 0, delta: -5, max: 4, want: 0},
		{name: "clamps above final row", start: 2, delta: 10, max: 4, want: 3},
		{name: "empty list resets stale cursor", start: 3, delta: 0, max: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nav := MoveNavCursor(Store{NavCursorPos: tt.start}, tt.delta, tt.max)
			timeline := MoveTimelineCursor(Store{TimelineCursorPos: tt.start}, tt.delta, tt.max)
			assert.Equal(t, tt.want, nav.NavCursorPos)
			assert.Equal(t, tt.want, timeline.TimelineCursorPos)
		})
	}
}

func TestMurmurFiltersResetDependentState(t *testing.T) {
	initial := Store{TimelineCursorPos: 4, MurmurQuery: "sync", MurmurQueryOpen: true}

	filtered := SetMurmurFilter(initial, domain.MurmurFilterBlocked)
	assert.Equal(t, domain.MurmurFilterBlocked, filtered.MurmurTopic)
	assert.Zero(t, filtered.TimelineCursorPos)

	searched := SetMurmurSearch(initial, "review")
	assert.Equal(t, "review", searched.MurmurQuery)
	assert.Zero(t, searched.TimelineCursorPos)

	closed := SetMurmurSearchActive(initial, false)
	assert.False(t, closed.MurmurQueryOpen)
	assert.Empty(t, closed.MurmurQuery, "closing search must remove the hidden filter")
	assert.Equal(t, "sync", initial.MurmurQuery, "mutation must not alter the input value")
}

func TestStoreLifecycleAndInspector(t *testing.T) {
	target := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	s := ApplySelection(Store{}, target)
	require.Same(t, target, s.Selected)
	assert.Equal(t, domain.TargetCodeDB, s.Inspector().Kind)

	s = IncrementGeneration(s)
	s = IncrementGeneration(s)
	s = SetLoading(s)
	s = SetStatusMessage(s, "refreshing")
	assert.Equal(t, 2, s.Generation)
	assert.True(t, s.Loading())
	assert.Equal(t, "refreshing", s.StatusMessage())

	cleared := ApplySelection(s, nil)
	assert.Equal(t, domain.TargetNone, cleared.Inspector().Kind)
}
