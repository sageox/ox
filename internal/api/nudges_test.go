package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nudgeTestClient(baseURL string) *RepoClient {
	return &RepoClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
}

// A real server card plus fields this release has never heard of (a newer
// server's art, kind and first_view). Failure prevented: a strict decode makes
// the server unable to grow the card without breaking every released ox.
const nudgeCardWithUnknownFields = `{
	"placement": "cli.hint",
	"card": {
		"nudge_id": "star-ox-github",
		"kind": "ask",
		"subject": "user",
		"launch_id": null,
		"eyebrow": null,
		"title": "Enjoying ox?",
		"body": "If ox has been useful, a star on GitHub helps other teams find it.",
		"cta": {"label": "Star on GitHub", "href": "/cli", "future_field": true},
		"dismiss_label": "Dismiss",
		"art": {"asset": "brand/oxy-cta", "fit": "contain", "session_grid": {"counts": [1]}},
		"first_view": true,
		"token": "tok_abc"
	},
	"some_future_top_level_field": 7
}`

func TestGetNudge_DecodesCardAndSendsPlacementAndAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/users/me/nudges", r.URL.Path)
		assert.Equal(t, "cli.hint", r.URL.Query().Get("placement"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(nudgeCardWithUnknownFields))
	}))
	defer srv.Close()

	resp, err := nudgeTestClient(srv.URL).GetNudge(context.Background(), NudgePlacementCLIHint)
	require.NoError(t, err)
	require.NotNil(t, resp.Card)
	assert.Equal(t, "star-ox-github", resp.Card.NudgeID)
	assert.Equal(t, "Enjoying ox?", resp.Card.Title)
	assert.Equal(t, "If ox has been useful, a star on GitHub helps other teams find it.", resp.Card.Body)
	assert.Equal(t, "/cli", resp.Card.CTA.Href)
	assert.Equal(t, "tok_abc", resp.Card.Token)
}

// The server answers "nothing to show" with a null card. That is an answer, not
// an error: the caller caches it for a day instead of retrying.
func TestGetNudge_NullCardIsAnAnswerNotAnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"placement":"cli.hint","card":null}`))
	}))
	defer srv.Close()

	resp, err := nudgeTestClient(srv.URL).GetNudge(context.Background(), NudgePlacementCLIHint)
	require.NoError(t, err)
	assert.Nil(t, resp.Card)
}

func TestGetNudge_StatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      int
		body        string
		wantErrIs   error
		wantAnyErr  bool
		wantNotSent bool // must NOT be the unsupported sentinel
	}{
		{name: "404 is an older server", status: http.StatusNotFound, body: `{"error":"not found"}`, wantErrIs: ErrNudgesUnsupported},
		{name: "unknown_placement is an older server", status: http.StatusOK, body: `{"placement":"cli.hint","card":null,"unknown_placement":true}`, wantErrIs: ErrNudgesUnsupported},
		{name: "401 is unauthorized", status: http.StatusUnauthorized, body: `{}`, wantErrIs: ErrUnauthorized},
		{name: "500 is a plain failure", status: http.StatusInternalServerError, body: `oops`, wantAnyErr: true, wantNotSent: true},
		{name: "426 is a plain failure", status: http.StatusUpgradeRequired, body: `{}`, wantAnyErr: true, wantNotSent: true},
		{name: "garbage body is a plain failure", status: http.StatusOK, body: `<html>`, wantAnyErr: true, wantNotSent: true},
		{name: "oversized body is refused", status: http.StatusOK, body: `{"placement":"` + strings.Repeat("x", maxNudgeBytes) + `"}`, wantAnyErr: true, wantNotSent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			resp, err := nudgeTestClient(srv.URL).GetNudge(context.Background(), NudgePlacementCLIHint)
			require.Error(t, err)
			assert.Nil(t, resp)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			if tt.wantNotSent {
				assert.False(t, errors.Is(err, ErrNudgesUnsupported), "a failure must not read as 'unsupported', which is cached for a day")
			}
		})
	}
}

// The caller's context is the time budget. Failure prevented: a slow server
// holds a command open for the client default (10s) instead of the hint's
// budget.
func TestGetNudge_HonorsContextDeadline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := nudgeTestClient(srv.URL).GetNudge(ctx, NudgePlacementCLIHint)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

// Failure prevented: an advisory hint prints "CLI Version No Longer Supported"
// in the middle of an unrelated command. The real commands own that banner.
// Serial on purpose: it swaps a package-level writer.
func TestGetNudge_NeverPrintsAVersionBanner(t *testing.T) {
	var banner strings.Builder
	old := noticeOut
	noticeOut = &banner
	t.Cleanup(func() { noticeOut = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderMinVersion, "99.0.0")
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	_, err := nudgeTestClient(srv.URL).GetNudge(context.Background(), NudgePlacementCLIHint)
	require.Error(t, err)
	assert.Empty(t, banner.String())
}

func TestReportNudgeSeen_PostsTokenInPathWithPlacement(t *testing.T) {
	t.Parallel()
	var gotMethod, gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotAuth = r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// a token with a path separator and a space must stay one path segment
	require.NoError(t, nudgeTestClient(srv.URL).ReportNudgeSeen(context.Background(), "tok/with space", NudgePlacementCLIHint))
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/users/me/nudges/tok%2Fwith%20space/seen", gotPath)
	assert.Equal(t, "placement=cli.hint", gotQuery)
	assert.Equal(t, "Bearer test-token", gotAuth)
}

func TestReportNudgeSeen_Failures(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := nudgeTestClient(srv.URL)
	assert.Error(t, c.ReportNudgeSeen(context.Background(), "tok", NudgePlacementCLIHint), "a non-2xx must surface so the caller can log it")
	assert.Error(t, c.ReportNudgeSeen(context.Background(), "", NudgePlacementCLIHint), "no token means nothing to report")
}
