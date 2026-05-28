package main

import (
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pauseResumeProject builds a project the same way the suspended-nudge tests do
// and additionally sets OX_PROJECT_ROOT so the runAgentSession{Pause,Resume}
// helpers resolve to this project without depending on the test's CWD.
func pauseResumeProject(t *testing.T) string {
	t.Helper()
	root := suspendedNudgeProject(t)
	t.Setenv("OX_PROJECT_ROOT", root)
	// cfg is normally initialized by PersistentPreRunE on the root cobra cmd;
	// in unit tests we set a zero-value config so emitPause/Resume output's
	// cfg.Text / cfg.Review checks don't nil-deref.
	prev := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = prev })
	return root
}

// --- ADR-020 TOCTOU regression tests (PR #627 follow-up) ---
// Failure prevented: the pause/resume commands originally read
// state.EntryCount BEFORE calling UpdateRecordingStateForAgent, while the
// canonical lifecycle Seq is set inside that update's load-modify-save
// window. When a tail-watcher (or any other writer) appended entries
// between the pre-read and the update, the persisted Pause/Resume Seq
// trailed the actual EntryCount being saved — and paused-range entries
// silently leaked across the boundary into the upload at stop time.
//
// The fix captures both seq AND the excluded-entry count INSIDE the
// update closure. These tests pin the contract: every persisted
// LifecycleEvent.Seq must equal the EntryCount the state was saved with.

// TestPause_SeqMatchesPersistedEntryCount — Failure prevented: pause writes
// a LifecycleEvent.Seq that does not match the EntryCount visible to the
// next reader, leaving the segment-mask logic with a stale boundary.
func TestPause_SeqMatchesPersistedEntryCount(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	agentID := "Oxpause1"

	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.EntryCount = 42 // simulate prior tail-watcher appends
	}))
	_ = state

	inst := &agentinstance.Instance{AgentID: agentID}
	require.NoError(t, runAgentSessionPause(inst, nil))

	got, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.SuspendedAt)
	require.NotEmpty(t, got.Lifecycle)
	last := got.Lifecycle[len(got.Lifecycle)-1]
	assert.Equal(t, session.LifecycleActionPause, last.Action)
	assert.Equal(t, got.EntryCount, last.Seq,
		"pause LifecycleEvent.Seq MUST equal the EntryCount the state is saved with — anything else means the upload mask boundary is stale")
}

// TestResume_SeqMatchesPersistedEntryCount — Failure prevented: resume
// writes a LifecycleEvent.Seq < persisted EntryCount, so entries appended
// while still SuspendedAt != nil leak into the post-resume range and ship
// to the ledger.
func TestResume_SeqMatchesPersistedEntryCount(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	agentID := "Oxresume1"

	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)

	// pause first so resume has something to do.
	require.NoError(t, runAgentSessionPause(&agentinstance.Instance{AgentID: agentID}, nil))

	// simulate tail-watcher appends DURING the paused window.
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.EntryCount += 17
	}))

	require.NoError(t, runAgentSessionResume(&agentinstance.Instance{AgentID: agentID}, nil))

	got, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.SuspendedAt, "resume must clear SuspendedAt")

	// find the most recent resume event
	var resumeSeq int
	var found bool
	for i := len(got.Lifecycle) - 1; i >= 0; i-- {
		if got.Lifecycle[i].Action == session.LifecycleActionResume {
			resumeSeq = got.Lifecycle[i].Seq
			found = true
			break
		}
	}
	require.True(t, found, "resume lifecycle event missing")
	assert.Equal(t, got.EntryCount, resumeSeq,
		"resume LifecycleEvent.Seq MUST equal the EntryCount the state is saved with — otherwise paused-range entries leak across the boundary")
}

// TestPauseResume_ConcurrentAppendsKeepLifecycleConsistent — Failure
// prevented: under a tail-watcher race, EntryCount drifts between
// pre-read and persisted state, leaving Pause Seq < Resume Seq and a
// false "excluded entries" count. With seq captured inside the closure
// the relationship Pause Seq <= Resume Seq <= persisted EntryCount
// always holds.
func TestPauseResume_ConcurrentAppendsKeepLifecycleConsistent(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	agentID := "Oxrace1"

	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)

	// Simulate tail-watcher appends sequentially around the pause/resume
	// CLI calls. The TOCTOU fix being pinned is that the CLI captures seq
	// INSIDE UpdateRecordingStateForAgent's closure — so the persisted
	// LifecycleEvent.Seq always matches the EntryCount the state is saved
	// with, regardless of what value EntryCount had when the CLI was
	// invoked.
	//
	// We deliberately do NOT run the appender as a concurrent goroutine
	// against the CLI: SaveRecordingState uses a non-atomic os.WriteFile
	// and concurrent writers would clobber each other's Lifecycle slice.
	// That's a separate, real production race (tracked separately); this
	// test focuses on the CLI's pre-read TOCTOU.
	appender := func(n int) {
		t.Helper()
		for range n {
			require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
				s.EntryCount++
			}))
		}
	}

	appender(7) // tail-watcher appends BEFORE pause
	inst := &agentinstance.Instance{AgentID: agentID}
	require.NoError(t, runAgentSessionPause(inst, nil))
	appender(5) // tail-watcher appends DURING the paused window
	require.NoError(t, runAgentSessionResume(inst, nil))
	appender(3) // tail-watcher appends AFTER resume

	got, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, got)

	var pauseSeq, resumeSeq int
	var sawPause, sawResume bool
	for _, ev := range got.Lifecycle {
		switch ev.Action {
		case session.LifecycleActionPause:
			pauseSeq = ev.Seq
			sawPause = true
		case session.LifecycleActionResume:
			resumeSeq = ev.Seq
			sawResume = true
		}
	}
	require.True(t, sawPause, "pause event missing")
	require.True(t, sawResume, "resume event missing")
	assert.LessOrEqual(t, pauseSeq, resumeSeq,
		"pauseSeq must be <= resumeSeq; otherwise the excluded range is meaningless")
	assert.LessOrEqual(t, resumeSeq, got.EntryCount,
		"resumeSeq must be <= persisted EntryCount; otherwise resume claims entries that never existed")
	// Specifically: pause must record seq=7 (count at pause time), resume
	// must record seq=12 (count after 5 appends during the paused window),
	// and persisted EntryCount must be 15 (12 + 3 post-resume appends).
	assert.Equal(t, 7, pauseSeq, "pause seq must equal EntryCount at moment of pause")
	assert.Equal(t, 12, resumeSeq, "resume seq must equal EntryCount at moment of resume")
	assert.Equal(t, 15, got.EntryCount, "final EntryCount must reflect all 15 sequential appends")
}
