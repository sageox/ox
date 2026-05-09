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

// newTestKBClient builds a KBClient bound to a test server. Kept tiny on
// purpose so each test reads top-to-bottom without indirection.
func newTestKBClient(baseURL string) *KBClient {
	return &KBClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authToken:  "test-token",
		version:    "test-version",
	}
}

// TestListBubbles_Success_EnvelopeShape verifies the client decodes the
// canonical {"bubbles": [...]} envelope with all expected fields populated.
// Failure prevented: a JSON shape change at the server silently dropping
// rows because the decoder picked the wrong path.
func TestListBubbles_Success_EnvelopeShape(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/kb", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"bubbles": [
				{
					"kb_id": "kb_01",
					"kb_type": "personal",
					"slug": "personal-abc",
					"name": "Personal",
					"owner_user_id": "user_01",
					"lifecycle_state": "active",
					"viewer_role": "owner",
					"repo_url": "https://example.invalid/personal.git"
				},
				{
					"kb_id": "kb_02",
					"kb_type": "team",
					"slug": "platform",
					"name": "Platform",
					"viewer_role": "member"
				}
			]
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.NoError(t, err)
	require.Len(t, bubbles, 2)

	assert.Equal(t, "kb_01", bubbles[0].KBID)
	assert.Equal(t, KBTypePersonal, bubbles[0].KBType)
	assert.Equal(t, "personal-abc", bubbles[0].Slug)
	assert.Equal(t, "owner", bubbles[0].ViewerRole)
	assert.Equal(t, "https://example.invalid/personal.git", bubbles[0].RepoURL)

	assert.Equal(t, "kb_02", bubbles[1].KBID)
	assert.Equal(t, KBTypeTeam, bubbles[1].KBType)
}

// TestListBubbles_Success_BareArray verifies the client also accepts a bare
// JSON array as the response body.
// Failure prevented: a server simplification (returning [] instead of
// {"bubbles": []}) breaking the CLI silently.
func TestListBubbles_Success_BareArray(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{"kb_id":"kb_x","kb_type":"team","slug":"team-x","name":"Team X"}
		]`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, "kb_x", bubbles[0].KBID)
	assert.Equal(t, KBTypeTeam, bubbles[0].KBType)
}

// TestListBubbles_UnknownKBType_NormalizedToUnknown verifies a future server
// kb_type value gets bucketed into KBTypeUnknown rather than crashing or
// being dropped — required for forward compatibility per the plan.
// Failure prevented: a server-side rollout of a sixth kb_type breaking
// every CLI client until they upgrade.
func TestListBubbles_UnknownKBType_NormalizedToUnknown(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"bubbles":[
			{"kb_id":"kb_y","kb_type":"future-shape","slug":"future","name":"Future"}
		]}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, KBTypeUnknown, bubbles[0].KBType, "unknown kb_type must collapse to KBTypeUnknown")
	assert.Equal(t, "kb_y", bubbles[0].KBID, "row must still surface even with unknown type")
}

// TestGetBubble_Success verifies single-bubble fetch decodes correctly and
// the URL path is correctly templated with the kb_id.
// Failure prevented: a path-templating regression sending the request to
// the list endpoint and silently returning the first bubble.
func TestGetBubble_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/kb/kb_42", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"kb_id":"kb_42",
			"kb_type":"profile",
			"slug":"profile-42",
			"name":"Profile",
			"viewer_role":"owner"
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubble, err := client.GetBubble(context.Background(), "kb_42")

	require.NoError(t, err)
	require.NotNil(t, bubble)
	assert.Equal(t, "kb_42", bubble.KBID)
	assert.Equal(t, KBTypeProfile, bubble.KBType)
}

// TestGetBubble_EmptyID_Rejected ensures the client doesn't issue a request
// when the caller passes an empty id, which would resolve to the list URL
// on the server.
// Failure prevented: GetBubble("") accidentally returning the list response
// or a 404 with confusing context.
func TestGetBubble_EmptyID_Rejected(t *testing.T) {
	t.Parallel()

	client := newTestKBClient("http://unused.invalid")
	bubble, err := client.GetBubble(context.Background(), "")

	require.Error(t, err)
	assert.Nil(t, bubble)
	assert.Contains(t, err.Error(), "kb id is required")
}

// TestListBubbles_404_IsKBAPIUnavailable ensures a 404 is wrapped in the
// non-fatal sentinel so the merger can continue with legacy sources.
// Failure prevented: a missing kb endpoint failing the whole `ox kb list`
// instead of degrading to legacy-only.
func TestListBubbles_404_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable),
		"404 must wrap ErrKBAPIUnavailable so callers can errors.Is it; got %v", err)
}

// TestListBubbles_403_IsKBAPIUnavailable mirrors the 404 case for the
// PostHog-flag-off path.
// Failure prevented: callers without the knowledge-bubbles flag seeing a
// hard error instead of a graceful empty source.
func TestListBubbles_403_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable),
		"403 must wrap ErrKBAPIUnavailable; got %v", err)
}

// TestGetBubble_404_IsKBAPIUnavailable verifies the sentinel fires for the
// detail endpoint too. The merger is the primary caller for List, but
// GetBubble shares the same flag-gating semantics.
// Failure prevented: divergent error handling between list and detail paths.
func TestGetBubble_404_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubble, err := client.GetBubble(context.Background(), "kb_missing")

	require.Error(t, err)
	assert.Nil(t, bubble)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable), "got %v", err)
}

// TestListBubbles_500_IsRegularError verifies that genuine server errors do
// NOT collapse into the non-fatal sentinel — the merger needs to log and
// surface them as warnings, not silently drop them.
// Failure prevented: a real outage being silently swallowed because every
// error looks like "flag off, no rows".
func TestListBubbles_500_IsRegularError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.False(t, errors.Is(err, ErrKBAPIUnavailable),
		"5xx must NOT be the non-fatal sentinel — caller needs to see real failures")
	assert.Contains(t, err.Error(), "500")
}

// TestListBubbles_Unauthorized_MapsToErrUnauthorized verifies 401 is reported
// via the package-shared ErrUnauthorized, matching the rest of internal/api.
// Failure prevented: callers having to special-case kb auth differently
// from every other API.
func TestListBubbles_Unauthorized_MapsToErrUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrUnauthorized), "got %v", err)
}

// TestListBubbles_MalformedJSON_WrappedError verifies a garbage body produces
// a wrapped decode error rather than panicking.
// Failure prevented: a misconfigured proxy returning HTML 200 crashing the
// CLI instead of producing a debuggable error.
func TestListBubbles_MalformedJSON_WrappedError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.Contains(t, strings.ToLower(err.Error()), "decode")
}

// TestListBubbles_ContextCancellation_Respected verifies the request honors
// context cancellation rather than blocking on a slow server.
// Failure prevented: a hung kb endpoint stalling `ox kb list` past any
// reasonable timeout because the client ignored the caller's context.
func TestListBubbles_ContextCancellation_Respected(t *testing.T) {
	t.Parallel()

	// server that never responds within the cancellation window
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)

	client := newTestKBClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	bubbles, err := client.ListBubbles(ctx)

	require.Error(t, err)
	assert.Nil(t, bubbles)
	// the underlying error must be context-derived; check both common forms
	assert.True(t,
		errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context"),
		"expected context cancellation in error, got %v", err)
}
