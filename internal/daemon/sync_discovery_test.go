package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
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

func TestRefreshCredentials_DedupWithinWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	s := newDiscoveryTestScheduler(t)

	// first call sets the timestamp
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	firstStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, firstStamp.IsZero(), "first call should set lastCredentialRefresh")

	// second call within 5min window should be a no-op (dedup)
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	secondStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.Equal(t, firstStamp, secondStamp,
		"second call within dedup window should not update timestamp")
}

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
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	newStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.True(t, newStamp.After(oldStamp),
		"call after dedup window should update timestamp")
}

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
			s.refreshCredentialsIfNeeded()
		}()
	}
	wg.Wait()

	// verify no race: timestamp should be set exactly once
	s.mu.Lock()
	stamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, stamp.IsZero(), "timestamp should be set after concurrent calls")
}

// newCredentialDiscoveryScheduler keeps HTTP requests and credential writes in
// the test's endpoint and temporary store.
func newCredentialDiscoveryScheduler(t *testing.T, server *httptest.Server) *SyncScheduler {
	t.Helper()
	isolateCredentialsWithDir(t)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	return newDiscoveryTestScheduler(t)
}

// Failure prevented: a rotated team token keeps using the prior token's PAT
// for five minutes merely because its credential refresh timer is still fresh.
func TestCredentialRotation_BypassesRefreshDedup(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
		assert.Equal(t, "Bearer oxt_rotated_1lKvCA", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(api.ReposResponse{
			Token: "rotated-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	}))
	t.Cleanup(server.Close)
	s := newCredentialDiscoveryScheduler(t, server)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))

	s.refreshCredentialsIfNeeded()
	require.Zero(t, calls.Load(), "matching fresh credentials should not call the API")
	require.False(t, s.lastCredentialRefresh.IsZero())
	t.Setenv("SAGEOX_TOKEN", "oxt_rotated_1lKvCA")
	s.refreshCredentialsIfNeeded()

	require.EqualValues(t, 1, calls.Load(), "rotation must bypass the five-minute timer")
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "rotated-pat", creds.Token)
	require.Equal(t, gitserver.BearerTokenFingerprint("oxt_rotated_1lKvCA"), creds.BearerTokenHash)
	s.refreshCredentialsIfNeeded()
	require.EqualValues(t, 1, calls.Load(), "the rotated credential should retain normal deduplication")
}

// Failure prevented: credential refresh or team discovery silently hides a
// revoked team token, suggests a personal login, or retains its warning after recovery.
func TestCredentialRevocation_ReportsTeamRemedyAndClearsOnRecovery(t *testing.T) {
	for _, path := range []string{"credential refresh", "team discovery"} {
		t.Run(path, func(t *testing.T) {
			var rejected atomic.Bool
			rejected.Store(true)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
				assert.Equal(t, "Bearer oxt_test_1ljPfr", r.Header.Get("Authorization"))
				if rejected.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(api.ReposResponse{
					Token: "recovered-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
				})
			}))
			t.Cleanup(server.Close)
			s := newCredentialDiscoveryScheduler(t, server)
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(30 * time.Minute),
				BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
			}))
			if path == "credential refresh" {
				s.refreshCredentialsIfNeeded()
			} else {
				s.discoverTeams()
			}
			require.EqualValues(t, 1, calls.Load())
			issue, found := s.issues.GetIssue(IssueTypeAuthExpiring, "")
			require.True(t, found, "a server rejection must surface as an authentication issue")
			require.Equal(t, SeverityError, issue.Severity)
			require.Contains(t, issue.Summary, "SAGEOX_TOKEN")
			require.Contains(t, issue.Summary, "CI secret store")
			require.NotContains(t, issue.Summary, "ox login")
			creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
			require.NoError(t, err)
			require.NotNil(t, creds)
			require.Equal(t, "old-pat", creds.Token, "revocation must preserve the offline cache")

			rejected.Store(false)
			if path == "credential refresh" {
				s.refreshCredentials(true)
			} else {
				s.lastTeamDiscovery = time.Time{}
				s.discoverTeams()
			}
			require.EqualValues(t, 2, calls.Load())
			_, found = s.issues.GetIssue(IssueTypeAuthExpiring, "")
			require.False(t, found, "successful recovery must clear the authentication issue")
			creds, err = gitserver.LoadCredentialsForEndpoint(server.URL)
			require.NoError(t, err)
			require.NotNil(t, creds)
			require.Equal(t, "recovered-pat", creds.Token)
		})
	}
}

// Failure prevented: the actual ledger fetch path never asks for a replacement
// after Git rejects a PAT whose expiration and refresh timer still look healthy.
func TestLedgerPull_AuthFailureRefreshesFreshPAT(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git fetch")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	var calls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
		_ = json.NewEncoder(w).Encode(api.ReposResponse{
			Token: "replacement-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	}))
	t.Cleanup(apiServer.Close)
	s := newCredentialDiscoveryScheduler(t, apiServer)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, gitserver.GitCredentials{
		Token: "rejected-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))
	s.refreshCredentialsIfNeeded()
	require.Zero(t, calls.Load())

	gitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(gitServer.Close)
	// Suppress the installed CLI helper so this test exercises the daemon's
	// recovery rather than a separate installed ox binary on the developer's PATH.
	previousHelper := gitserver.DefaultHelperCommand()
	gitserver.SetHelperCommand("!true")
	t.Cleanup(func() { gitserver.SetHelperCommand(previousHelper) })
	s.config.LedgerPath = t.TempDir()
	gitInDir(t, s.config.LedgerPath, "init", "-b", "main")
	gitInDir(t, s.config.LedgerPath, "remote", "add", "origin", gitServer.URL+"/ledger.git")
	err := s.doPull(context.Background(), nil, true, false)
	require.Error(t, err)
	require.True(t, gitserver.IsAuthFailure(err.Error()), "the real Git fetch must fail on authentication")
	require.EqualValues(t, 1, calls.Load(), "ledger fetch authentication failures must force credential refresh")
	creds, err := gitserver.LoadCredentialsForEndpoint(apiServer.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "replacement-pat", creds.Token)
}
