package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// The server acknowledges receipt before the test cancels the caller. Its
// response stays blocked so completion can only come from cancellation.
func newStalledSyncServer(t *testing.T, requestPath string) (*httptest.Server, <-chan struct{}, <-chan struct{}, func()) {
	t.Helper()
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, canceledOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != requestPath {
			t.Errorf("unexpected sync request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		startedOnce.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
			canceledOnce.Do(func() { close(canceled) })
		case <-release:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(func() {
		unblock()
		server.Close()
	})
	return server, started, canceled, unblock
}

// A stalled credential response must be canceled before Start's startup join
// can exceed the daemon's five-second worker shutdown window.
func TestSyncScheduler_StartupCredentialRefreshHonorsCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real five-second scheduler startup delay")
	}
	server, requestStarted, requestCanceled, release := newStalledSyncServer(t, "/api/v1/cli/repos")
	isolated := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))
	t.Setenv("OX_XDG_DISABLE", "")
	scheduler, _ := newCredentialDiscoveryScheduler(t, server)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "cached-pat", ExpiresAt: time.Now().Add(15 * time.Minute),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))
	scheduler.config.SyncIntervalRead = time.Hour
	scheduler.config.TeamContextSyncInterval = time.Hour
	scheduler.config.VersionCheckInterval = 0
	scheduler.config.GCCheckInterval = 0
	scheduler.config.CodeDBCheckInterval = 0
	scheduler.config.LedgerCheckInterval = 0
	scheduler.config.GitHubSyncInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release()
		awaitStartupCompletion(t, done)
	})
	go func() {
		scheduler.Start(ctx)
		close(done)
	}()
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("startup credential request never reached the server")
	}
	cancel()
	shutdownDeadline := time.NewTimer(5 * time.Second)
	defer shutdownDeadline.Stop()
	select {
	case <-done:
	case <-shutdownDeadline.C:
		t.Fatal("Start exceeded the five-second shutdown window while refreshing credentials")
	}
	select {
	case <-requestCanceled:
	case <-shutdownDeadline.C:
		t.Fatal("credential HTTP request survived scheduler shutdown")
	}
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "cached-pat", creds.Token, "cancellation must preserve the previous PAT")
	scheduler.mu.Lock()
	inProgress := scheduler.credentialRefreshInProgress
	scheduler.mu.Unlock()
	require.False(t, inProgress, "cancellation must release the refresh dedup guard")
}

// Manual sync and auth-failure recovery share the same cancellation contract
// as startup, including the forced refresh of an otherwise fresh cached PAT.
func TestCredentialRefresh_SyncCallersHonorCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync func(*SyncScheduler, context.Context)
	}{
		{"manual sync", func(s *SyncScheduler, ctx context.Context) { _ = s.doSyncAll(ctx, nil) }},
		{"auth failure", func(s *SyncScheduler, ctx context.Context) {
			s.refreshAfterAuthFailure(ctx, errors.New("Authentication failed"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, started, canceled, release := newStalledSyncServer(t, "/api/v1/cli/repos")
			scheduler, _ := newCredentialDiscoveryScheduler(t, server)
			if tc.name == "auth failure" {
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
					Token: "fresh-cached-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
					BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
				}))
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				release()
				awaitStartupCompletion(t, done)
			})
			go func() {
				tc.sync(scheduler, ctx)
				close(done)
			}()
			awaitStartupCompletion(t, started)
			cancel()
			awaitStartupCompletion(t, done)
			awaitStartupCompletion(t, canceled)
			scheduler.mu.Lock()
			inProgress := scheduler.credentialRefreshInProgress
			scheduler.mu.Unlock()
			require.False(t, inProgress, "cancellation must release the refresh dedup guard")
		})
	}
}

// A canceled sync must interrupt the real Git liveness probe and must not
// start another PAT request after that probe returns.
func TestCredentialRefresh_AuthFailureProbeHonorsCancellation(t *testing.T) {
	server, started, _, release := newStalledSyncServer(t, "/team.git/info/refs")
	scheduler, _ := newCredentialDiscoveryScheduler(t, server)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "cached-pat", ServerURL: server.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
		Repos:           map[string]gitserver.RepoEntry{"team": {URL: server.URL + "/team.git"}},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release()
		awaitStartupCompletion(t, done)
	})
	go func() {
		scheduler.refreshAfterAuthFailure(ctx, errors.New("Authentication failed"))
		close(done)
	}()
	awaitStartupCompletion(t, started)
	cancel()
	// The probe's independent timeout is three seconds. Requiring completion
	// within one second proves cancellation, rather than that timeout, ended it.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	select {
	case <-done:
	case <-deadline.C:
		t.Fatal("PAT liveness probe ignored sync cancellation")
	}
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "cached-pat", creds.Token)
}

// Shutdown can cancel the auth-failure probe before refresh starts. That
// canceled follow-up must not consume the next live refresh's dedup window.
func TestCredentialRefresh_AlreadyCanceledPreservesDedupWindow(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "normal"
		if force {
			name = "forced"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			scheduler, _ := newCredentialDiscoveryScheduler(t, server)
			previousStamp := time.Now().Add(-6 * time.Minute)
			const previousHash = "previous-bearer"
			scheduler.lastCredentialRefresh = previousStamp
			scheduler.lastCredentialBearerHash = previousHash
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			scheduler.refreshCredentials(ctx, force)
			require.Equal(t, previousStamp, scheduler.lastCredentialRefresh)
			require.Equal(t, previousHash, scheduler.lastCredentialBearerHash)
			require.False(t, scheduler.credentialRefreshInProgress)
			require.Zero(t, calls.Load(), "a pre-canceled refresh must never send a PAT request")
		})
	}
}
