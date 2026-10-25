package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// Ledger discovery also runs inside the delayed startup callback. Both its
// repo-detail request and its legacy fallback must finish when Start is canceled.
func TestSyncScheduler_StartupLedgerDiscoveryHonorsCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real five-second scheduler startup delay")
	}
	for _, fallback := range []bool{false, true} {
		name := "repo detail"
		if fallback {
			name = "ledger status fallback"
		}
		t.Run(name, func(t *testing.T) {
			const repoID = "repo_startup_cancel"
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := "/api/v1/cli/repos/" + repoID
				if fallback && r.URL.Path == path {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if fallback {
					path = "/api/v1/repos/" + repoID + "/ledger-status"
				}
				if r.URL.Path != path {
					t.Errorf("unexpected Ledger discovery request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				// Let Start's initial anti-entropy check and immediate pull
				// check finish. Only the five-second startup team callback stalls.
				switch calls.Add(1) {
				case 1, 2:
					if fallback {
						_ = json.NewEncoder(w).Encode(api.LedgerStatusResponse{Status: "pending"})
					} else {
						_ = json.NewEncoder(w).Encode(api.RepoDetailResponse{Ledger: &api.RepoDetailLedger{Status: "pending"}})
					}
				case 3:
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				default:
					t.Error("Ledger discovery issued another request after cancellation")
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				server.Close()
			})
			isolated := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))
			t.Setenv("OX_XDG_DISABLE", "")
			scheduler, _ := newCredentialDiscoveryScheduler(t, server)
			require.NoError(t, config.SaveProjectConfig(scheduler.config.ProjectRoot, &config.ProjectConfig{
				RepoID: repoID, Endpoint: server.URL,
			}))
			require.NoError(t, config.SaveLocalConfig(scheduler.config.ProjectRoot, &config.LocalConfig{}))
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
				Token: "fresh-cached-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
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
				releaseOnce.Do(func() { close(release) })
				awaitStartupCompletion(t, done)
			})
			begin := time.Now()
			go func() {
				scheduler.Start(ctx)
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("delayed startup Ledger discovery never reached the server")
			}
			require.GreaterOrEqual(t, time.Since(begin), 5*time.Second, "must exercise the delayed startup callback")
			cancel()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			select {
			case <-done:
			case <-deadline.C:
				t.Fatal("startup Ledger discovery exceeded the five-second shutdown window")
			}
			select {
			case <-canceled:
			case <-deadline.C:
				t.Fatal("Ledger discovery HTTP request survived scheduler shutdown")
			}
			require.EqualValues(t, 3, calls.Load())
			failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger-api")
			require.Zero(t, failures, "cancellation must not put Ledger discovery into backoff")
		})
	}
}
