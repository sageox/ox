package effects

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loaderFake struct {
	err         error
	status      *daemon.StatusData
	sessions    []session.SessionInfo
	murmurs     []domain.MurmurEntry
	discussions []domain.TeamDiscussion
	instances   []daemon.InstanceInfo
	errors      []daemon.StoredError
	contexts    []domain.TeamContextEntry
	stats       *daemon.CodeDBStats
	whispers    []domain.WhisperHistoryEntry
}

func (f *loaderFake) GetDaemonStatus() (*daemon.StatusData, error) { return f.status, f.err }
func (f *loaderFake) ListSessions() ([]session.SessionInfo, error) { return f.sessions, f.err }
func (f *loaderFake) ListMurmurs() ([]domain.MurmurEntry, error)   { return f.murmurs, f.err }
func (f *loaderFake) ListTeamDiscussions() ([]domain.TeamDiscussion, error) {
	return f.discussions, f.err
}
func (f *loaderFake) ListInstances() ([]daemon.InstanceInfo, error) { return f.instances, f.err }
func (f *loaderFake) ListStoredErrors() ([]daemon.StoredError, error) {
	return f.errors, f.err
}
func (f *loaderFake) ListTeamContexts() ([]domain.TeamContextEntry, error) {
	return f.contexts, f.err
}
func (f *loaderFake) LoadCodeIndexStats() (*daemon.CodeDBStats, error) { return f.stats, f.err }
func (f *loaderFake) ListWhisperHistory() ([]domain.WhisperHistoryEntry, error) {
	return f.whispers, f.err
}
func (f *loaderFake) BuildSessionURL(string) string { return "" }

func TestLoadCommandsPreserveGenerationDataAndErrors(t *testing.T) {
	wantErr := errors.New("backend failed")
	fake := &loaderFake{
		err:         wantErr,
		status:      &daemon.StatusData{Pid: 42},
		sessions:    []session.SessionInfo{{Filename: "session.jsonl"}},
		murmurs:     []domain.MurmurEntry{{AgentID: "agent-1"}},
		discussions: []domain.TeamDiscussion{{Path: "memory/decision.md"}},
		instances:   []daemon.InstanceInfo{{AgentID: "agent-1"}},
		errors:      []daemon.StoredError{{Code: "sync_failed"}},
		contexts:    []domain.TeamContextEntry{{TeamSlug: "sageox"}},
		stats:       &daemon.CodeDBStats{Commits: 7},
		whispers:    []domain.WhisperHistoryEntry{{AgentID: "agent-1"}},
	}
	const generation = 9

	t.Run("daemon status", func(t *testing.T) {
		msg, ok := LoadDaemonStatusCmd(fake, generation)().(DaemonStatusLoadedMsg)
		require.True(t, ok)
		require.Same(t, fake.status, msg.Data)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("sessions", func(t *testing.T) {
		msg, ok := LoadSessionsCmd(fake, generation)().(SessionsLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.sessions, msg.Sessions)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("murmurs", func(t *testing.T) {
		msg, ok := LoadMurmursCmd(fake, generation)().(MurmursLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.murmurs, msg.Murmurs)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("discussions", func(t *testing.T) {
		msg, ok := LoadTeamDiscussionsCmd(fake, generation)().(TeamDiscussionsLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.discussions, msg.Discussions)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("instances", func(t *testing.T) {
		msg, ok := LoadInstancesCmd(fake, generation)().(InstancesLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.instances, msg.Instances)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("stored errors", func(t *testing.T) {
		msg, ok := LoadStoredErrorsCmd(fake, generation)().(StoredErrorsLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.errors, msg.Errors)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("team contexts", func(t *testing.T) {
		msg, ok := LoadTeamContextsCmd(fake, generation)().(TeamContextsLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.contexts, msg.TeamContexts)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("code index", func(t *testing.T) {
		msg, ok := LoadCodeIndexStatsCmd(fake, generation)().(CodeIndexStatsLoadedMsg)
		require.True(t, ok)
		require.Same(t, fake.stats, msg.Stats)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
	t.Run("whispers", func(t *testing.T) {
		msg, ok := LoadWhisperHistoryCmd(fake, generation)().(WhisperHistoryLoadedMsg)
		require.True(t, ok)
		assert.Equal(t, fake.whispers, msg.Entries)
		assert.Equal(t, generation, msg.Gen)
		assert.ErrorIs(t, msg.Err, wantErr)
	})
}
