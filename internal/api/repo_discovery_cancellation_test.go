package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Discovery must honor cancellation while waiting for headers and while
// reading a response body; otherwise daemon shutdown can outlive its worker budget.
func TestRepoDiscovery_ContextCancelsStalledRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		path string
		call func(context.Context, *RepoClient) error
	}{
		{"repo detail", "/api/v1/cli/repos/repo_cancel", func(ctx context.Context, client *RepoClient) error {
			_, err := client.GetRepoDetailContext(ctx, "repo_cancel")
			return err
		}},
		{"ledger status", "/api/v1/repos/repo_cancel/ledger-status", func(ctx context.Context, client *RepoClient) error {
			_, err := client.GetLedgerStatusContext(ctx, "repo_cancel")
			return err
		}},
	} {
		for _, bodyStarted := range []bool{false, true} {
			phase := "headers"
			if bodyStarted {
				phase = "body"
			}
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				t.Parallel()
				started := make(chan struct{})
				canceled := make(chan struct{})
				release := make(chan struct{})
				var releaseOnce sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != tc.path {
						t.Errorf("unexpected discovery request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if bodyStarted {
						_, _ = w.Write([]byte("{"))
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
					}
				}))
				ctx, cancel := context.WithCancel(context.Background())
				result := make(chan error, 1)
				t.Cleanup(func() {
					cancel()
					releaseOnce.Do(func() { close(release) })
					server.Close()
				})
				go func() { result <- tc.call(ctx, NewRepoClientWithEndpoint(server.URL)) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("discovery request never reached the server")
				}
				cancel()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				select {
				case err := <-result:
					require.ErrorIs(t, err, context.Canceled)
				case <-deadline.C:
					t.Fatal("discovery ignored caller cancellation")
				}
				select {
				case <-canceled:
				case <-deadline.C:
					t.Fatal("discovery HTTP request survived cancellation")
				}
			})
		}
	}
}
