package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/require"
)

// never dialed: the clone budgets in these tests are spent before git starts
const unreachableTeamCloneURL = "http://127.0.0.1:1/team.git"

// fakeCloneClock is a hand-driven clock for clone retry windows.
type fakeCloneClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeCloneClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeCloneClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newBackoffScheduler is a scheduler that knows one team context that is not
// cloned yet, with the clone retry clock and the log under the test's control.
func newBackoffScheduler(t *testing.T, cloneURL string) (s *SyncScheduler, teamID, teamPath string, clock *fakeCloneClock, logs *lockedBuffer) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentialsWithDir(t)
	s = newTestScheduler(setupProjectWithConfig(t, ""))
	logs = &lockedBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clock = &fakeCloneClock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	s.cloneClock = clock.Now

	teamID = "team_backoff"
	teamPath = filepath.Join(t.TempDir(), "teams", teamID)
	s.workspaceRegistry.mu.Lock()
	s.workspaceRegistry.workspaces[teamID] = &WorkspaceState{
		ID: teamID, Type: WorkspaceTypeTeamContext, TeamID: teamID, TeamName: "Backoff Team",
		Path: teamPath, CloneURL: cloneURL,
	}
	s.workspaceRegistry.mu.Unlock()
	t.Cleanup(s.cloneWg.Wait)
	return s, teamID, teamPath, clock, logs
}

// triggerAndWait is one sync cycle's anti-entropy pass, joined so the clone it
// may have started has finished.
func triggerAndWait(s *SyncScheduler) {
	s.triggerMissingClones(context.Background())
	s.cloneWg.Wait()
}

// TestCloneBackoffDelay verifies the retry schedule: 1m, doubling, and the
// cap for each kind of clone and failure.
func TestCloneBackoffDelay(t *testing.T) {
	tests := []struct {
		name      string
		repoType  string
		permanent bool
		failures  int
		want      time.Duration
	}{
		{"team 1st failure", "team-context", false, 1, time.Minute},
		{"team 2nd failure", "team-context", false, 2, 2 * time.Minute},
		{"team 3rd failure", "team-context", false, 3, 4 * time.Minute},
		{"team 4th failure", "team-context", false, 4, 8 * time.Minute},
		{"team 5th failure", "team-context", false, 5, 16 * time.Minute},
		{"team 6th failure hits the 30m cap (32m uncapped)", "team-context", false, 6, 30 * time.Minute},
		{"team stays at the cap", "team-context", false, 7, 30 * time.Minute},
		{"team far past the cap", "team-context", false, 60, 30 * time.Minute},
		{"ledger keeps its 1h cap", "ledger", false, 7, time.Hour},
		{"ledger 6th failure is uncapped 32m", "ledger", false, 6, 32 * time.Minute},
		{"permanent team error caps at 5m", "team-context", true, 6, 5 * time.Minute},
		{"permanent ledger error caps at 5m", "ledger", true, 6, 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, cloneBackoffDelay(tt.repoType, tt.permanent, tt.failures))
		})
	}
}

func TestShortDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{4 * time.Minute, "4m"},
		{4*time.Minute - 80*time.Millisecond, "4m"},
		{90 * time.Second, "1m30s"},
		{10 * time.Second, "10s"},
		{30 * time.Minute, "30m"},
		{time.Hour, "1h"},
		{time.Hour + 5*time.Minute, "1h5m"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, shortDuration(tt.in))
	}
}

// TestTeamContextClone_BudgetTimeoutIsFailureNotLockBusy verifies a team
// context clone that runs out of its budget comes back as a timeout failure.
// Failure prevented: the budget's "context deadline exceeded" was read as
// another clone holding the lock, so the clone was retried every sync cycle
// (~70s apart, 150+ times in one live daemon log) with no failure recorded.
func TestTeamContextClone_BudgetTimeoutIsFailureNotLockBusy(t *testing.T) {
	setCloneBudgets(t, time.Nanosecond, time.Minute)
	s, _, teamPath, _, _ := newBackoffScheduler(t, unreachableTeamCloneURL)

	_, err := s.Checkout(CheckoutPayload{CloneURL: unreachableTeamCloneURL, RepoPath: teamPath, RepoType: "team-context"}, nil)

	require.Error(t, err)
	require.False(t, gitutil.IsPreCloneLockBusy(err), "a timed-out clone is a clone failure, not lock contention: %v", err)
	require.False(t, errors.Is(err, context.DeadlineExceeded), "the budget kill must not wrap a context error: %v", err)
	require.Contains(t, err.Error(), "clone team-context timed out after 1ns")
	require.NoDirExists(t, teamPath, "a failed clone must not leave a team context in place")
}

// TestCloneInBackground_TeamContextBudgetTimeoutBacksOff verifies the timed-out
// clone is recorded as a failure that waits its backoff window.
func TestCloneInBackground_TeamContextBudgetTimeoutBacksOff(t *testing.T) {
	setCloneBudgets(t, time.Nanosecond, time.Minute)
	s, teamID, teamPath, clock, _ := newBackoffScheduler(t, unreachableTeamCloneURL)

	require.True(t, s.addClone())
	s.cloneInBackground(context.Background(), unreachableTeamCloneURL, teamPath, "team-context", teamID)

	attempts, next := s.workspaceRegistry.GetCloneRetryInfo(teamID)
	require.Equal(t, 1, attempts, "a clone that hit its budget is a failed attempt")
	require.Equal(t, clock.Now().Add(time.Minute), next, "first retry after 1m")
}

// TestTriggerMissingClones_TeamContextBackoffWindow verifies the self-healing
// trigger leaves a failed team context alone for its whole backoff window,
// says so once per failure, and tries again when the window ends.
// Failure prevented: the trigger started a fresh clone (and a git process) on
// every sync cycle for a team context whose clone kept timing out.
func TestTriggerMissingClones_TeamContextBackoffWindow(t *testing.T) {
	setCloneBudgets(t, time.Nanosecond, time.Minute)
	s, teamID, _, clock, logs := newBackoffScheduler(t, unreachableTeamCloneURL)
	clonesStarted := func() int { return strings.Count(logs.String(), "background clone starting") }
	deferrals := func() int { return strings.Count(logs.String(), "team context clone backoff") }

	// first cycle: nothing known yet, so the clone starts, times out, and is recorded
	triggerAndWait(s)
	require.Equal(t, 1, clonesStarted())

	// next cycles inside the 1m window start no clone
	triggerAndWait(s)
	clock.Advance(20 * time.Second)
	triggerAndWait(s)
	clock.Advance(20 * time.Second)
	triggerAndWait(s)
	require.Equal(t, 1, clonesStarted(), "no clone may start inside the backoff window")
	require.Equal(t, 1, deferrals(), "the deferral is logged once per failure, not on every cycle")
	require.Contains(t, logs.String(), `msg="team context clone backoff" team_id=team_backoff next_attempt_in=1m failures=1`)

	// the window ends: one more attempt, and it fails into a 2m window
	clock.Advance(21 * time.Second)
	triggerAndWait(s)
	require.Equal(t, 2, clonesStarted(), "the retry happens once the window has passed")
	attempts, next := s.workspaceRegistry.GetCloneRetryInfo(teamID)
	require.Equal(t, 2, attempts)
	require.Equal(t, clock.Now().Add(2*time.Minute), next, "second failure doubles the wait")

	triggerAndWait(s)
	require.Equal(t, 2, clonesStarted())
	require.Equal(t, 2, deferrals(), "a new failure earns a new deferral line")
	require.Contains(t, logs.String(), `msg="team context clone backoff" team_id=team_backoff next_attempt_in=2m failures=2`)
}

// TestTeamContextClone_SuccessResetsBackoff verifies a clone that works after
// failures clears the retry state, so the next failure starts again at 1m.
func TestTeamContextClone_SuccessResetsBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real team context clone")
	}
	teamURL := serveTeamContext(t)
	setCloneBudgets(t, time.Nanosecond, time.Minute)
	s, teamID, teamPath, clock, _ := newBackoffScheduler(t, teamURL)

	triggerAndWait(s)
	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo(teamID)
	require.Equal(t, 1, attempts)

	// the network is back: a normal budget, and the window has passed
	setCloneBudgets(t, time.Minute, time.Minute)
	clock.Advance(time.Minute + time.Second)
	triggerAndWait(s)

	require.DirExists(t, filepath.Join(teamPath, ".git"), "the retry must clone the team context")
	attempts, next := s.workspaceRegistry.GetCloneRetryInfo(teamID)
	require.Zero(t, attempts, "a successful clone clears the failure count")
	require.True(t, next.IsZero())
}

// TestCloneInBackground_TeamContextLockBusyStillDoesNotBackOff verifies true
// contention keeps its old behavior: another process cloning the same path is
// not this team context's failure.
func TestCloneInBackground_TeamContextLockBusyStillDoesNotBackOff(t *testing.T) {
	s, teamID, teamPath, _, _ := newBackoffScheduler(t, unreachableTeamCloneURL)

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gitutil.WithPreCloneLock(context.Background(), teamPath, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() {
		close(release)
		<-done
	}()

	require.True(t, s.addClone())
	s.cloneInBackground(context.Background(), unreachableTeamCloneURL, teamPath, "team-context", teamID)

	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo(teamID)
	require.Zero(t, attempts, "a clone held by a peer must not escalate backoff")
	require.NoDirExists(t, teamPath)
}
