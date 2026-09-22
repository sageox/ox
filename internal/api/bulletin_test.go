// Bulletin publish tests drive PublishBulletinPost strictly through its
// exported surface, like invite_test.go: parseBulletinError and the details
// decoders are unreachable from here, so every assertion is on what a caller
// observes — the returned receipt, the sentinel or typed error, and the bytes
// that went over the wire.
package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// newBulletinServer starts a server that records every request (via the
// recorder shared with invite_test.go) and defers to respond for the reply.
func newBulletinServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// bulletinStaticServer replies with a fixed status, headers, and body to every
// request. A JSON content type is set unless the caller overrides it.
func bulletinStaticServer(t *testing.T, status int, body string, headers map[string]string) (*httptest.Server, *recorder) {
	t.Helper()
	return newBulletinServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func validBulletinRequest() api.BulletinPublishRequest {
	return api.BulletinPublishRequest{
		Board:   "",
		Slug:    "release-notes",
		Title:   "Release notes for 0.17",
		Format:  api.BulletinFormatMarkdown,
		Content: "# Release notes for 0.17\n\nShipped.\n",
		TTL:     "14d",
	}
}

const bulletinCreatedBody = `{
	"team_id": "team_abc",
	"board": "general",
	"path": "bulletin/general/posts/release-notes-7c4a8d09a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b.md",
	"slug": "release-notes",
	"title": "Release notes for 0.17",
	"format": "markdown",
	"content_sha256": "7c4a8d09a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b",
	"content_bytes": 1842,
	"created_at": "2026-09-21T22:41:07Z",
	"expires_at": "2026-10-05T22:41:07Z",
	"commit_id": "9f1c2a7d3e5b4c6a8f0e1d2c3b4a5968778695a4"
}`

// bulletinSentinels is every sentinel PublishBulletinPost can return. Used to
// prove a response maps to exactly one of them — the three 404s are only
// meaningfully told apart if the losing sentinels are also excluded.
var bulletinSentinels = map[string]error{
	"ErrBulletinNotEnabled":  api.ErrBulletinNotEnabled,
	"ErrBulletinNotAMember":  api.ErrBulletinNotAMember,
	"ErrBulletinUnsupported": api.ErrBulletinUnsupported,
	"ErrBulletinTooLarge":    api.ErrBulletinTooLarge,
	"ErrBulletinValidation":  api.ErrBulletinValidation,
	"ErrBulletinDuplicate":   api.ErrBulletinDuplicate,
	"ErrBulletinUnavailable": api.ErrBulletinUnavailable,
	"ErrUnauthorized":        api.ErrUnauthorized,
	"ErrVersionUnsupported":  api.ErrVersionUnsupported,
	"ErrInvalidTeamRef":      api.ErrInvalidTeamRef,
}

// requireOnlyBulletinSentinel asserts err matches want and no other sentinel.
func requireOnlyBulletinSentinel(t *testing.T, err error, want error) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, want, "wrong sentinel; got %v", err)
	for name, other := range bulletinSentinels {
		if errors.Is(other, want) {
			continue
		}
		assert.NotErrorIs(t, err, other, "must not also match %s", name)
	}
}

// requireNoBulletinSentinel asserts err is a real error that impersonates none
// of the sentinels — the contract for unmapped statuses like 415 and 500.
func requireNoBulletinSentinel(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for name, s := range bulletinSentinels {
		assert.NotErrorIs(t, err, s, "unmapped status must not be reported as %s", name)
	}
}

// ---------------------------------------------------------------------------
// A. Request shape: method, path, headers, body
// ---------------------------------------------------------------------------

// TestPublishBulletinPost_RequestShape pins what goes over the wire.
//
// Failure prevented: the server rejects any key outside the six it knows and
// hashes the content bytes exactly as received. An omitempty that drops a
// blank board, a seventh key, or an HTML-escaped content value (`\u003c` for
// `<`) would each turn a valid post into a 400 — or, for a tag-dense HTML
// post, push the request over the 2 MiB cap — and the user would be told
// their file is wrong when it is not. A rewritten User-Agent would misreport
// the publishing client on every post.
func TestPublishBulletinPost_RequestShape(t *testing.T) {
	t.Parallel()

	srv, rec := bulletinStaticServer(t, http.StatusCreated, bulletinCreatedBody, nil)

	req := validBulletinRequest()
	req.Format = api.BulletinFormatHTML
	req.Content = "<b>&</b> is not escaped"

	_, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", req)
	require.NoError(t, err)

	got := rec.last(t)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/v1/teams/team_abc/bulletin/posts", got.Path)
	assert.Empty(t, got.RawQuery)
	assert.Equal(t, "application/json", got.Headers.Get("Content-Type"))
	assert.Equal(t, "Bearer tok", got.Headers.Get("Authorization"))
	assert.True(t, strings.HasPrefix(got.Headers.Get("User-Agent"), "ox/"),
		"User-Agent must be the default ox client string, got %q", got.Headers.Get("User-Agent"))

	// exactly the six keys, no more, no fewer — blank board included
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.Body, &keys), "body must be a JSON object")
	assert.ElementsMatch(t,
		[]string{"board", "slug", "title", "format", "content", "ttl"},
		mapKeys(keys))

	var decoded api.BulletinPublishRequest
	require.NoError(t, json.Unmarshal(got.Body, &decoded))
	assert.Equal(t, req, decoded, "the request must round-trip unchanged")

	// the raw bytes carry a literal '<' and '&', not their \u escapes
	raw := string(got.Body)
	assert.Contains(t, raw, `<b>&</b> is not escaped`)
	assert.NotContains(t, raw, `\u003c`)
	assert.NotContains(t, raw, `\u0026`)
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPublishBulletinPost_TeamRefEscaping covers the team reference's trip
// into the URL path.
//
// Failure prevented: a ref that is not escaped as one segment lands on a
// different route and 404s with a plain-text body — which the mapper reports
// as "this server does not support bulletin posts", a capability gap
// diagnosed for what was a bad team name. Dot segments survive escaping
// untouched and must be refused before any request is made.
func TestPublishBulletinPost_TeamRefEscaping(t *testing.T) {
	t.Parallel()

	t.Run("space is escaped into one segment", func(t *testing.T) {
		t.Parallel()
		srv, rec := bulletinStaticServer(t, http.StatusCreated, bulletinCreatedBody, nil)

		_, err := api.NewRepoClientWithEndpoint(srv.URL).
			WithAuthToken("tok").
			PublishBulletinPost(context.Background(), "a b", validBulletinRequest())
		require.NoError(t, err)
		assert.Equal(t, "/api/v1/teams/a%20b/bulletin/posts", rec.last(t).EscapedPath)
	})

	t.Run("slug ref reaches the same route as an id", func(t *testing.T) {
		t.Parallel()
		srv, rec := bulletinStaticServer(t, http.StatusCreated, bulletinCreatedBody, nil)

		_, err := api.NewRepoClientWithEndpoint(srv.URL).
			WithAuthToken("tok").
			PublishBulletinPost(context.Background(), "acme-platform", validBulletinRequest())
		require.NoError(t, err)
		assert.Equal(t, "/api/v1/teams/acme-platform/bulletin/posts", rec.last(t).EscapedPath)
	})

	for _, ref := range []string{".", "..", "  ..  ", ""} {
		t.Run(fmt.Sprintf("%q is refused before any request", ref), func(t *testing.T) {
			t.Parallel()
			srv, rec := bulletinStaticServer(t, http.StatusCreated, bulletinCreatedBody, nil)

			result, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				PublishBulletinPost(context.Background(), ref, validBulletinRequest())

			require.ErrorIs(t, err, api.ErrInvalidTeamRef)
			assert.Nil(t, result)
			assert.Empty(t, rec.all(), "a ref that cannot be placed safely must never be sent")
		})
	}
}

// ---------------------------------------------------------------------------
// B. Success path
// ---------------------------------------------------------------------------

// TestPublishBulletinPost_Success_DecodesReceipt covers the 201 receipt.
//
// Failure prevented: a field-name mismatch between the server's 201 body and
// BulletinPublishResult silently yields a receipt with a blank path or hash —
// the command then either prints a receipt that points nowhere or fails its
// hash comparison against an empty string and reports corruption that never
// happened.
func TestPublishBulletinPost_Success_DecodesReceipt(t *testing.T) {
	t.Parallel()

	srv, _ := bulletinStaticServer(t, http.StatusCreated, bulletinCreatedBody, nil)

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "team_abc", result.TeamID)
	assert.Equal(t, "general", result.Board)
	assert.Equal(t, "bulletin/general/posts/release-notes-7c4a8d09a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b.md", result.Path)
	assert.Equal(t, "release-notes", result.Slug)
	assert.Equal(t, "Release notes for 0.17", result.Title)
	assert.Equal(t, "markdown", result.Format)
	assert.Equal(t, "7c4a8d09a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b", result.ContentSHA256)
	assert.Equal(t, 1842, result.ContentBytes)
	assert.Equal(t, time.Date(2026, 9, 21, 22, 41, 7, 0, time.UTC), result.CreatedAt)
	assert.Equal(t, time.Date(2026, 10, 5, 22, 41, 7, 0, time.UTC), result.ExpiresAt)
	assert.Equal(t, "9f1c2a7d3e5b4c6a8f0e1d2c3b4a5968778695a4", result.CommitID)
}

// TestPublishBulletinPost_Non201SuccessIsAnError covers a 2xx that is not the
// publish handler's 201.
//
// Failure prevented: a 200 from some other handler (a proxy's landing page
// rendered as JSON, a misrouted request) decoded as a receipt would forge a
// "published" outcome for a post nothing ever stored.
func TestPublishBulletinPost_Non201SuccessIsAnError(t *testing.T) {
	t.Parallel()

	srv, _ := bulletinStaticServer(t, http.StatusOK, bulletinCreatedBody, nil)

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	assert.Nil(t, result)
	requireNoBulletinSentinel(t, err)
	assert.Contains(t, err.Error(), "200")
}

// TestPublishBulletinPost_UndecodableReceiptIsAnError covers a 201 whose body
// is not a receipt.
//
// Failure prevented: an empty or non-JSON 201 body quietly producing a zero
// receipt, which the command would then render as a successful publish with
// no path and no hash.
func TestPublishBulletinPost_UndecodableReceiptIsAnError(t *testing.T) {
	t.Parallel()

	// `{}` and `null` decode cleanly into a zero receipt; without the
	// load-bearing-field check they would be returned as a success the
	// caller then misreports as a hash mismatch.
	for _, body := range []string{``, `not json`, `[1,2,3]`, `{}`, `null`, `{"team_id":"team_abc"}`} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			t.Parallel()
			srv, _ := bulletinStaticServer(t, http.StatusCreated, body, nil)

			result, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
			assert.Nil(t, result)
			requireNoBulletinSentinel(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// C. Status mapping — every outcome the endpoint can produce
// ---------------------------------------------------------------------------

// TestPublishBulletinPost_StatusMapping is the mapping table for every non-2xx
// the endpoint documents.
//
// Failure prevented: the same 404 status means three different things here
// (outside the pilot / not a member / no such route) and they need three
// different next steps — "do not retry", "pick another team", "do not retry
// against this server". Collapsing them, or reporting a 503 without its
// Retry-After hint, would send the command down the wrong recovery path.
func TestPublishBulletinPost_StatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		check   func(t *testing.T, err error)
	}{
		{
			name:   "400 with field details",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"validation_error","message":"invalid post","details":{"ttl":"ttl \"120d\" is longer than the maximum 90d","slug":"slug must match ^[a-z0-9][a-z0-9-]{0,79}$"}}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinValidation)
				var ve *api.BulletinValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, "invalid post", ve.Message)
				assert.Equal(t, `ttl "120d" is longer than the maximum 90d`, ve.Fields["ttl"])
				assert.Equal(t, `slug must match ^[a-z0-9][a-z0-9-]{0,79}$`, ve.Fields["slug"])
				assert.Len(t, ve.Fields, 2)
			},
		},
		{
			name:   "400 without details (malformed request body)",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"validation_error","message":"unknown field \"extra\""}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinValidation)
				var ve *api.BulletinValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, `unknown field "extra"`, ve.Message)
				assert.Nil(t, ve.Fields, "absent details must decode to a nil map, not an empty one")
				assert.Equal(t, `unknown field "extra"`, ve.Error())
			},
		},
		{
			name:   "400 with non-map details is tolerated",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"validation_error","message":"bad","details":["ttl","slug"]}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinValidation)
				var ve *api.BulletinValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, "bad", ve.Message)
				assert.Nil(t, ve.Fields)
			},
		},
		{
			name:   "401 not authenticated",
			status: http.StatusUnauthorized,
			body:   `{"error":{"code":"unauthorized","message":"authentication required"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrUnauthorized)
			},
		},
		{
			name:   "401 team service token",
			status: http.StatusUnauthorized,
			body:   `{"error":{"code":"unauthorized","message":"publishing requires a signed-in person"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrUnauthorized)
			},
		},
		{
			name:   "404 empty body: outside the pilot",
			status: http.StatusNotFound,
			body:   ``,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinNotEnabled)
			},
		},
		{
			name:   "404 whitespace-only body: outside the pilot",
			status: http.StatusNotFound,
			body:   "  \n",
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinNotEnabled)
			},
		},
		{
			// Captured verbatim from the live server on 2026-09-21 for a team
			// the caller cannot reach: the membership layer's nested envelope.
			name:    "404 nested envelope: not a member",
			status:  http.StatusNotFound,
			body:    "{\"error\":{\"code\":\"NOT_FOUND\",\"message\":\"team not found\"}}\n",
			headers: map[string]string{"Content-Type": "application/json"},
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinNotAMember)
			},
		},
		{
			// Captured verbatim from the live server on 2026-09-21 for an
			// unrouted path: the router's FLAT envelope, served as JSON. A
			// mapper that read "any JSON 404" as a membership answer would
			// tell someone on a server without the route to pick another
			// team.
			name:    "404 flat envelope from the router: no bulletin route on this server",
			status:  http.StatusNotFound,
			body:    "{\"error\":\"route not registered\"}\n",
			headers: map[string]string{"Content-Type": "application/json"},
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnsupported)
			},
		},
		{
			// The handler-style flat envelope never carries a 404 on this
			// route (the handler's own outcomes are 400/401/409/413/415/500/503),
			// so a flat 404 of any wording is the router's, not the membership
			// layer's.
			name:   "404 flat envelope with success:false: still not a membership answer",
			status: http.StatusNotFound,
			body:   `{"success":false,"error":"not found"}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnsupported)
			},
		},
		{
			name:    "404 plain text: no bulletin route on this server",
			status:  http.StatusNotFound,
			body:    "404 page not found\n",
			headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnsupported)
			},
		},
		{
			name:   "409 duplicate with full details",
			status: http.StatusConflict,
			body:   `{"error":{"code":"duplicate_post","message":"identical content already posted","details":{"path":"bulletin/general/posts/release-notes-7c4a.md","state":"active","created_at":"2026-09-20T10:00:00Z","expires_at":"2026-10-04T10:00:00Z"}}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinDuplicate)
				var de *api.BulletinDuplicateError
				require.ErrorAs(t, err, &de)
				assert.Equal(t, "identical content already posted", de.Message)
				assert.Equal(t, "bulletin/general/posts/release-notes-7c4a.md", de.Duplicate.Path)
				assert.Equal(t, "active", de.Duplicate.State)
				assert.Equal(t, "2026-09-20T10:00:00Z", de.Duplicate.CreatedAt)
				assert.Equal(t, "2026-10-04T10:00:00Z", de.Duplicate.ExpiresAt)
				assert.Contains(t, de.Error(), "bulletin/general/posts/release-notes-7c4a.md")
			},
		},
		{
			name:   "409 duplicate with timestamps omitted",
			status: http.StatusConflict,
			body:   `{"error":{"code":"duplicate_post","message":"identical content already posted","details":{"path":"bulletin/general/posts/release-notes-7c4a.md","state":"archived"}}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinDuplicate)
				var de *api.BulletinDuplicateError
				require.ErrorAs(t, err, &de)
				assert.Equal(t, "bulletin/general/posts/release-notes-7c4a.md", de.Duplicate.Path)
				assert.Equal(t, "archived", de.Duplicate.State)
				assert.Empty(t, de.Duplicate.CreatedAt, "an omitted timestamp must stay empty")
				assert.Empty(t, de.Duplicate.ExpiresAt, "an omitted timestamp must stay empty")
			},
		},
		{
			name:   "409 duplicate without details",
			status: http.StatusConflict,
			body:   `{"error":{"code":"duplicate_post","message":"identical content already posted"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinDuplicate)
				var de *api.BulletinDuplicateError
				require.ErrorAs(t, err, &de)
				assert.Equal(t, api.BulletinDuplicate{}, de.Duplicate)
			},
		},
		{
			name:   "413 content too large",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"error":{"code":"content_too_large","message":"content exceeds 1 MiB"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinTooLarge)
				assert.Contains(t, err.Error(), "content exceeds 1 MiB", "the server's wording must survive")
			},
		},
		{
			name:   "415 unsupported media type is generic",
			status: http.StatusUnsupportedMediaType,
			body:   `{"error":{"code":"unsupported_media_type","message":"Content-Type must be application/json"}}`,
			check: func(t *testing.T, err error) {
				requireNoBulletinSentinel(t, err)
				assert.Contains(t, err.Error(), "415")
				assert.Contains(t, err.Error(), "Content-Type must be application/json")
			},
		},
		{
			name:   "500 internal error is generic",
			status: http.StatusInternalServerError,
			body:   `{"error":{"code":"internal_error","message":"something broke"}}`,
			check: func(t *testing.T, err error) {
				requireNoBulletinSentinel(t, err)
				assert.Contains(t, err.Error(), "500")
				assert.Contains(t, err.Error(), "something broke")
			},
		},
		{
			name:   "500 with a non-JSON body still names the status",
			status: http.StatusInternalServerError,
			body:   "upstream exploded",
			check: func(t *testing.T, err error) {
				requireNoBulletinSentinel(t, err)
				assert.Contains(t, err.Error(), "500")
				assert.Contains(t, err.Error(), "upstream exploded")
			},
		},
		{
			name:    "503 with Retry-After",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`,
			headers: map[string]string{"Retry-After": "5"},
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnavailable)
				var ue *api.BulletinUnavailableError
				require.ErrorAs(t, err, &ue)
				assert.Equal(t, 5*time.Second, ue.RetryAfter)
				assert.Equal(t, "team context store unavailable", ue.Message)
			},
		},
		{
			name:   "503 without Retry-After",
			status: http.StatusServiceUnavailable,
			body:   `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnavailable)
				var ue *api.BulletinUnavailableError
				require.ErrorAs(t, err, &ue)
				assert.Equal(t, time.Duration(0), ue.RetryAfter)
			},
		},
		{
			name:    "503 with an unparseable Retry-After",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`,
			headers: map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"},
			check: func(t *testing.T, err error) {
				requireOnlyBulletinSentinel(t, err, api.ErrBulletinUnavailable)
				var ue *api.BulletinUnavailableError
				require.ErrorAs(t, err, &ue)
				assert.Equal(t, time.Duration(0), ue.RetryAfter, "a non-integer Retry-After must fall back to 0")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := bulletinStaticServer(t, tt.status, tt.body, tt.headers)

			result, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())

			assert.Nil(t, result, "a non-2xx must never yield a receipt")
			tt.check(t, err)
		})
	}
}

// TestPublishBulletinPost_ValidationErrorRendersFieldsSorted pins the
// human-readable form of a 400.
//
// Failure prevented: Go map iteration is randomized, so an unsorted rendering
// would shuffle the fields between two runs of the same command — confusing
// to read and impossible to assert on in the command's own tests.
func TestPublishBulletinPost_ValidationErrorRendersFieldsSorted(t *testing.T) {
	t.Parallel()

	srv, _ := bulletinStaticServer(t, http.StatusBadRequest,
		`{"error":{"code":"validation_error","message":"invalid post","details":{"ttl":"too long","board":"unknown board","slug":"bad shape"}}}`, nil)

	_, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	require.Error(t, err)
	assert.Equal(t, "invalid post (board: unknown board; slug: bad shape; ttl: too long)", err.Error())
}

// TestPublishBulletinPost_VersionGateBeatsBodyParsing covers the 426 hard block.
//
// Failure prevented: if the body were parsed before the version gate, a 426
// carrying an error envelope would surface as a generic "HTTP 426" and the
// command would tell the user to fix their post instead of upgrading ox.
func TestPublishBulletinPost_VersionGateBeatsBodyParsing(t *testing.T) {
	t.Parallel()

	srv, _ := newBulletinServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(api.HeaderMinVersion, "9.9.9")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = io.WriteString(w, `{"success":false,"error":"upgrade required"}`)
	})

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())

	assert.Nil(t, result)
	requireOnlyBulletinSentinel(t, err, api.ErrVersionUnsupported)
}

// TestPublishBulletinPost_HostileErrorBodiesNeverPanicNeverSucceed sweeps the
// shapes of junk that can arrive where an error envelope was expected, across
// the statuses whose details are decoded.
//
// Failure prevented: the details decoders for 400 and 409 must survive any
// shape (number, array, null, nested junk) rather than panic — and must never
// turn a non-2xx into a nil error, which the command would render as a
// published post.
func TestPublishBulletinPost_HostileErrorBodiesNeverPanicNeverSucceed(t *testing.T) {
	t.Parallel()

	bodies := []string{
		`null`, `{}`, `[]`, `"boom"`, `42`,
		`{"error":123}`, `{"error":null}`, `{"error":{}}`, `{"error":["a"]}`,
		`{"error":{"code":"x","message":"m","details":null}}`,
		`{"error":{"code":"x","message":"m","details":42}}`,
		`{"error":{"code":"x","message":"m","details":"string"}}`,
		`{"error":{"code":"x","message":"m","details":{"ttl":["a","b"]}}}`,
		`{"error":{"code":"x","message":"m","details":{"path":123,"state":null}}}`,
		`{"error":{"code":"x"`,
	}
	statuses := []int{
		http.StatusBadRequest, http.StatusConflict, http.StatusServiceUnavailable,
		http.StatusRequestEntityTooLarge, http.StatusInternalServerError,
	}

	for _, status := range statuses {
		for i, body := range bodies {
			t.Run(fmt.Sprintf("%d/%d", status, i), func(t *testing.T) {
				t.Parallel()
				srv, _ := bulletinStaticServer(t, status, body, nil)

				result, err := api.NewRepoClientWithEndpoint(srv.URL).
					WithAuthToken("tok").
					PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
				assert.Nil(t, result)
				require.Error(t, err, "status %d with body %q must not succeed", status, body)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// D. Transport safety: redirects and oversized responses
// ---------------------------------------------------------------------------

// TestPublishBulletinPost_RedirectIsAnError covers an HTTP 302 on the publish
// route.
//
// Failure prevented: Go's default policy rewrites a redirected POST into a GET
// and drops the body. A canonicalizing redirect in front of the API would then
// have the client GET some other resource and decode whatever it returns as
// the receipt — a "published" outcome for a post no handler ever saw. The
// redirect target must never be hit and the 3xx must surface as an error.
func TestPublishBulletinPost_RedirectIsAnError(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/teams/team_abc/bulletin/posts", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		http.Redirect(w, r, "/api/v1/teams/team_abc", http.StatusFound)
	})
	var targetHit atomic.Bool
	mux.HandleFunc("/api/v1/teams/team_abc", func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, bulletinCreatedBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())

	assert.Nil(t, result)
	requireNoBulletinSentinel(t, err)
	assert.Contains(t, err.Error(), "302")
	assert.False(t, targetHit.Load(), "the redirect target must never be requested")
	require.Len(t, rec.all(), 1, "exactly one request — the original POST — may be made")
	assert.Equal(t, http.MethodPost, rec.all()[0].Method)
}

// TestPublishBulletinPost_OversizedResponseIsBounded covers a 201 whose body
// never ends.
//
// Failure prevented: a hostile or broken server streaming an unbounded body
// makes the CLI buffer it all (the HTTP timeout bounds time, not memory). The
// read must stop at the cap and report an error rather than hang or grow.
func TestPublishBulletinPost_OversizedResponseIsBounded(t *testing.T) {
	t.Parallel()

	const junkSize = 2 << 20 // 2 MiB, double the 1 MiB cap
	srv, _ := newBulletinServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		chunk := strings.Repeat("x", 64<<10)
		for written := 0; written < junkSize; written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return // the client stopped reading, which is the point
			}
		}
	})

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())

	assert.Nil(t, result)
	requireNoBulletinSentinel(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

// TestPublishBulletinPost_NoTokenSendsNoAuthorization covers the unauthenticated
// client.
//
// Failure prevented: a blank token rendered as "Bearer " would be a malformed
// credential the server could reject with a confusing message instead of the
// clean 401 that maps to "run ox login".
func TestPublishBulletinPost_NoTokenSendsNoAuthorization(t *testing.T) {
	t.Parallel()

	srv, rec := bulletinStaticServer(t, http.StatusUnauthorized,
		`{"error":{"code":"unauthorized","message":"authentication required"}}`, nil)

	_, err := api.NewRepoClientWithEndpoint(srv.URL).
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	requireOnlyBulletinSentinel(t, err, api.ErrUnauthorized)
	assert.Empty(t, rec.last(t).Headers.Get("Authorization"))
}
