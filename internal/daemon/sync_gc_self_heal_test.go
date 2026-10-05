package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type divergedLedgerFixture struct {
	scheduler *SyncScheduler
	workspace WorkspaceState
	tracker   *IssueTracker
	log       *bytes.Buffer
}

func newDivergedLedgerFixture(t *testing.T, aged, partial bool, lastGC time.Time) divergedLedgerFixture {
	t.Helper()
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)
	tracker := NewIssueTracker()
	s.SetIssueTracker(tracker)
	logBuffer := &bytes.Buffer{}
	s.logger = slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)
	diverge(t, bareDir, ledgerDir, filepath.Join("sessions", "local.txt"), filepath.Join("sessions", "remote.txt"))
	if aged {
		backdateCommitTimestamp(t, ledgerDir, -4*time.Hour)
	}
	if partial {
		require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "remote.origin.promisor", "true").Run())
	} else {
		for _, key := range []string{"remote.origin.promisor", "extensions.partialClone"} {
			// Exit 5 means the optional key was absent, which is already the
			// desired full-clone fixture state.
			cmd := exec.Command("git", "-C", ledgerDir, "config", "--unset-all", key)
			if err := cmd.Run(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 5 {
					t.Fatalf("unset %s: %v", key, err)
				}
			}
		}
	}

	ws := WorkspaceState{
		ID:             "ledger",
		Type:           WorkspaceTypeLedger,
		Path:           ledgerDir,
		CloneURL:       cloneURL,
		Exists:         true,
		GCIntervalDays: 7,
		LastGCTime:     lastGC,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	primeConfigCacheLocked(registry)
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	return divergedLedgerFixture{scheduler: s, workspace: ws, tracker: tracker, log: logBuffer}
}

func assertDivergedLedgerRecovered(t *testing.T, fixture divergedLedgerFixture) {
	t.Helper()
	assert.FileExists(t, filepath.Join(fixture.workspace.Path, "sessions", "remote.txt"))
	assert.FileExists(t, filepath.Join(fixture.workspace.Path, "sessions", "local.txt"))
	status, err := gitutil.RunGit(context.Background(), fixture.workspace.Path, "status", "--porcelain")
	require.NoError(t, err)
	assert.Contains(t, status, "sessions/local.txt", "rescued local commit content must remain visible for review")
}

func TestCheckAndRunGC_AgedDivergenceOutranksOverdueInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and divergence recovery")
	}
	fixture := newDivergedLedgerFixture(t, true, true, time.Now().Add(-8*24*time.Hour))

	fixture.scheduler.checkAndRunGC(context.Background())

	assertDivergedLedgerRecovered(t, fixture)
	assert.Contains(t, fixture.log.String(), "reason=\"sync wedge detected\"")
}

func TestCheckAndRunGC_AgedDivergenceOutranksFullCloneUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and divergence recovery")
	}
	fixture := newDivergedLedgerFixture(t, true, false, time.Now())

	fixture.scheduler.checkAndRunGC(context.Background())

	assertDivergedLedgerRecovered(t, fixture)
	assert.Contains(t, fixture.log.String(), "reason=\"sync wedge detected\"")
}

func TestCheckAndRunGC_FreshDivergenceRemainsConservative(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and divergence recovery")
	}
	fixture := newDivergedLedgerFixture(t, false, true, time.Now().Add(-8*24*time.Hour))

	fixture.scheduler.checkAndRunGC(context.Background())

	assert.NoFileExists(t, filepath.Join(fixture.workspace.Path, "sessions", "remote.txt"), "fresh divergence must not be converted into working-tree recovery")
	assert.FileExists(t, filepath.Join(fixture.workspace.Path, "sessions", "local.txt"), "skipped GC must leave local commits untouched")
	_, found := fixture.tracker.GetIssue(IssueTypeDirtyWorkspace, "ledger")
	assert.True(t, found)
	assert.Contains(t, fixture.log.String(), "reason=\"interval exceeded\"")
}

func TestCheckAndRunGC_LockBusyDoesNotSpendWedgeCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and repo lock contention")
	}
	fixture := newDivergedLedgerFixture(t, true, true, time.Now().Add(-8*24*time.Hour))

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = gitutil.WithRepoLock(context.Background(), fixture.workspace.Path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	fixture.scheduler.checkAndRunGC(ctx)

	fixture.scheduler.mu.Lock()
	lastWedgeCheck := fixture.scheduler.lastWedgeCheck
	fixture.scheduler.mu.Unlock()
	assert.True(t, lastWedgeCheck.IsZero(), "lock contention did not run a probe and must not consume the six-hour cooldown")
}

func TestTriggerGC_ForcedLedgerRecoveryCapturesDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and forced divergence recovery")
	}
	fixture := newDivergedLedgerFixture(t, false, true, time.Now())
	fixture.scheduler.WorkspaceRegistry().RecordSyncFailure("ledger")
	fixture.scheduler.WorkspaceRegistry().RecordSyncFailure("ledger")
	fixture.scheduler.WorkspaceRegistry().RecordSyncFailure("ledger")
	require.False(t, fixture.scheduler.WorkspaceRegistry().ShouldSync("ledger"))

	response := fixture.scheduler.TriggerGC(context.Background())

	require.True(t, response.LedgerTriggered)
	require.Empty(t, response.Errors)
	assertDivergedLedgerRecovered(t, fixture)
	assert.True(t, fixture.scheduler.WorkspaceRegistry().ShouldSync("ledger"), "successful recovery must clear stale sync backoff")
	issue, found := fixture.tracker.GetIssue(IssueTypeSessionConflictRecovered, "ledger")
	require.True(t, found, "forced recovery must surface recovered content for review")
	assert.True(t, issue.RequiresConfirm)
}
