package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func TestGitHubRepoSnapshotsPollsAndSkipsKnown(t *testing.T) {
	id := strings.Repeat("a", 40)
	catalogs, blobs := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		if r.URL.Query().Get("view") == "catalog" {
			catalogs++
			fmt.Fprintf(w, `{"entries":[{"id":"%s","kind":"pr","number":7,"status":"ready"}],"complete":%t}`, id, catalogs > 1)
			return
		}
		blobs++
		fmt.Fprint(w, `{"number":7,"title":"thread"}`)
	}))
	defer server.Close()
	c := NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")
	raw, entries, err := c.GitHubRepoSnapshots(context.Background(), "repo_1", nil)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	require.Len(t, entries, 1)
	require.Equal(t, 2, catalogs)
	require.Equal(t, 1, blobs)
	_, entries, err = c.GitHubRepoSnapshots(context.Background(), "repo_1", map[string]bool{"pr:" + id: true})
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Equal(t, 1, blobs)
}

func TestGitHubRepoSnapshotsUnavailableIsNotEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer server.Close()
	c := NewRepoClientWithEndpoint(server.URL)
	_, _, err := c.GitHubRepoSnapshots(context.Background(), "repo_1", nil)
	require.ErrorContains(t, err, "HTTP 503")
}

// A catalog chooses the only snapshot paths that may receive the selected repo's credential.
func TestGitHubRepoSnapshotsReadsOnlySelectedCatalogEntries(t *testing.T) {
	prID, issueID := strings.Repeat("a", 40), strings.Repeat("b", 64)
	catalog := GitHubCatalog{Complete: true, Entries: []GitHubCatalogEntry{
		{ID: prID, Kind: "pr", Number: 7, Status: "ready"},
		{ID: issueID, Kind: "issue", Number: 8, Status: "ready"},
	}}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
		switch r.URL.RequestURI() {
		case "/api/v1/repos/repo_selected/github?view=catalog":
			require.NoError(t, json.NewEncoder(w).Encode(catalog))
		case "/api/v1/repos/repo_selected/github/" + prID:
			fmt.Fprint(w, `{"number":7,"title":"selected PR"}`)
		case "/api/v1/repos/repo_selected/github/" + issueID:
			fmt.Fprint(w, `{"number":8,"title":"selected issue"}`)
		default:
			t.Errorf("unexpected repository path %q", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	raw, entries, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("fixture-token").GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
	require.NoError(t, err)
	require.Equal(t, catalog.Entries, entries)
	require.Equal(t, []json.RawMessage{json.RawMessage(`{"number":7,"title":"selected PR"}`), json.RawMessage(`{"number":8,"title":"selected issue"}`)}, raw)
	require.Equal(t, []string{"/api/v1/repos/repo_selected/github?view=catalog", "/api/v1/repos/repo_selected/github/" + prID, "/api/v1/repos/repo_selected/github/" + issueID}, paths)
}

// Untrusted catalog identity fields must never become request paths.
func TestGitHubRepoSnapshotsRejectsInvalidSelectionBeforeFetching(t *testing.T) {
	for _, repoID := range []string{"", "../repo_other", "repo_1/path", "repo_1?view=other", "repo_1#other"} {
		t.Run("repo="+repoID, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; w.WriteHeader(http.StatusOK) }))
			t.Cleanup(server.Close)
			raw, entries, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("fixture-token").GitHubRepoSnapshots(context.Background(), repoID, nil)
			require.ErrorContains(t, err, "invalid repository ID")
			require.Nil(t, raw)
			require.Nil(t, entries)
			require.Zero(t, requests, "a rejected repo must not receive any authenticated request")
		})
	}
	for _, tc := range []struct {
		name, body, message string
		catalogs            int
	}{
		{"malformed JSON", `{"entries":`, "decode GitHub catalog", 1},
		{"incomplete", `{"entries":[],"complete":false}`, "catalog is incomplete", 45},
		{"unavailable", fmt.Sprintf(`{"entries":[{"id":%q,"kind":"pr","number":1,"status":"pending"}],"complete":true}`, strings.Repeat("a", 40)), "unavailable entries", 1},
		{"cross repo path", `{"entries":[{"id":"../repo_other/github/secret","kind":"pr","number":1,"status":"ready"}],"complete":true}`, "unavailable entries", 1},
		{"unknown kind", fmt.Sprintf(`{"entries":[{"id":%q,"kind":"commit","number":1,"status":"ready"}],"complete":true}`, strings.Repeat("a", 40)), "unavailable entries", 1},
		{"missing number", fmt.Sprintf(`{"entries":[{"id":%q,"kind":"pr","number":0,"status":"ready"}],"complete":true}`, strings.Repeat("a", 40)), "unavailable entries", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				require.Equal(t, "/api/v1/repos/repo_selected/github?view=catalog", r.URL.RequestURI())
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(server.Close)
			raw, entries, err := NewRepoClientWithEndpoint(server.URL).GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
			require.ErrorContains(t, err, tc.message)
			require.Nil(t, raw)
			require.Nil(t, entries)
			require.Equal(t, tc.catalogs, requests, "invalid catalogs must never lead to snapshot reads")
		})
	}
}

// Neither denied access nor corrupt bytes can masquerade as an empty or partially complete refresh.
func TestGitHubRepoSnapshotsDoesNotReturnPartialResults(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name   string
		status int
		blob   string
	}{
		{"unauthorized", http.StatusUnauthorized, "private-error-body"},
		{"forbidden", http.StatusForbidden, "private-error-body"},
		{"unavailable", http.StatusServiceUnavailable, "private-error-body"},
		{"malformed snapshot", http.StatusOK, `{"number":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogs, blobs := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("view") == "catalog" {
					catalogs++
					fmt.Fprintf(w, `{"entries":[{"id":%q,"kind":"pr","number":1,"status":"ready"},{"id":%q,"kind":"issue","number":2,"status":"ready"}],"complete":true}`, first, second)
					return
				}
				blobs++
				if strings.HasSuffix(r.URL.Path, first) {
					fmt.Fprint(w, `{"number":1}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.blob)
			}))
			t.Cleanup(server.Close)
			raw, entries, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("fixture-token").GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
			require.Error(t, err)
			if tc.status != http.StatusOK {
				var httpErr *githubRepoHTTPError
				require.ErrorAs(t, err, &httpErr)
				require.Equal(t, tc.status, httpErr.status)
			} else {
				require.ErrorContains(t, err, "invalid GitHub snapshot")
			}
			require.NotContains(t, err.Error(), "private-error-body")
			require.NotContains(t, err.Error(), "fixture-token")
			require.Nil(t, raw)
			require.Nil(t, entries)
			require.Equal(t, 1, catalogs)
			require.Equal(t, 2, blobs)
		})
	}
}

// A superseded snapshot forces a fresh selection; it must not re-use the stale catalog.
func TestGitHubRepoSnapshotsRefreshesCatalogAfterMissingBlob(t *testing.T) {
	oldID, newID := strings.Repeat("a", 40), strings.Repeat("b", 40)
	catalogs, blobs := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") == "catalog" {
			catalogs++
			id := oldID
			if catalogs > 1 {
				id = newID
			}
			fmt.Fprintf(w, `{"entries":[{"id":%q,"kind":"pr","number":1,"status":"ready"}],"complete":true}`, id)
			return
		}
		blobs++
		if strings.HasSuffix(r.URL.Path, oldID) {
			http.NotFound(w, r)
			return
		}
		require.True(t, strings.HasSuffix(r.URL.Path, newID))
		fmt.Fprint(w, `{"number":1,"title":"new selection"}`)
	}))
	t.Cleanup(server.Close)
	raw, entries, err := NewRepoClientWithEndpoint(server.URL).GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
	require.NoError(t, err)
	require.Equal(t, newID, entries[0].ID)
	require.JSONEq(t, `{"number":1,"title":"new selection"}`, string(raw[0]))
	require.Equal(t, 2, catalogs)
	require.Equal(t, 2, blobs)
}

func TestGitHubRepoSnapshotsBoundsRepeatedMissingCatalog(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.NotFound(w, r) }))
	t.Cleanup(server.Close)
	raw, entries, err := NewRepoClientWithEndpoint(server.URL).GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
	require.ErrorContains(t, err, "HTTP 404")
	require.Nil(t, raw)
	require.Nil(t, entries)
	require.Equal(t, 2, requests, "a missing selected repo gets one retry, not an unbounded loop")
}

func TestGitHubRepoSnapshotsPropagatesInterruptedReads(t *testing.T) {
	t.Run("canceled request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		raw, entries, err := NewRepoClientWithEndpoint("http://127.0.0.1").GitHubRepoSnapshots(ctx, "repo_selected", nil)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, raw)
		require.Nil(t, entries)
	})
	t.Run("truncated response", func(t *testing.T) {
		client := NewRepoClientWithEndpoint("https://example.test")
		client.httpClient.Transport = supportRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)), Header: make(http.Header)}, nil
		})
		raw, entries, err := client.GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.Nil(t, raw)
		require.Nil(t, entries)
	})
	t.Run("invalid endpoint", func(t *testing.T) {
		raw, entries, err := NewRepoClientWithEndpoint("http://[invalid").GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
		require.Error(t, err)
		require.Nil(t, raw)
		require.Nil(t, entries)
	})
}

// The cap must distinguish an exactly maximal valid snapshot from a truncated oversized one.
func TestGitHubRepoSnapshotsBoundsSnapshotBytes(t *testing.T) {
	const limit = 1 << 20
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			id := strings.Repeat("a", 40)
			prefix, suffix := `{"body":"`, `"}`
			body := prefix + strings.Repeat("x", limit+extra-len(prefix)-len(suffix)) + suffix
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("view") == "catalog" {
					fmt.Fprintf(w, `{"entries":[{"id":%q,"kind":"pr","number":1,"status":"ready"}],"complete":true}`, id)
					return
				}
				fmt.Fprint(w, body)
			}))
			t.Cleanup(server.Close)
			raw, entries, err := NewRepoClientWithEndpoint(server.URL).GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
			if extra == 0 {
				require.NoError(t, err)
				require.Len(t, raw, 1)
				require.Len(t, entries, 1)
				require.Equal(t, body, string(raw[0]))
			} else {
				require.ErrorContains(t, err, "response too large")
				require.Nil(t, raw)
				require.Nil(t, entries)
			}
		})
	}
}

// A redirect must not take the selected repository's credential to another resource.
func TestGitHubRepoSnapshotsRejectsCrossRepoRedirect(t *testing.T) {
	for _, stage := range []string{"catalog", "snapshot"} {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				redirected, credentialForwarded := false, false
				id := strings.Repeat("a", 40)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/repos/repo_selected/github" && stage == "snapshot" {
						fmt.Fprintf(w, `{"entries":[{"id":%q,"kind":"pr","number":1,"status":"ready"}],"complete":true}`, id)
						return
					}
					if strings.HasPrefix(r.URL.Path, "/api/v1/repos/repo_selected/github") {
						http.Redirect(w, r, "/api/v1/repos/repo_other/github?view=catalog", status)
						return
					}
					redirected = true
					credentialForwarded = r.Header.Get("Authorization") != ""
					fmt.Fprint(w, `{"entries":[],"complete":true}`)
				}))
				t.Cleanup(server.Close)
				raw, entries, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("fixture-token").GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
				require.False(t, credentialForwarded, "the selected repo credential must never reach a redirected resource")
				require.False(t, redirected, "a redirect cannot change the selected repository")
				require.ErrorContains(t, err, fmt.Sprintf("HTTP %d", status))
				require.Nil(t, raw)
				require.Nil(t, entries)
			})
		}
	}
}

func TestGitHubRepoSnapshotsCatalogDenialIsNotEmptySuccess(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				http.Error(w, "private-response-body", status)
			}))
			t.Cleanup(server.Close)
			raw, entries, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("fixture-token").GitHubRepoSnapshots(context.Background(), "repo_selected", nil)
			require.ErrorContains(t, err, fmt.Sprintf("HTTP %d", status))
			require.NotContains(t, err.Error(), "private-response-body")
			require.NotContains(t, err.Error(), "fixture-token")
			require.Nil(t, raw)
			require.Nil(t, entries)
			require.Equal(t, 1, requests, "denied access must not trigger snapshot reads or retries")
		})
	}
}
