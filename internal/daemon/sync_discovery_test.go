package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDiscoveryTestScheduler creates a SyncScheduler for credential refresh testing.
func newDiscoveryTestScheduler(t *testing.T) *SyncScheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewSyncScheduler(cfg, logger)
	s.issues = NewIssueTracker()
	return s
}

// Repeated lazy refresh with the same bearer inside the dedup window must not
// advance the refresh timestamp and continuously postpone the next allowed attempt.
func TestRefreshCredentials_DedupWithinWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	s := newDiscoveryTestScheduler(t)

	// first call sets the timestamp
	s.refreshCredentialsIfNeeded(context.Background())

	s.mu.Lock()
	firstStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, firstStamp.IsZero(), "first call should set lastCredentialRefresh")

	// second call within 5min window should be a no-op (dedup)
	s.refreshCredentialsIfNeeded(context.Background())

	s.mu.Lock()
	secondStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.Equal(t, firstStamp, secondStamp,
		"second call within dedup window should not update timestamp")
}

// An elapsed dedup window must permit a new refresh; a prior attempt cannot
// suppress credential maintenance indefinitely.
func TestRefreshCredentials_AllowsAfterWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	s := newDiscoveryTestScheduler(t)

	// set timestamp to 6 minutes ago (beyond 5min dedup window)
	s.mu.Lock()
	s.lastCredentialRefresh = time.Now().Add(-6 * time.Minute)
	oldStamp := s.lastCredentialRefresh
	s.mu.Unlock()

	// call should proceed past the dedup check and update the timestamp
	s.refreshCredentialsIfNeeded(context.Background())

	s.mu.Lock()
	newStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.True(t, newStamp.After(oldStamp),
		"call after dedup window should update timestamp")
}

// Concurrent lazy refresh calls must safely share refresh state and leave a
// valid attempt timestamp; the race detector exercises their synchronization.
func TestRefreshCredentials_ConcurrentCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	s := newDiscoveryTestScheduler(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.refreshCredentialsIfNeeded(context.Background())
		}()
	}
	wg.Wait()

	// verify no race: timestamp should be set exactly once
	s.mu.Lock()
	stamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, stamp.IsZero(), "timestamp should be set after concurrent calls")
}

func newCredentialDiscoveryScheduler(t *testing.T, server *httptest.Server) (*SyncScheduler, string) {
	t.Helper()
	credDir := isolateCredentialsWithDir(t)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	return newDiscoveryTestScheduler(t), credDir
}

// Bearer rotation must bypass dedup without destroying a usable cached PAT on
// revocation. Recovery replaces credentials and clears the auth issue; unchanged
// or failed persistence must not incorrectly change cache identity.
func TestCredentialRotation_RevocationAndRecovery(t *testing.T) {
	for _, mode := range []string{"refresh", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			var rejected, blockSave atomic.Bool
			var calls atomic.Int32
			expires := time.Now().Add(24 * time.Hour)
			blockedStore := filepath.Join(t.TempDir(), "not-a-directory")
			require.NoError(t, os.WriteFile(blockedStore, nil, 0600))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "Bearer oxt_rotated_1lKvCA", r.Header.Get("Authorization"))
				if rejected.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				response := api.ReposResponse{Token: "fresh-pat", ExpiresAt: expires}
				if blockSave.Load() {
					gitserver.TestSetConfigDirOverride(blockedStore)
					response.Token = "replacement-pat"
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			t.Cleanup(server.Close)
			s, credDir := newCredentialDiscoveryScheduler(t, server)
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
				BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
			}))
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfX") // invalid checksum
			s.refreshCredentialsIfNeeded(context.Background())
			require.Zero(t, calls.Load(), "a malformed bearer must never reach the API")
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
			s.refreshCredentialsIfNeeded(context.Background())
			require.Zero(t, calls.Load())
			t.Setenv("SAGEOX_TOKEN", "oxt_rotated_1lKvCA")
			for i, revoked := range []bool{true, false} {
				rejected.Store(revoked)
				if mode == "refresh" {
					s.refreshCredentials(context.Background(), !revoked)
				} else {
					s.lastTeamDiscovery = time.Time{}
					s.discoverTeams(context.Background())
				}
				require.EqualValues(t, i+1, calls.Load(), "rotation and recovery must bypass refresh dedup")
				issue, found := s.issues.GetIssue(IssueTypeAuthExpiring, "")
				require.Equal(t, revoked, found)
				creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
				require.NoError(t, err)
				require.NotNil(t, creds)
				if revoked {
					require.Contains(t, issue.Summary, "SAGEOX_TOKEN")
					require.NotContains(t, issue.Summary, "ox login")
					require.Equal(t, "old-pat", creds.Token)
				} else {
					require.Equal(t, "fresh-pat", creds.Token)
					require.Equal(t, gitserver.BearerTokenFingerprint("oxt_rotated_1lKvCA"), creds.BearerTokenHash)
				}
			}
			s.refreshCredentialsIfNeeded(context.Background())
			require.EqualValues(t, 2, calls.Load(), "a matching fresh PAT must not refresh again")
			if mode == "discovery" {
				cachePath := filepath.Join(credDir, "sageox", "git-credentials-"+endpoint.NormalizeSlug(server.URL)+".json")
				before, err := os.Stat(cachePath)
				require.NoError(t, err)
				for _, failSave := range []bool{false, true} {
					blockSave.Store(failSave)
					s.lastTeamDiscovery = time.Time{}
					s.discoverTeams(context.Background())
					gitserver.TestSetConfigDirOverride(credDir)
					after, err := os.Stat(cachePath)
					require.NoError(t, err)
					require.True(t, os.SameFile(before, after), "unchanged discovery or a failed save must preserve the cache")
				}
				require.EqualValues(t, 4, calls.Load())
				cached, err := gitserver.LoadCredentialsForEndpoint(server.URL)
				require.NoError(t, err)
				require.Equal(t, "fresh-pat", cached.Token)
			}
		})
	}
}

// Personal bearer refresh must finish before discovery; canceling discovery must preserve its PAT.
func TestTeamDiscovery_RefreshesPersonalBearerAndHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case auth.TokenEndpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque", "expires_in": 3600})
		case "/api/v1/cli/auth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-jwt", "expires_in": 900})
		case "/api/v1/cli/repos":
			assert.Equal(t, "Bearer fresh-jwt", r.Header.Get("Authorization"))
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				t.Error("discovery ignored caller cancellation")
			}
			_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "fresh-pat", ExpiresAt: time.Now().Add(24 * time.Hour)})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	s, _ := newCredentialDiscoveryScheduler(t, server)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
		AccessToken: "expired-jwt", RefreshToken: "valid-refresh", ExpiresAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "cached-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("expired-jwt"),
	}))
	s.discoverTeams(ctx)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "cached-pat", creds.Token)
}

// A real Git authentication failure must force API credential refresh even when
// the cached PAT and bearer fingerprint look fresh enough for lazy refresh to skip.
func TestLedgerPull_AuthFailureRefreshesFreshPAT(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git fetch")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli/repos" {
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "fresh-pat", ExpiresAt: time.Now().Add(24 * time.Hour)})
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	s, _ := newCredentialDiscoveryScheduler(t, server)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))
	s.refreshCredentialsIfNeeded(context.Background())
	previousHelper := gitserver.DefaultHelperCommand()
	gitserver.SetHelperCommand("!true")
	t.Cleanup(func() { gitserver.SetHelperCommand(previousHelper) })
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s.config.LedgerPath = t.TempDir()
	gitInDir(t, s.config.LedgerPath, "init", "-b", "main")
	gitInDir(t, s.config.LedgerPath, "remote", "add", "origin", server.URL+"/ledger.git")
	err := s.doPull(context.Background(), nil, true, false)
	require.Error(t, err)
	require.True(t, gitserver.IsAuthFailure(err.Error()))
	require.EqualValues(t, 1, calls.Load(), "Git 401 must force refresh despite a fresh PAT and dedup timer")
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "fresh-pat", creds.Token)
}
