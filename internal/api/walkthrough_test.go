package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Recovery must remain scoped, bounded, explicit, and unable to follow a
// redirect carrying the user's credential to a video/object-store hostname.
func TestWalkthroughRecoveryContract(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		require.Equal(t, "/api/v1/teams/team_test/recordings/rec_test/walkthrough/extractions", r.URL.Path)
		var body WalkthroughExtractionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, 5, body.MaxFrames)
		require.Equal(t, 1280, body.MaxWidth)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"job_test","status":"queued"}`))
	}))
	defer server.Close()
	client := NewRepoClientWithEndpoint(server.URL).WithAuthToken("secret")
	req := &WalkthroughExtractionRequest{Revision: strings.Repeat("a", 64), CueFirst: 2, CueLast: 3, MaxFrames: 5, MaxWidth: 1280}
	result, err := client.WalkthroughExtraction(context.Background(), "team_test", "rec_test", "", req)
	require.NoError(t, err)
	require.Equal(t, "queued", result.Status)
	require.Equal(t, 1, calls)
	_, err = client.WalkthroughExtraction(context.Background(), "../escape", "rec_test", "", req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
	req.MaxFrames = 9
	_, err = client.WalkthroughExtraction(context.Background(), "team_test", "rec_test", "", req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestWalkthroughRecoveryDoesNotFollowRedirect(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("credential-bearing redirect followed") }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	_, err := NewRepoClientWithEndpoint(source.URL).WithAuthToken("secret").WalkthroughExtraction(context.Background(), "team_test", "rec_test", "job_test", nil)
	require.Error(t, err)
}

func TestWalkthroughPrepareLegacyContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/teams/team_test/recordings/rec_test/walkthrough/prepare", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Empty(t, body)
		_, _ = w.Write([]byte(`{"job_id":"prepare_test","status":"queued"}`))
	}))
	defer server.Close()
	result, err := NewRepoClientWithEndpoint(server.URL).WithAuthToken("secret").PrepareWalkthrough(context.Background(), "team_test", "rec_test")
	require.NoError(t, err)
	require.Equal(t, "queued", result.Status)
	require.Equal(t, 1, calls)
}

// A retry is explicit transport intent; a failed receipt alone never creates
// another paid job. Both operation types preserve omission on ordinary replay.
func TestWalkthroughRetryIsExplicitTransport(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("prepare=%t/retry=%t", prepare, retry), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					if retry {
						require.Equal(t, true, body["retry"])
					} else {
						require.NotContains(t, body, "retry")
					}
					_, _ = w.Write([]byte(`{"job_id":"retry_test","status":"queued"}`))
				}))
				defer server.Close()
				client := NewRepoClientWithEndpoint(server.URL).WithAuthToken("secret")
				var err error
				if prepare {
					_, err = client.PrepareWalkthrough(context.Background(), "team_test", "rec_test", retry)
				} else {
					_, err = client.WalkthroughExtraction(context.Background(), "team_test", "rec_test", "", &WalkthroughExtractionRequest{Revision: strings.Repeat("a", 64), CueFirst: 1, CueLast: 1, MaxFrames: 5, MaxWidth: 1280, Retry: retry})
				}
				require.NoError(t, err)
			})
		}
	}
}
