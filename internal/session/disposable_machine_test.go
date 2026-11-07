package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/proc"
	"github.com/stretchr/testify/require"
)

// useDisposableMachine points the user config at a file with the
// disposable_machine setting on or off.
func useDisposableMachine(t *testing.T, on bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "disposable_machine: %v\n", on), 0o600))
	t.Setenv("OX_USER_CONFIG", path)
}

// Failure prevented: a recording left on a persistent home by a machine that
// went away names a parent PID that an unrelated process holds on the next
// machine. The recording reads as live for as long as that process runs, so
// it is never cleaned up or finalized, and HasLiveRecording defers skill and
// rule sync for good.
func TestRecordingLiveness_ParentPIDTakenByAnotherProcess(t *testing.T) {
	start, ok := proc.StartTime(os.Getpid())
	if !ok {
		t.Skip("this platform cannot report a process start time")
	}
	for _, tc := range []struct {
		name        string
		parentStart int64
		live        bool
	}{
		{"the process that started the recording", start, true},
		{"another process under the same PID", start - 1, false},
		{"no start time stored", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, sessionsBase := setupRecordingTestWithSessionsBase(t, t.TempDir())
			state := &RecordingState{
				AgentID: "OxReuse", StartedAt: time.Now(), ParentPID: os.Getpid(), ParentStartTime: tc.parentStart,
				SessionPath: filepath.Join(sessionsBase, "reuse"),
			}
			require.NoError(t, SaveRecordingState(project, state))

			require.Equal(t, tc.live, state.IsAgentAlive())
			live, err := HasLiveRecording(project)
			require.NoError(t, err)
			require.Equal(t, tc.live, live)
		})
	}
}

// Failure prevented: without the setting, recordings on a laptop gain a field
// that changes their liveness checks; with it, the start time is missing and a
// reused PID goes undetected.
func TestStartRecording_StoresParentStartTimeOnlyOnADisposableMachine(t *testing.T) {
	start, ok := proc.StartTime(os.Getpid())
	if !ok {
		t.Skip("this platform cannot report a process start time")
	}
	for _, disposable := range []bool{false, true} {
		t.Run(fmt.Sprintf("disposable_machine=%v", disposable), func(t *testing.T) {
			project := setupRecordingTest(t, t.TempDir())
			useDisposableMachine(t, disposable)

			state, err := StartRecording(project, StartRecordingOptions{AgentID: "OxStart", ParentPID: os.Getpid()})
			require.NoError(t, err)

			want := int64(0)
			if disposable {
				want = start
			}
			require.Equal(t, want, state.ParentStartTime)
			saved, err := LoadRecordingStateForAgent(project, "OxStart")
			require.NoError(t, err)
			require.Equal(t, want, saved.ParentStartTime)
		})
	}
}
