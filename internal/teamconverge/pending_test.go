package teamconverge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestAutomaticRetryAllowed_RequiresRetryableSameTeamWithinBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record *PendingRecord
		team   string
		want   bool
	}{
		{name: "missing record"},
		{name: "retryable work", record: &PendingRecord{
			Status: PendingRetry, TeamPath: "/team", Attempts: MaxAutomaticConvergenceAttempts - 1,
		}, team: "/team", want: true},
		{name: "settled failure", record: &PendingRecord{
			Status: PendingFailed, TeamPath: "/team", Attempts: 1,
		}, team: "/team"},
		{name: "budget exhausted", record: &PendingRecord{
			Status: PendingRetry, TeamPath: "/team", Attempts: MaxAutomaticConvergenceAttempts,
		}, team: "/team"},
		{name: "different team", record: &PendingRecord{
			Status: PendingRetry, TeamPath: "/other", Attempts: 1,
		}, team: "/team"},
		{name: "empty team path", record: &PendingRecord{
			Status: PendingRetry, TeamPath: "", Attempts: 1,
		}, team: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, AutomaticRetryAllowed(tt.record, tt.team))
		})
	}
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

func TestPendingHelpers_ReportMalformedAndFilesystemState(t *testing.T) {
	t.Run("unsupported schema", func(t *testing.T) {
		project := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Dir(PendingPath(project)), 0o700))
		require.NoError(t, os.WriteFile(PendingPath(project), []byte(`{"schema_version":99}`), 0o600))
		_, err := LoadPending(project)
		require.ErrorContains(t, err, "unsupported")
	})

	t.Run("pending path is unreadable", func(t *testing.T) {
		project := t.TempDir()
		require.NoError(t, os.MkdirAll(PendingPath(project), 0o700))
		_, err := LoadPending(project)
		require.ErrorContains(t, err, "read Team Context convergence state")
	})

	t.Run("project root cannot contain cache", func(t *testing.T) {
		project := filepath.Join(t.TempDir(), "project-file")
		require.NoError(t, os.WriteFile(project, []byte("not a directory"), 0o600))
		_, err := SavePending(project, PendingRetry, Report{}, "retry")
		require.ErrorContains(t, err, "create Team Context convergence cache")
	})
}

func TestFailureReason_CoversTypedFallbacks(t *testing.T) {
	require.Equal(t, "pending: skill/deploy", FailureReason(Report{Outcomes: []Outcome{{
		State: StatePending, Kind: KindSkill, Name: "deploy",
	}}}))
	require.Equal(t, "unsupported: tool/deploy", FailureReason(Report{Outcomes: []Outcome{{
		State: StateUnsupported, Kind: KindTool, Name: "deploy", Required: true,
	}}}))
	require.Equal(t, "explicit detail", FailureReason(Report{Outcomes: []Outcome{{
		State: StateUnsupported, Kind: KindTool, Name: "deploy", Required: true, Detail: "explicit detail",
	}}}))
	require.Equal(t, "Team Context convergence is incomplete", FailureReason(Report{Outcomes: []Outcome{{
		State: StateUnsupported, Required: false,
	}}}))
}

type convergenceFailWriter struct{}

func (convergenceFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteText_EmptyAndWriterFailures(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, WriteText(&buf, Report{Snapshot: Snapshot{Commit: "123456789012345"}}))
	require.Contains(t, buf.String(), "123456789012")
	require.Contains(t, buf.String(), "No applicable artifacts")

	err := WriteText(convergenceFailWriter{}, Report{})
	require.ErrorContains(t, err, "write failed")
	err = WriteText(io.MultiWriter(convergenceFailWriter{}), Report{Outcomes: []Outcome{{Kind: KindRule, Name: "x"}}})
	require.ErrorContains(t, err, "write failed")

	retry := &RetryableError{Err: errors.New("busy")}
	require.EqualError(t, retry, "busy")
	require.ErrorIs(t, retry, retry.Err)
}
