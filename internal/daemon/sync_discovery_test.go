package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
func newCredentialDiscoveryScheduler(t *testing.T, server *httptest.Server) (*SyncScheduler, string) {
	t.Helper()
	credDir := isolateCredentialsWithDir(t)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	return newDiscoveryTestScheduler(t), credDir
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
	s, _ := newCredentialDiscoveryScheduler(t, server)
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
			var blockSave atomic.Bool
			var credentialsPath string
			expires := time.Now().Add(24 * time.Hour)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
				assert.Equal(t, "Bearer oxt_test_1ljPfr", r.Header.Get("Authorization"))
				if rejected.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if blockSave.Load() {
					// A directory at the destination deterministically fails an
					// atomic cache replacement, including on root-run CI hosts.
					assert.NoError(t, os.Remove(credentialsPath))
					assert.NoError(t, os.Mkdir(credentialsPath, 0700))
				}
				_ = json.NewEncoder(w).Encode(api.ReposResponse{
					Token: "recovered-pat", ExpiresAt: expires,
				})
			}))
			t.Cleanup(server.Close)
			s, credDir := newCredentialDiscoveryScheduler(t, server)
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(30 * time.Minute),
				BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
			}))
			files, err := filepath.Glob(filepath.Join(credDir, "sageox", "git-credentials-*.json"))
			require.NoError(t, err)
			require.Len(t, files, 1)
			credentialsPath = files[0]
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfX") // invalid checksum
			s.refreshCredentialsIfNeeded()
			s.discoverTeams(context.Background())
			require.Zero(t, calls.Load(), "malformed bearers must be refused before any API call")
			require.Contains(t, logs.String(), "failed to get auth token for credential refresh")
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
			s.lastTeamDiscovery = time.Time{}
			if path == "credential refresh" {
				s.refreshCredentialsIfNeeded()
			} else {
				s.discoverTeams(context.Background())
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
				s.discoverTeams(context.Background())
			}
			require.EqualValues(t, 2, calls.Load())
			_, found = s.issues.GetIssue(IssueTypeAuthExpiring, "")
			require.False(t, found, "successful recovery must clear the authentication issue")
			creds, err = gitserver.LoadCredentialsForEndpoint(server.URL)
			require.NoError(t, err)
			require.NotNil(t, creds)
			require.Equal(t, "recovered-pat", creds.Token)

			if path == "credential refresh" {
				// A corrupted cache is preserved for diagnosis instead of
				// silently fetching and replacing it with unrelated credentials.
				require.NoError(t, os.WriteFile(credentialsPath, []byte("{broken"), 0600))
				s.refreshCredentials(true)
				require.EqualValues(t, 2, calls.Load())
				require.Contains(t, logs.String(), "failed to load credentials for refresh check")
				content, err := os.ReadFile(credentialsPath)
				require.NoError(t, err)
				require.Equal(t, "{broken", string(content))
			} else {
				before, err := os.Stat(credentialsPath)
				require.NoError(t, err)
				s.lastTeamDiscovery = time.Time{}
				s.discoverTeams(context.Background())
				require.EqualValues(t, 3, calls.Load())
				after, err := os.Stat(credentialsPath)
				require.NoError(t, err)
				require.True(t, os.SameFile(before, after), "unchanged discovery must not rewrite the cache")

				creds.Token = "prior-pat"
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, *creds))
				blockSave.Store(true)
				s.lastTeamDiscovery = time.Time{}
				s.discoverTeams(context.Background())
				require.EqualValues(t, 4, calls.Load())
				require.Contains(t, logs.String(), "failed to save credentials after team discovery")
			}
		})
	}
}

// Failure prevented: team discovery treats an expired personal bearer as revoked
// when its refresh token and cached Git PAT are still healthy.
func TestTeamDiscovery_RefreshesExpiredPersonalBearer(t *testing.T) {
	var refreshCalls, exchangeCalls, repoCalls atomic.Int32
	var blockDiscovery atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	patExpires := time.Now().Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.TokenEndpoint:
			refreshCalls.Add(1)
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			assert.Equal(t, "valid-refresh", r.Form.Get("refresh_token"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "refreshed-opaque", "expires_in": 3600,
			})
		case "/api/v1/cli/auth/token":
			exchangeCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "refreshed-jwt", "expires_in": 900,
			})
		case "/api/v1/cli/repos":
			repoCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer refreshed-jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if blockDiscovery.Load() {
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}
			_ = json.NewEncoder(w).Encode(api.ReposResponse{
				Token: "fresh-pat", ExpiresAt: patExpires,
			})
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	s, _ := newCredentialDiscoveryScheduler(t, server)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
		AccessToken: "expired-jwt", RefreshToken: "valid-refresh",
		ExpiresAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "fresh-pat", ExpiresAt: patExpires,
		BearerTokenHash: gitserver.BearerTokenFingerprint("expired-jwt"),
	}))

	s.refreshCredentialsIfNeeded()
	require.Zero(t, refreshCalls.Load(), "a matching fresh PAT skips the passive refresh")
	require.Zero(t, repoCalls.Load())
	s.discoverTeams(context.Background())

	require.EqualValues(t, 1, refreshCalls.Load())
	require.EqualValues(t, 1, exchangeCalls.Load())
	require.EqualValues(t, 1, repoCalls.Load())
	_, found := s.issues.GetIssue(IssueTypeAuthExpiring, "")
	require.False(t, found, "successful token refresh must not require another login")
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "fresh-pat", creds.Token)
	require.Equal(t, gitserver.BearerTokenFingerprint("refreshed-jwt"), creds.BearerTokenHash)

	t.Run("canceled discovery preserves cached PAT", func(t *testing.T) {
		creds.Token = "cached-before-cancel"
		require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, *creds))
		s.lastTeamDiscovery = time.Time{}
		blockDiscovery.Store(true)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.discoverTeams(ctx)
		}()
		t.Cleanup(func() {
			cancel()
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("team discovery did not stop during cleanup")
			}
		})
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("team discovery never reached the server")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("team discovery ignored its caller's cancellation")
		}
		cached, err := gitserver.LoadCredentialsForEndpoint(server.URL)
		require.NoError(t, err)
		require.Equal(t, "cached-before-cancel", cached.Token)
	})
}

// Failure prevented: the actual ledger fetch path never asks for a replacement
// after Git rejects a PAT whose expiration and refresh timer still look healthy.
func TestLedgerPull_AuthFailureRefreshesFreshPAT(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git fetch")
	}
	if runtime.GOOS == "windows" {
		t.Skip("CGI git-http-backend fixture requires Unix process semantics")
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
	s, _ := newCredentialDiscoveryScheduler(t, apiServer)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, gitserver.GitCredentials{
		Token: "rejected-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))
	s.refreshCredentialsIfNeeded()
	require.Zero(t, calls.Load())

	gitRoot := t.TempDir()
	gitInDir(t, gitRoot, "init", "--bare", "ledger.git")
	gitExec, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + gitRoot, "GIT_HTTP_EXPORT_ALL=1"},
	}
	var acceptRecovered atomic.Bool
	gitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, token, _ := r.BasicAuth()
		if acceptRecovered.Load() && username == "oauth2" && token == "replacement-pat" {
			backend.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	err = s.doPull(context.Background(), nil, true, false)
	require.Error(t, err)
	require.True(t, gitserver.IsAuthFailure(err.Error()), "the real Git fetch must fail on authentication")
	require.EqualValues(t, 1, calls.Load(), "ledger fetch authentication failures must force credential refresh")
	creds, err := gitserver.LoadCredentialsForEndpoint(apiServer.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "replacement-pat", creds.Token)

	creds.ServerURL = gitServer.URL
	creds.AddRepo(gitserver.RepoEntry{TeamID: "team_probe", URL: gitServer.URL + "/ledger.git"})
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, *creds))
	acceptRecovered.Store(true)
	s.refreshAfterAuthFailure(errors.New("authentication failed"))
	require.EqualValues(t, 1, calls.Load(), "a PAT already repaired by the helper must not be fetched again")
}
