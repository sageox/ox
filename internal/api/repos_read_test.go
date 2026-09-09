package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: missing new backend support uses a human credential or
// old GitLab URL, redirects disclose the selected TAT, or raw errors leak it.
func TestLedgerReadDiscoveryFailsClosed(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	const token = "oxt_test_1ljPfr"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"disabled", 200, `{"ledger":{"status":"ready","repo_url":"https://git.sageox.ai/legacy.git"}}`, ErrLedgerReadUnavailable},
		{"no ledger", 200, `{"ledger":null}`, ErrLedgerReadMissing},
		{"pending", 200, `{"ledger":{"status":"pending"}}`, ErrLedgerReadMissing},
		{"foreign", 200, `{"ledger":{"status":"ready","read_url":"https://foreign.example/ledger.git"}}`, gitserver.ErrUnsafeReadTransport},
		{"malformed", 200, token, ErrLedgerReadUnavailable},
		{"unknown repo", 404, token, ErrLedgerReadUnavailable},
		{"denied", 403, token, ErrForbidden},
		{"revoked", 401, token, ErrUnauthorized},
		{"server failed", 500, token, ErrLedgerReadUnavailable},
		{"redirect", 302, token, ErrLedgerReadUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, "/api/v1/cli/repos/"+repoID, r.URL.Path)
				assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := NewRepoClientWithEndpoint(server.URL).WithAuthToken("human-token-must-not-be-used")
			client.httpClient = server.Client()
			readURL, err := client.GetLedgerReadURL(context.Background(), repoID, token)
			require.Empty(t, readURL)
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), token)
			require.Equal(t, 1, requests)
		})
	}
}

// Failure prevented: additive read discovery breaks old repo detail decoding or
// substitutes a direct GitLab clone URL for the authorized SageOx resource.
func TestLedgerReadDiscoveryUsesAuthorizedAdditiveURL(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"ledger":{"status":"ready","repo_url":"https://git.sageox.ai/legacy.git","read_url":"https://%s/api/v1/cli/repos/%s/ledger.git"}}`, r.Host, repoID)
	}))
	defer server.Close()
	client := NewRepoClientWithEndpoint(server.URL)
	client.httpClient = server.Client()
	readURL, err := client.GetLedgerReadURL(context.Background(), repoID, "oxt_test_1ljPfr")
	require.NoError(t, err)
	require.Equal(t, server.URL+"/api/v1/cli/repos/"+repoID+"/ledger.git", readURL)
}

// Invalid selection and missing credentials must fail before a request can
// escape; losing the discovery connection must not select a cached human login.
func TestLedgerReadDiscoveryRejectsSelectionBeforeNetwork(t *testing.T) {
	const repoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	const token = "oxt_test_1ljPfr"
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Error("invalid read selection reached the server")
	}))
	defer server.Close()
	for _, tc := range []struct{ endpoint, repo, token string }{
		{server.URL, "../foreign", token},
		{"http://example.invalid", repoID, token},
		{server.URL, repoID, ""},
		{server.URL, repoID, "human-token"},
		{server.URL, repoID, token + "\n"},
	} {
		client := NewRepoClientWithEndpoint(tc.endpoint).WithAuthToken("human-token")
		client.httpClient = server.Client()
		readURL, err := client.GetLedgerReadURL(context.Background(), tc.repo, tc.token)
		require.Error(t, err)
		require.Empty(t, readURL)
	}
	require.Zero(t, requests)
	client := NewRepoClientWithEndpoint(server.URL)
	client.httpClient = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetLedgerReadURL(ctx, repoID, token)
	require.ErrorIs(t, err, context.Canceled)
	server.Close()
	_, err = client.GetLedgerReadURL(context.Background(), repoID, token)
	require.ErrorIs(t, err, ErrLedgerReadUnavailable)
	require.NotContains(t, err.Error(), token)
}
