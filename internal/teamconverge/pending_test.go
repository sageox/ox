package teamconverge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingRecord_SurvivesRestartAndCountsBoundedAttempts(t *testing.T) {
	project := t.TempDir()
	report := Report{Snapshot: Snapshot{Path: "/team", Commit: "abc"}, Outcomes: []Outcome{{
		Kind: KindSkill, Name: "deploy", State: StatePending, Required: true, Detail: "lock busy",
	}}}

	first, err := SavePending(project, PendingRetry, report, FailureReason(report))
	require.NoError(t, err)
	require.Equal(t, 1, first.Attempts)
	loaded, err := LoadPending(project)
	require.NoError(t, err)
	require.Equal(t, first.Status, loaded.Status)
	require.Equal(t, first.TeamCommit, loaded.TeamCommit)
	require.Equal(t, first.Attempts, loaded.Attempts)
	require.Equal(t, "lock busy", loaded.Reason)

	second, err := SavePending(project, PendingRetry, report, "still busy")
	require.NoError(t, err)
	require.Equal(t, 2, second.Attempts)
	require.NoError(t, ClearPending(project))
	loaded, err = LoadPending(project)
	require.NoError(t, err)
	require.Nil(t, loaded)
}

func TestPendingRecord_NewCommitResetsAttempts(t *testing.T) {
	project := t.TempDir()
	first := Report{Snapshot: Snapshot{Path: "/team", Commit: "one"}}
	second := Report{Snapshot: Snapshot{Path: "/team", Commit: "two"}}
	_, err := SavePending(project, PendingRetry, first, "first")
	require.NoError(t, err)
	record, err := SavePending(project, PendingRetry, second, "second")
	require.NoError(t, err)
	require.Equal(t, 1, record.Attempts)
}

func TestLoadPending_RejectsCorruptState(t *testing.T) {
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(PendingPath(project)), 0o700))
	require.NoError(t, os.WriteFile(PendingPath(project), []byte("not json"), 0o600))
	_, err := LoadPending(project)
	require.Error(t, err)
}

func TestPendingStatusFor_DistinguishesRetryFromHumanFailure(t *testing.T) {
	retry := Report{Outcomes: []Outcome{{State: StatePending, Required: true}}}
	require.Equal(t, PendingRetry, PendingStatusFor(retry))
	for _, state := range []OutcomeState{StateError, StateConflict, StatePendingApproval, StateUnsupported} {
		report := Report{Outcomes: []Outcome{{State: state, Required: true}}}
		require.Equal(t, PendingFailed, PendingStatusFor(report), state)
	}
}
