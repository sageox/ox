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

// TestPendingRecord_UnknownCommitOnErrorPathDoesNotResetAttempts covers the
// class of bug where the automatic retry budget (MaxAutomaticConvergenceAttempts)
// fails to bound anything: a convergence that errors before discovery
// completes (e.g. a busy git lock) has no commit to report and previously
// saved Snapshot.Commit == "", while a convergence that ran but reported
// pending work saved the real commit. Keying attempts on the (path, commit)
// pair let that legitimate gap in knowledge look like a changed commit and
// silently reset Attempts to 1 on every alternation — so a repository stuck
// alternating between the two never reached the cap and never stopped
// auto-retrying every daemon tick.
func TestPendingRecord_UnknownCommitOnErrorPathDoesNotResetAttempts(t *testing.T) {
	project := t.TempDir()
	errorPath := Report{Snapshot: Snapshot{Path: "/team", Commit: ""}}
	pendingPath := Report{Snapshot: Snapshot{Path: "/team", Commit: "real-commit"}}

	// First attempt: discovery fails before a commit is known.
	record, err := SavePending(project, PendingRetry, errorPath, "lock busy")
	require.NoError(t, err)
	require.Equal(t, 1, record.Attempts)
	require.Empty(t, record.TeamCommit)

	// Second attempt: convergence ran and reported pending work at the real
	// commit. This must count as attempt 2 of the SAME episode, not reset.
	record, err = SavePending(project, PendingRetry, pendingPath, "still pending")
	require.NoError(t, err)
	require.Equal(t, 2, record.Attempts, "a newly-observed commit after an unknown one must not reset the budget")
	require.Equal(t, "real-commit", record.TeamCommit)

	// Third attempt: the daemon carries the last known commit forward on the
	// error path (as internal/daemon/sync_team_skills.go does), so this also
	// must not reset — and reaches the automatic budget cap.
	record, err = SavePending(project, PendingRetry, Report{Snapshot: Snapshot{Path: "/team", Commit: "real-commit"}}, "lock busy again")
	require.NoError(t, err)
	require.Equal(t, MaxAutomaticConvergenceAttempts, record.Attempts)
	require.False(t, AutomaticRetryAllowed(record, "/team"), "the automatic budget must be exhausted, not reset by the alternation")

	// A fourth attempt at the same commit keeps counting past the cap rather
	// than resetting — SavePending only records; the daemon decides not to
	// retry via AutomaticRetryAllowed above.
	record, err = SavePending(project, PendingRetry, pendingPath, "still pending")
	require.NoError(t, err)
	require.Equal(t, MaxAutomaticConvergenceAttempts+1, record.Attempts)
	require.False(t, AutomaticRetryAllowed(record, "/team"))

	// A genuinely new, non-empty commit is materially new work and resets the budget.
	record, err = SavePending(project, PendingRetry, Report{Snapshot: Snapshot{Path: "/team", Commit: "new-commit"}}, "fresh work")
	require.NoError(t, err)
	require.Equal(t, 1, record.Attempts, "a real new commit must still reset the budget")
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
	require.Equal(t, "unsupported: skill/deploy", FailureReason(Report{Outcomes: []Outcome{{
		State: StateUnsupported, Kind: KindSkill, Name: "deploy", Required: true,
	}}}))
	require.Equal(t, "explicit detail", FailureReason(Report{Outcomes: []Outcome{{
		State: StateUnsupported, Kind: KindSkill, Name: "deploy", Required: true, Detail: "explicit detail",
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
}
