package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalkthroughPlaybackRequestIsScopedAndAuthorized(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/teams/team_test/recordings/rec_test/walkthrough/playback", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"spec":"walkthrough-playback/1"}`))
	}))
	defer srv.Close()

	raw, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("secret").WalkthroughPlayback(context.Background(), "team_test", "rec_test")
	require.NoError(t, err)
	require.JSONEq(t, `{"spec":"walkthrough-playback/1"}`, string(raw))
	require.Equal(t, 1, calls)
}

// TestWalkthroughPlaybackStatusMapping: absence is a normal answer the caller
// falls back from; refusals and server faults are distinguishable from it.
// Failure prevented: a 404 is reported as an outage (or an outage as "no
// playback"), and the reader is told the wrong reason for a missing view.
func TestWalkthroughPlaybackStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		target error
		text   string
	}{
		{"not built", http.StatusNotFound, ErrWalkthroughPlaybackUnavailable, ""},
		{"signed out", http.StatusUnauthorized, ErrUnauthorized, ""},
		{"server fault", http.StatusInternalServerError, nil, "HTTP 500"},
		{"redirect is not followed", http.StatusFound, nil, "HTTP 302"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"walkthrough_playback_unavailable"}`))
			}))
			defer srv.Close()
			_, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("secret").WalkthroughPlayback(context.Background(), "team_test", "rec_test")
			require.Error(t, err)
			if tc.target != nil {
				require.True(t, errors.Is(err, tc.target), "got %v", err)
			} else {
				require.False(t, errors.Is(err, ErrWalkthroughPlaybackUnavailable))
				require.Contains(t, err.Error(), tc.text)
			}
		})
	}
}

func TestWalkthroughPlaybackRefusesOversizedAndMalformedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxWalkthroughPlaybackBytes+1)))
	}))
	defer srv.Close()
	client := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("secret")
	_, err := client.WalkthroughPlayback(context.Background(), "team_test", "rec_test")
	require.ErrorContains(t, err, "size limit")

	for _, bad := range [][2]string{{"team/../x", "rec_test"}, {"team_test", "rec_test/../../x"}, {"", "rec_test"}} {
		_, err = client.WalkthroughPlayback(context.Background(), bad[0], bad[1])
		require.ErrorContains(t, err, "invalid walkthrough scope")
	}
}

// The image link is its own credential. The bearer token must never ride
// along (the object store would reject two credentials, and it would leak the
// login to whatever host the link names), and the link must not leak into
// errors.
func TestDownloadWalkthroughImageSendsNoLoginAndKeepsTheLinkOutOfErrors(t *testing.T) {
	var sawAuth, sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		if r.URL.Path == "/denied" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("image-bytes"))
	}))

	body, err := DownloadWalkthroughImage(context.Background(), srv.URL+"/ok.jpg?sig=SIGNED-SECRET", 1024)
	require.NoError(t, err)
	require.Equal(t, "image-bytes", string(body))
	require.Empty(t, sawAuth)
	require.Equal(t, "sig=SIGNED-SECRET", sawQuery)

	_, err = DownloadWalkthroughImage(context.Background(), srv.URL+"/denied?sig=SIGNED-SECRET", 1024)
	require.ErrorContains(t, err, "HTTP 403")
	require.NotContains(t, err.Error(), "SIGNED-SECRET")

	link := srv.URL + "/gone.jpg?sig=SIGNED-SECRET"
	srv.Close()
	_, err = DownloadWalkthroughImage(context.Background(), link, 1024)
	require.Error(t, err, "the server is gone")
	require.NotContains(t, err.Error(), "SIGNED-SECRET", "net/http errors embed the URL; it must be stripped")
}

func TestDownloadWalkthroughImageEnforcesTransportAndSize(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	_, err := DownloadWalkthroughImage(context.Background(), srv.URL+"/big", 99)
	require.ErrorContains(t, err, "limit")
	_, err = DownloadWalkthroughImage(context.Background(), srv.URL+"/redirect", 1024)
	require.ErrorContains(t, err, "HTTP 302")
	require.Zero(t, hits, "a redirect is never followed")

	for _, link := range []string{"http://objects.example.test/a.jpg", "ftp://objects.example.test/a.jpg", "file:///etc/passwd", "https://user:pw@objects.example.test/a.jpg", "https:///a.jpg", "::bad"} {
		_, err = DownloadWalkthroughImage(context.Background(), link, 1024)
		require.Error(t, err, link)
		require.NotContains(t, err.Error(), "pw@")
	}
}
