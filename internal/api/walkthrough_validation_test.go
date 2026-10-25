package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalkthroughInvalidRequestsNeverSpendServerCompute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("invalid request reached server") }))
	defer server.Close()
	client := NewRepoClientWithEndpoint(server.URL)
	number := func(v float64) *float64 { return &v }
	cases := map[string]func(*WalkthroughExtractionRequest){
		"invalid revision":   func(r *WalkthroughExtractionRequest) { r.Revision = "not-a-digest" },
		"too small width":    func(r *WalkthroughExtractionRequest) { r.MaxWidth = 319 },
		"no range":           func(r *WalkthroughExtractionRequest) { r.CueFirst = 0; r.CueLast = 0 },
		"both ranges":        func(r *WalkthroughExtractionRequest) { r.FromSeconds = number(0); r.ToSeconds = number(1) },
		"reversed cues":      func(r *WalkthroughExtractionRequest) { r.CueLast = 1 },
		"too many cues":      func(r *WalkthroughExtractionRequest) { r.CueLast = 52 },
		"missing time bound": func(r *WalkthroughExtractionRequest) { r.CueFirst = 0; r.CueLast = 0; r.FromSeconds = number(0) },
		"unbounded window": func(r *WalkthroughExtractionRequest) {
			r.CueFirst = 0
			r.CueLast = 0
			r.FromSeconds = number(0)
			r.ToSeconds = number(61)
		},
		"nonfinite time": func(r *WalkthroughExtractionRequest) {
			r.CueFirst = 0
			r.CueLast = 0
			r.FromSeconds = number(math.NaN())
			r.ToSeconds = number(1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := &WalkthroughExtractionRequest{Revision: strings.Repeat("a", 64), CueFirst: 2, CueLast: 3, MaxFrames: 5, MaxWidth: 1280}
			mutate(req)
			_, err := client.WalkthroughExtraction(t.Context(), "team_test", "rec_test", "", req)
			require.Error(t, err)
		})
	}
	_, err := client.WalkthroughExtraction(t.Context(), "team_test", "rec_test", "", nil)
	require.ErrorContains(t, err, "request is required")
	_, err = client.WalkthroughExtraction(t.Context(), "team_test", "rec_test", "../foreign", nil)
	require.ErrorContains(t, err, "job ID")
	_, err = client.PrepareWalkthrough(t.Context(), "team_test", "rec/foreign")
	require.ErrorContains(t, err, "scope")
	require.Zero(t, calls)
}

func TestWalkthroughReceiptBoundary(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"unauthorized", `{"private":"secret"}`, "unauthorized", 401},
		{"budget", `{"private":"secret"}`, "HTTP 429", 429},
		{"oversized", strings.Repeat("x", 65537), "exceeds limit", 200},
		{"invalid JSON", "{", "invalid extraction receipt", 200},
		{"unpinned completion", `{"job_id":"job_test","status":"completed","revision":"mutable"}`, "immutable evidence revision", 200},
		{"unsafe identity", `{"job_id":"../foreign","status":"running"}`, "invalid job identity", 200},
		{"unknown status", `{"job_id":"job_test","status":"invented"}`, "invalid extraction status", 200},
		{"completed", fmt.Sprintf(`{"job_id":"job_test","status":"completed","revision":%q,"sync_required":true}`, strings.Repeat("a", 64)), "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Empty(t, r.Header.Get("Content-Type"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result, err := NewRepoClientWithEndpoint(server.URL).WalkthroughExtraction(t.Context(), "team_test", "rec_test", "job_test", nil)
			if tc.status == 401 {
				require.ErrorIs(t, err, ErrUnauthorized)
			} else if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				require.NotContains(t, err.Error(), "secret")
			} else {
				require.NoError(t, err)
				require.True(t, result.SyncRequired)
				require.Equal(t, strings.Repeat("a", 64), result.Revision)
			}
			require.Equal(t, 1, calls, "a refusal must not cause a paid retry")
		})
	}
}

func TestWalkthroughTimeWindowAndCanceledTransport(t *testing.T) {
	from, to := 1.0, 61.0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"job_id":"job_test","status":"queued"}`))
	}))
	defer server.Close()
	client := NewRepoClientWithEndpoint(server.URL)
	req := &WalkthroughExtractionRequest{Revision: strings.Repeat("a", 64), FromSeconds: &from, ToSeconds: &to, MaxFrames: 8, MaxWidth: 4096}
	_, err := client.WalkthroughExtraction(t.Context(), "team_test", "rec_test", "", req)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.WalkthroughExtraction(ctx, "team_test", "rec_test", "job_test", nil)
	require.ErrorIs(t, err, context.Canceled)
	client.baseURL = ":invalid"
	_, err = client.PrepareWalkthrough(t.Context(), "team_test", "rec_test")
	require.Error(t, err)
}
