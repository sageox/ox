package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// A missing ledger can be requested by IPC, a watcher, or cloud discovery.
// These tests exercise actual clones and persisted status so losing the request
// that asked for a clone cannot silently poison a daemon-owned background task.
func setupCloneLifecycleLedgerHTTP(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not available")
	}
	bare, work := initBareRepo(t, "clone-lifecycle")
	require.NoError(t, os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("# Fixture Ledger\n"), 0600))
	gitInDir(t, work, "add", "AGENTS.md")
	gitInDir(t, work, "commit", "-m", "seed initialized Ledger")
	gitInDir(t, work, "push", "origin", "HEAD:main")
	gitInDir(t, bare, "update-server-info")
	server := httptest.NewServer(http.FileServer(http.Dir(bare)))
	t.Cleanup(server.Close)
	return server.URL
}

// newCloneLifecycleScheduler isolates credentials and workspace registration,
// and joins background clones before temporary repositories are cleaned up.
func newCloneLifecycleScheduler(t *testing.T, cloneURL string) (*SyncScheduler, string) {
	t.Helper()
	isolateCredentialsWithDir(t)
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	project := setupProjectWithConfig(t, fmt.Sprintf("[ledger]\npath = %q\n", ledgerPath))
	s := newTestScheduler(project)
	require.NoError(t, s.workspaceRegistry.LoadFromConfig())
	require.True(t, s.workspaceRegistry.SetLedgerCloneURL(cloneURL))
	t.Cleanup(func() { s.cloneWg.Wait() })
	return s, ledgerPath
}

// requireCloneRecorded compares actual Git HEAD with persisted sync and registry
// state: a directory alone does not prove a background clone finished correctly.
func requireCloneRecorded(t *testing.T, s *SyncScheduler, id, repoPath string) {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "HEAD").CombinedOutput()
	require.NoError(t, err, string(out))
	state := LoadSyncState(repoPath)
	require.False(t, state.LastSync.IsZero(), "a completed clone must report a successful sync")
	require.Equal(t, strings.TrimSpace(string(out)), state.LastSyncCommit, "the clone's HEAD must survive request cancellation")
	require.Zero(t, state.ConsecutiveFailures)
	ws := s.workspaceRegistry.GetWorkspace(id)
	require.NotNil(t, ws)
	require.True(t, ws.Exists)
	require.False(t, ws.ConfigLastSync.IsZero(), "status must show synced before the next scheduler tick")
}

// Hold every clone slot until the initiating request ends. Daemon-owned cloning
// must still finish and persist the actual Git HEAD after that request is canceled.
func TestCloneLifecycle_RequestCancellationDoesNotCancelDaemonClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone")
	}
	s, ledgerPath := newCloneLifecycleScheduler(t, setupCloneLifecycleLedgerHTTP(t))
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	t.Cleanup(daemonCancel)
	s.ctx = daemonCtx
	requestCtx, requestCancel := context.WithCancel(context.Background())
	t.Cleanup(requestCancel)

	for range maxConcurrentClones {
		s.cloneSem <- struct{}{}
	}
	var releaseOnce sync.Once
	releaseSlots := func() {
		releaseOnce.Do(func() {
			for range maxConcurrentClones {
				<-s.cloneSem
			}
		})
	}
	t.Cleanup(releaseSlots)
	reached := make(chan struct{})
	s.onBeforeCloneSem = func() { close(reached) }
	s.triggerMissingClones(requestCtx)
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("background clone did not reach the semaphore")
	}
	requestCancel()
	releaseSlots()
	mustCompleteWithin(t, 10*time.Second, "daemon clone did not complete", s.cloneWg.Wait)
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	require.NoError(t, daemonCtx.Err())
	requireCloneRecorded(t, s, "ledger", ledgerPath)
}

// Anti-entropy and watcher paths must repair missing Ledger and Team Context
// workspaces through real Git clones, including maintenance before Start sets a
// scheduler context, then record those clones as synced.
func TestCloneLifecycle_AntiEntropyAndWatcherRepairMissingWorkspaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CGI Git HTTP fixture requires Unix process semantics")
	}
	if testing.Short() {
		t.Skip("short: real Git clones")
	}
	for _, trigger := range []string{"anti-entropy", "watcher"} {
		t.Run(trigger, func(t *testing.T) {
			s, ledgerPath := newCloneLifecycleScheduler(t, setupCloneLifecycleLedgerHTTP(t))
			teamBare := setupTeamContextBareRepo(t, "version = 1\n", map[string]string{"AGENTS.md": "# Fixture Team Context\n"})
			gitExec, err := exec.Command("git", "--exec-path").Output()
			require.NoError(t, err)
			// Two-phase Team Context clones require real shallow/filter
			// negotiation, so serve Git's actual smart HTTP backend.
			backend := &cgi.Handler{
				Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
				Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(teamBare), "GIT_HTTP_EXPORT_ALL=1"},
			}
			teamServer := httptest.NewServer(backend)
			t.Cleanup(teamServer.Close)
			teamURL := teamServer.URL + "/" + filepath.Base(teamBare)
			teamPath := filepath.Join(t.TempDir(), "team-context")
			local, err := config.LoadLocalConfig(s.config.ProjectRoot)
			require.NoError(t, err)
			local.TeamContexts = []config.TeamContext{{TeamID: "team_clone_lifecycle", TeamName: "Clone lifecycle", Path: teamPath}}
			require.NoError(t, config.SaveLocalConfig(s.config.ProjectRoot, local))
			require.NoError(t, gitserver.SaveCredentialsForEndpoint("https://fake.test.invalid", gitserver.GitCredentials{
				Token: "test-pat", ExpiresAt: time.Now().Add(time.Hour),
				Repos: map[string]gitserver.RepoEntry{"team_clone_lifecycle": {Type: "team-context", TeamID: "team_clone_lifecycle", Name: "Clone lifecycle", URL: teamURL}},
			}))
			s.workspaceRegistry.InvalidateConfigCache()
			require.NoError(t, s.workspaceRegistry.LoadFromConfig())
			require.Len(t, s.workspaceRegistry.GetTeamContexts(), 1)
			require.Equal(t, teamURL, s.workspaceRegistry.GetTeamContexts()[0].CloneURL)

			// Unstarted schedulers have no daemon context yet; these public
			// maintenance entry points still need to finish self-healing.
			require.Nil(t, s.ctx)
			if trigger == "anti-entropy" {
				s.TriggerAntiEntropy()
			} else {
				s.syncFromWatcher(context.Background())
			}
			mustCompleteWithin(t, 10*time.Second, "workspace repair did not finish", s.cloneWg.Wait)
			requireCloneRecorded(t, s, "ledger", ledgerPath)
			requireCloneRecorded(t, s, "team_clone_lifecycle", teamPath)
			require.FileExists(t, filepath.Join(teamPath, "SOUL.md"))
		})
	}
}

// A workspace cloned by another caller must be recognized as healthy, with its
// HEAD backfilled and stale retry/doctor failures cleared instead of recloning.
func TestCloneLifecycle_ExistingPeerCloneBackfillsSyncState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone")
	}
	s, ledgerPath := newCloneLifecycleScheduler(t, setupCloneLifecycleLedgerHTTP(t))
	// Another actor finishes the clone after this daemon loaded the registry.
	result, err := s.Checkout(CheckoutPayload{CloneURL: s.workspaceRegistry.GetLedger().CloneURL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
	require.NoError(t, err)
	require.True(t, result.Cloned)
	require.False(t, s.workspaceRegistry.GetLedger().Exists)
	require.True(t, s.workspaceRegistry.GetLedger().ConfigLastSync.IsZero())
	s.workspaceRegistry.SetCloneRetry("ledger", 3, time.Now().Add(-time.Minute))
	require.NoError(t, SaveSyncState(ledgerPath, &SyncState{ConsecutiveFailures: 2}))
	s.issues = NewIssueTracker()
	s.issues.SetIssue(DaemonIssue{Type: IssueTypeCloneFailed, Repo: "ledger", Summary: "previous clone failed"})
	require.True(t, s.addClone())
	s.cloneInBackground(context.Background(), s.workspaceRegistry.GetLedger().CloneURL, ledgerPath, "ledger", "ledger")
	requireCloneRecorded(t, s, "ledger", ledgerPath)
	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo("ledger")
	require.Zero(t, attempts, "a peer's completed clone must clear stale retry state")
	_, found := s.issues.GetIssue(IssueTypeCloneFailed, "ledger")
	require.False(t, found, "a completed clone must clear the failure shown by doctor")
}

// Once the daemon is canceled, a live request cannot resurrect discovery or
// cloning. Shutdown must leave missing-workspace state intact without retry errors.
func TestCloneLifecycle_CanceledMaintenancePreservesMissingWorkspace(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	isolateCredentialsWithDir(t)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	project := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_canceled_clone", Endpoint: server.URL}))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
	s := newTestScheduler(project)
	t.Cleanup(func() { s.cloneWg.Wait() })
	require.NoError(t, s.workspaceRegistry.LoadFromConfig())
	require.Empty(t, s.workspaceRegistry.GetLedger().CloneURL, "discovery would be necessary without cancellation")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	s.TriggerAntiEntropy()
	s.triggerMissingClones(ctx)
	s.fetchLedgerURLFromAPI(ctx)
	require.True(t, s.addClone())
	s.cloneInBackground(ctx, server.URL, ledgerPath, "ledger", "ledger")
	// An IPC request can still be live while its owning daemon is stopping.
	// Its background clone must inherit the canceled daemon lifecycle.
	require.True(t, s.workspaceRegistry.SetLedgerCloneURL(server.URL))
	s.triggerMissingClones(context.Background())
	mustCompleteWithin(t, time.Second, "a canceled clone leaked its scheduler join", s.cloneWg.Wait)
	require.Zero(t, requests.Load(), "canceled maintenance must not contact Git or the API")
	require.NoDirExists(t, ledgerPath)
	require.True(t, s.workspaceRegistry.GetLedger().ConfigLastSync.IsZero())
	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo("ledger")
	require.Zero(t, attempts, "shutdown is not a clone failure and must not create retry backoff")
	_, inFlight := s.cloneInFlight.Load("ledger")
	require.False(t, inFlight)
}

// A clone discovered through the API belongs to the daemon after the request
// ends. It must finish, persist sync state and save its path for the next startup.
func TestCloneLifecycle_APIDiscoveryContinuesAfterRequestCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone and cloud discovery")
	}
	cloneURL := setupCloneLifecycleLedgerHTTP(t)
	var calls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.IntrospectEndpoint {
			_ = json.NewEncoder(w).Encode(auth.IntrospectResult{Active: true, PrincipalKind: auth.PrincipalKindTeamService})
			return
		}
		calls.Add(1)
		if r.URL.Path != "/api/v1/cli/repos/repo_clone_lifecycle" {
			t.Errorf("unexpected discovery request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(api.RepoDetailResponse{Ledger: &api.RepoDetailLedger{Status: "ready", RepoURL: cloneURL}})
	}))
	t.Cleanup(apiServer.Close)
	isolateCredentialsWithDir(t)
	t.Setenv("SAGEOX_ENDPOINT", apiServer.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	project := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_clone_lifecycle", Endpoint: apiServer.URL}))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
	s := newTestScheduler(project)
	t.Cleanup(func() { s.cloneWg.Wait() })
	require.NoError(t, s.workspaceRegistry.LoadFromConfig())
	s.config.LedgerPath = ledgerPath
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	t.Cleanup(daemonCancel)
	s.ctx = daemonCtx
	requestCtx, requestCancel := context.WithCancel(context.Background())
	t.Cleanup(requestCancel)
	for range maxConcurrentClones {
		s.cloneSem <- struct{}{}
	}
	var releaseOnce sync.Once
	releaseSlots := func() {
		releaseOnce.Do(func() {
			for range maxConcurrentClones {
				<-s.cloneSem
			}
		})
	}
	t.Cleanup(releaseSlots)
	reached := make(chan struct{})
	var once sync.Once
	s.onBeforeCloneSem = func() { once.Do(func() { close(reached) }) }
	// doPull asks the API for its missing URL. Discovery persists the path
	// and schedules cloning before this foreground request returns.
	require.NoError(t, s.doPull(requestCtx, nil, false, false))
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("API-discovered clone did not start")
	}
	requestCancel()
	releaseSlots()
	mustCompleteWithin(t, 10*time.Second, "API-discovered clone did not finish", s.cloneWg.Wait)
	require.EqualValues(t, 1, calls.Load())
	requireCloneRecorded(t, s, "ledger", ledgerPath)
	local, err := config.LoadLocalConfig(s.config.ProjectRoot)
	require.NoError(t, err)
	require.NotNil(t, local.Ledger)
	require.Equal(t, ledgerPath, local.Ledger.Path, "discovered clone must survive a daemon restart")
}
