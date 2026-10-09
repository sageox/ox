// GitHub mirror relay tests drive RelayGitHubMirrorItems strictly through its
// exported surface, like bulletin_test.go: parseGitHubMirrorError and the
// details decoders are unreachable from here, so every assertion is on what a
// caller observes — the decoded response, the sentinel or typed error, and the
// bytes that went over the wire.
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
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// gitHubMirrorServer starts a server that records every request (via the
// recorder shared with invite_test.go) and defers to respond for the reply.
func gitHubMirrorServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// gitHubMirrorStaticServer replies with a fixed status, headers, and body to
// every request. A JSON content type is set unless the caller overrides it.
func gitHubMirrorStaticServer(t *testing.T, status int, body string, headers map[string]string) (*httptest.Server, *recorder) {
	t.Helper()
	return gitHubMirrorServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// validGitHubMirrorRequest is a one-item batch whose text carries the three
// characters encoding/json escapes by default (<, >, &).
func validGitHubMirrorRequest() githubmirror.RelayRequest {
	created := time.Date(2026, 9, 28, 17, 2, 11, 0, time.UTC)
	return githubmirror.RelayRequest{
		Repo: githubmirror.Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 4242, Private: true},
		Items: []githubmirror.Item{{
			Kind:                 githubmirror.KindPullRequest,
			Number:               1287,
			State:                githubmirror.StateOpen,
			Title:                "Mirror <GitHub> & friends",
			Body:                 "<b>&</b> is not escaped",
			Author:               githubmirror.Author{Login: "devon-dev", ID: 5550101, Association: "MEMBER", Type: "User"},
			Labels:               []string{"daemon"},
			URL:                  "https://github.com/acme/api/pull/1287",
			CreatedAt:            created,
			UpdatedAt:            created,
			LastMaterialChangeAt: created,
			Reviews:              []githubmirror.Review{},
			Comments:             []githubmirror.Comment{},
			Files:                []string{"internal/daemon/github_sync.go"},
			ChangeHash:           "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}
}

const gitHubMirrorOKBody = `{
	"repo_status": "enabled",
	"results": [
		{"source_key": "github.com/acme/api/pull/1287", "status": "accepted"},
		{"source_key": "github.com/acme/api/issues/12", "status": "current"},
		{"source_key": "github.com/acme/api/issues/13", "status": "rejected", "reason": "body is not valid UTF-8"}
	]
}`

// gitHubMirrorSentinels is every sentinel RelayGitHubMirrorItems can return.
// Used to prove a response maps to exactly one of them — the three 404s are
// only meaningfully told apart if the losing sentinels are also excluded.
var gitHubMirrorSentinels = map[string]error{
	"ErrGitHubMirrorNotEnabled":  api.ErrGitHubMirrorNotEnabled,
	"ErrGitHubMirrorNotAMember":  api.ErrGitHubMirrorNotAMember,
	"ErrGitHubMirrorUnsupported": api.ErrGitHubMirrorUnsupported,
	"ErrGitHubMirrorTooLarge":    api.ErrGitHubMirrorTooLarge,
	"ErrGitHubMirrorValidation":  api.ErrGitHubMirrorValidation,
	"ErrGitHubMirrorBusy":        api.ErrGitHubMirrorBusy,
	"ErrUnauthorized":            api.ErrUnauthorized,
	"ErrVersionUnsupported":      api.ErrVersionUnsupported,
	"ErrInvalidTeamRef":          api.ErrInvalidTeamRef,
}

// requireOnlyGitHubMirrorSentinel asserts err matches want and no other
// sentinel.
func requireOnlyGitHubMirrorSentinel(t *testing.T, err error, want error) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, want, "wrong sentinel; got %v", err)
	for name, other := range gitHubMirrorSentinels {
		if errors.Is(other, want) {
			continue
		}
		assert.NotErrorIs(t, err, other, "must not also match %s", name)
	}
}

// requireNoGitHubMirrorSentinel asserts err is a real error that impersonates
// none of the sentinels — the contract for unmapped statuses like 500 and for
// every malformed success.
func requireNoGitHubMirrorSentinel(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for name, s := range gitHubMirrorSentinels {
		assert.NotErrorIs(t, err, s, "unmapped outcome must not be reported as %s", name)
	}
}

func relayGitHubMirror(t *testing.T, srvURL string) (*githubmirror.RelayResponse, error) {
	t.Helper()
	return api.NewRepoClientWithEndpoint(srvURL).
		WithAuthToken("tok").
		RelayGitHubMirrorItems(context.Background(), "team_abc", validGitHubMirrorRequest())
}

// ---------------------------------------------------------------------------
// A. Request shape: method, path, headers, body
// ---------------------------------------------------------------------------

// TestRelayGitHubMirrorItems_RequestShape pins what goes over the wire.
//
// Failure prevented: PR descriptions are tag- and ampersand-dense, and an
// HTML-escaped body (`\u003c` for `<`) expands them six-fold, pushing a full
// 50-item batch over the server's body cap even when the text fits — the
// daemon would then halve batches forever for a problem that is encoding. A
// rewritten User-Agent would misreport the relaying client on every request,
// and a missing bearer token turns every relay into a 401.
func TestRelayGitHubMirrorItems_RequestShape(t *testing.T) {
	t.Parallel()

	srv, rec := gitHubMirrorStaticServer(t, http.StatusOK, gitHubMirrorOKBody, nil)

	req := validGitHubMirrorRequest()
	_, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		RelayGitHubMirrorItems(context.Background(), "team_abc", req)
	require.NoError(t, err)

	got := rec.last(t)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/v1/teams/team_abc/github-mirror/items", got.Path)
	assert.Empty(t, got.RawQuery)
	assert.Equal(t, "application/json", got.Headers.Get("Content-Type"))
	assert.Equal(t, "Bearer tok", got.Headers.Get("Authorization"))
	assert.True(t, strings.HasPrefix(got.Headers.Get("User-Agent"), "ox/"),
		"User-Agent must be the default ox client string, got %q", got.Headers.Get("User-Agent"))

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.Body, &keys), "body must be a JSON object")
	assert.ElementsMatch(t, []string{"repo", "items"}, mapKeys(keys))

	var decoded githubmirror.RelayRequest
	require.NoError(t, json.Unmarshal(got.Body, &decoded))
	assert.Equal(t, req, decoded, "the request must round-trip unchanged")

	// the raw bytes carry literal '<', '>' and '&', not their \u escapes
	raw := string(got.Body)
	assert.Contains(t, raw, `<b>&</b> is not escaped`)
	assert.Contains(t, raw, `Mirror <GitHub> & friends`)
	assert.NotContains(t, raw, `\u003c`)
	assert.NotContains(t, raw, `\u003e`)
	assert.NotContains(t, raw, `\u0026`)
}

// TestRelayGitHubMirrorItems_NoTokenSendsNoAuthorization covers the
// unauthenticated client.
//
// Failure prevented: a blank token rendered as "Bearer " is a malformed
// credential the server could reject with a confusing message instead of the
// clean 401 that maps to "run ox login".
func TestRelayGitHubMirrorItems_NoTokenSendsNoAuthorization(t *testing.T) {
	t.Parallel()

	srv, rec := gitHubMirrorStaticServer(t, http.StatusUnauthorized,
		`{"error":{"code":"unauthorized","message":"authentication required"}}`, nil)

	_, err := api.NewRepoClientWithEndpoint(srv.URL).
		RelayGitHubMirrorItems(context.Background(), "team_abc", validGitHubMirrorRequest())
	requireOnlyGitHubMirrorSentinel(t, err, api.ErrUnauthorized)
	assert.Empty(t, rec.last(t).Headers.Get("Authorization"))
}

// TestRelayGitHubMirrorItems_TeamRefEscaping covers the team reference's trip
// into the URL path.
//
// Failure prevented: a ref that is not escaped as one segment lands on a
// different route and 404s with a plain-text body — which the mapper reports
// as "this server does not support the GitHub mirror", a capability gap
// diagnosed for what was a bad team name. Dot segments survive escaping
// untouched and must be refused before any request is made.
func TestRelayGitHubMirrorItems_TeamRefEscaping(t *testing.T) {
	t.Parallel()

	escaped := []struct {
		name, ref, wantEscaped string
	}{
		{"space is escaped into one segment", "a b", "/api/v1/teams/a%20b/github-mirror/items"},
		{"slash and question mark stay inside the segment", "a/b?c", "/api/v1/teams/a%2Fb%3Fc/github-mirror/items"},
		{"hash stays inside the segment", "a#b", "/api/v1/teams/a%23b/github-mirror/items"},
		{"slug ref reaches the same route as an id", "acme-platform", "/api/v1/teams/acme-platform/github-mirror/items"},
	}
	for _, tt := range escaped {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, rec := gitHubMirrorStaticServer(t, http.StatusOK, gitHubMirrorOKBody, nil)

			_, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				RelayGitHubMirrorItems(context.Background(), tt.ref, validGitHubMirrorRequest())
			require.NoError(t, err)

			got := rec.last(t)
			assert.Equal(t, tt.wantEscaped, got.EscapedPath)
			assert.Empty(t, got.RawQuery, "a '?' in the ref must not become a query string")
		})
	}

	for _, ref := range []string{".", "..", "  ..  ", ""} {
		t.Run(fmt.Sprintf("%q is refused before any request", ref), func(t *testing.T) {
			t.Parallel()
			srv, rec := gitHubMirrorStaticServer(t, http.StatusOK, gitHubMirrorOKBody, nil)

			result, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				RelayGitHubMirrorItems(context.Background(), ref, validGitHubMirrorRequest())

			require.ErrorIs(t, err, api.ErrInvalidTeamRef)
			assert.Nil(t, result)
			assert.Empty(t, rec.all(), "a ref that cannot be placed safely must never be sent")
		})
	}
}

// ---------------------------------------------------------------------------
// B. Success path
// ---------------------------------------------------------------------------

// TestRelayGitHubMirrorItems_Success_DecodesResults covers the 200 response.
//
// Failure prevented: a field-name mismatch between the server's body and
// RelayResponse silently yields blank statuses — the daemon would then record
// every item as neither accepted nor rejected, and re-relay the whole backlog
// on every cycle.
func TestRelayGitHubMirrorItems_Success_DecodesResults(t *testing.T) {
	t.Parallel()

	srv, _ := gitHubMirrorStaticServer(t, http.StatusOK, gitHubMirrorOKBody, nil)

	got, err := relayGitHubMirror(t, srv.URL)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, githubmirror.RepoEnabled, got.RepoStatus)
	assert.Equal(t, []githubmirror.ItemResult{
		{SourceKey: "github.com/acme/api/pull/1287", Status: githubmirror.ResultAccepted},
		{SourceKey: "github.com/acme/api/issues/12", Status: githubmirror.ResultCurrent},
		{SourceKey: "github.com/acme/api/issues/13", Status: githubmirror.ResultRejected, Reason: "body is not valid UTF-8"},
	}, got.Results)
}

// TestRelayGitHubMirrorItems_RefusedRepoIsStillASuccessfulResponse covers a
// 200 whose repo_status is not "enabled".
//
// Failure prevented: a repo refusal (private repo not opted in, repo not
// linked) is the server's answer, not a transport failure. Turning it into an
// error would make the daemon retry on the error backoff instead of the 24h
// repo backoff the spec assigns to it.
func TestRelayGitHubMirrorItems_RefusedRepoIsStillASuccessfulResponse(t *testing.T) {
	t.Parallel()

	for _, status := range []string{githubmirror.RepoNotOptedIn, githubmirror.RepoNotLinked, githubmirror.RepoNotEligible} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			srv, _ := gitHubMirrorStaticServer(t, http.StatusOK, `{"repo_status":"`+status+`","results":[]}`, nil)

			got, err := relayGitHubMirror(t, srv.URL)
			require.NoError(t, err)
			assert.Equal(t, status, got.RepoStatus)
			assert.Empty(t, got.Results)
		})
	}
}

// TestRelayGitHubMirrorItems_Non200SuccessIsAnError covers a 2xx that is not
// the relay handler's 200.
//
// Failure prevented: a 201 or 202 from some other handler (a proxy's landing
// page rendered as JSON, a misrouted request) decoded as a response would
// forge a "relayed" outcome for items nothing ever stored — worse than a
// failure, because the daemon would remember them as done.
func TestRelayGitHubMirrorItems_Non200SuccessIsAnError(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusCreated, http.StatusAccepted, http.StatusNonAuthoritativeInfo, http.StatusPartialContent} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			srv, _ := gitHubMirrorStaticServer(t, status, gitHubMirrorOKBody, nil)

			result, err := relayGitHubMirror(t, srv.URL)
			assert.Nil(t, result)
			requireNoGitHubMirrorSentinel(t, err)
			assert.Contains(t, err.Error(), fmt.Sprint(status))
		})
	}

	t.Run("204 no content", func(t *testing.T) {
		t.Parallel()
		srv, _ := gitHubMirrorStaticServer(t, http.StatusNoContent, ``, nil)

		result, err := relayGitHubMirror(t, srv.URL)
		assert.Nil(t, result)
		requireNoGitHubMirrorSentinel(t, err)
		assert.Contains(t, err.Error(), "204")
	})
}

// TestRelayGitHubMirrorItems_UndecodableResponseIsAnError covers a 200 whose
// body is not a relay response.
//
// Failure prevented: `{}` and `null` both unmarshal cleanly into a zero
// RelayResponse. Without the repo_status check the daemon would read an empty
// status as neither "enabled" nor "refused" and either relay forever or back
// the repo off for the wrong reason.
func TestRelayGitHubMirrorItems_UndecodableResponseIsAnError(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		``, `not json`, `[1,2,3]`, `{}`, `null`,
		`{"results":[{"source_key":"k","status":"accepted"}]}`,
		`{"repo_status":""}`,
		`{"repo_status":null,"results":[]}`,
		`{"repo_status":123}`,
	} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			t.Parallel()
			srv, _ := gitHubMirrorStaticServer(t, http.StatusOK, body, nil)

			result, err := relayGitHubMirror(t, srv.URL)
			assert.Nil(t, result)
			requireNoGitHubMirrorSentinel(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// C. Status mapping — every outcome the endpoint can produce
// ---------------------------------------------------------------------------

// TestRelayGitHubMirrorItems_StatusMapping is the mapping table for every
// non-2xx the spec documents.
//
// Failure prevented: the same 404 status means three different things here
// (mirror off for this account / not a member / no such route) and each needs
// a different 24h-backoff diagnosis; collapsing them, or reporting a 429
// without its Retry-After hint, would send the daemon down the wrong recovery
// path and show the user the wrong reason in `ox doctor`.
func TestRelayGitHubMirrorItems_StatusMapping(t *testing.T) {
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
			body:   `{"error":{"code":"validation_error","message":"invalid batch","details":{"items":"at most 50 items per request","repo.owner":"owner is required"}}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorValidation)
				var ve *api.GitHubMirrorValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, "invalid batch", ve.Message)
				assert.Equal(t, map[string]string{
					"items":      "at most 50 items per request",
					"repo.owner": "owner is required",
				}, ve.Fields)
			},
		},
		{
			name:   "400 without details",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"validation_error","message":"unknown field \"extra\""}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorValidation)
				var ve *api.GitHubMirrorValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, `unknown field "extra"`, ve.Message)
				assert.Nil(t, ve.Fields, "absent details must decode to a nil map, not an empty one")
				assert.Equal(t, `unknown field "extra"`, ve.Error())
			},
		},
		{
			name:   "400 with non-map details is tolerated",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"validation_error","message":"bad","details":["items"]}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorValidation)
				var ve *api.GitHubMirrorValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, "bad", ve.Message)
				assert.Nil(t, ve.Fields)
			},
		},
		{
			name:   "400 with an empty body",
			status: http.StatusBadRequest,
			body:   ``,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorValidation)
				var ve *api.GitHubMirrorValidationError
				require.ErrorAs(t, err, &ve)
				assert.Empty(t, ve.Message)
				assert.Equal(t, api.ErrGitHubMirrorValidation.Error(), err.Error(),
					"with no server wording the sentinel's own text is the message")
			},
		},
		{
			name:   "401 not authenticated",
			status: http.StatusUnauthorized,
			body:   `{"error":{"code":"unauthorized","message":"authentication required"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrUnauthorized)
			},
		},
		{
			name:   "401 with no body",
			status: http.StatusUnauthorized,
			body:   ``,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrUnauthorized)
			},
		},
		{
			name:   "403 carries the server's reason",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"forbidden","message":"relaying requires a signed-in person"}}`,
			check: func(t *testing.T, err error) {
				requireNoGitHubMirrorSentinel(t, err)
				var fe *api.ForbiddenError
				require.ErrorAs(t, err, &fe)
				assert.Equal(t, "relaying requires a signed-in person", fe.Reason)
			},
		},
		{
			name:   "404 empty body: the mirror is not enabled for this account",
			status: http.StatusNotFound,
			body:   ``,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorNotEnabled)
			},
		},
		{
			name:   "404 whitespace-only body: the mirror is not enabled for this account",
			status: http.StatusNotFound,
			body:   "  \n",
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorNotEnabled)
			},
		},
		{
			// The membership layer's nested envelope, as captured from the live
			// server for a team the caller cannot reach.
			name:    "404 nested envelope: not a member",
			status:  http.StatusNotFound,
			body:    "{\"error\":{\"code\":\"NOT_FOUND\",\"message\":\"team not found\"}}\n",
			headers: map[string]string{"Content-Type": "application/json"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorNotAMember)
			},
		},
		{
			// The router's own not-found answer on the live server is a FLAT
			// envelope served as JSON. A mapper that read "any JSON 404" as a
			// membership answer would tell a user on a server without the route
			// to pick another team.
			name:    "404 flat envelope from the router: no mirror route on this server",
			status:  http.StatusNotFound,
			body:    "{\"error\":\"route not registered\"}\n",
			headers: map[string]string{"Content-Type": "application/json"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorUnsupported)
			},
		},
		{
			name:   "404 flat envelope with success:false: still not a membership answer",
			status: http.StatusNotFound,
			body:   `{"success":false,"error":"not found"}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorUnsupported)
			},
		},
		{
			name:    "404 plain text: no mirror route on this server",
			status:  http.StatusNotFound,
			body:    "404 page not found\n",
			headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorUnsupported)
			},
		},
		{
			name:   "404 JSON with no error key is not a membership answer",
			status: http.StatusNotFound,
			body:   `{}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorUnsupported)
			},
		},
		{
			name:   "413 batch too large keeps the server's wording",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"error":{"code":"payload_too_large","message":"request body exceeds 2 MiB"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorTooLarge)
				assert.Contains(t, err.Error(), "request body exceeds 2 MiB")
			},
		},
		{
			name:   "413 with no body is the bare sentinel",
			status: http.StatusRequestEntityTooLarge,
			body:   ``,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorTooLarge)
				assert.Equal(t, api.ErrGitHubMirrorTooLarge.Error(), err.Error())
			},
		},
		{
			name:    "429 with Retry-After",
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"code":"rate_limited","message":"slow down"}}`,
			headers: map[string]string{"Retry-After": "120"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, http.StatusTooManyRequests, be.Status)
				assert.Equal(t, "slow down", be.Message)
				assert.Equal(t, 120*time.Second, be.RetryAfter)
			},
		},
		{
			name:   "429 without Retry-After",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"rate_limited","message":"slow down"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, http.StatusTooManyRequests, be.Status)
				assert.Equal(t, time.Duration(0), be.RetryAfter)
			},
		},
		{
			name:    "503 with Retry-After",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`,
			headers: map[string]string{"Retry-After": "5"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, http.StatusServiceUnavailable, be.Status)
				assert.Equal(t, "team context store unavailable", be.Message)
				assert.Equal(t, 5*time.Second, be.RetryAfter)
			},
		},
		{
			name:   "503 without Retry-After",
			status: http.StatusServiceUnavailable,
			body:   `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`,
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, http.StatusServiceUnavailable, be.Status)
				assert.Equal(t, time.Duration(0), be.RetryAfter)
			},
		},
		{
			name:    "503 with an HTTP-date Retry-After falls back to 0",
			status:  http.StatusServiceUnavailable,
			body:    ``,
			headers: map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, time.Duration(0), be.RetryAfter, "a non-integer Retry-After must fall back to 0")
			},
		},
		{
			// An epoch-milliseconds value from a misconfigured proxy would wrap
			// negative as a Duration and read as "retry at once".
			name:    "429 with an absurd Retry-After falls back to 0",
			status:  http.StatusTooManyRequests,
			body:    ``,
			headers: map[string]string{"Retry-After": "9223372036854775807"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, time.Duration(0), be.RetryAfter)
			},
		},
		{
			name:    "503 with a negative Retry-After falls back to 0",
			status:  http.StatusServiceUnavailable,
			body:    ``,
			headers: map[string]string{"Retry-After": "-30"},
			check: func(t *testing.T, err error) {
				requireOnlyGitHubMirrorSentinel(t, err, api.ErrGitHubMirrorBusy)
				var be *api.GitHubMirrorBusyError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, time.Duration(0), be.RetryAfter)
			},
		},
		{
			name:   "500 is generic and names the server's wording",
			status: http.StatusInternalServerError,
			body:   `{"error":{"code":"internal_error","message":"something broke"}}`,
			check: func(t *testing.T, err error) {
				requireNoGitHubMirrorSentinel(t, err)
				assert.Contains(t, err.Error(), "500")
				assert.Contains(t, err.Error(), "something broke")
			},
		},
		{
			name:   "500 with a non-JSON body still names the status",
			status: http.StatusInternalServerError,
			body:   "upstream exploded",
			check: func(t *testing.T, err error) {
				requireNoGitHubMirrorSentinel(t, err)
				assert.Contains(t, err.Error(), "500")
				assert.Contains(t, err.Error(), "upstream exploded")
			},
		},
		{
			name:   "409 is not part of this contract and stays generic",
			status: http.StatusConflict,
			body:   `{"error":{"code":"conflict","message":"unexpected"}}`,
			check: func(t *testing.T, err error) {
				requireNoGitHubMirrorSentinel(t, err)
				assert.Contains(t, err.Error(), "409")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := gitHubMirrorStaticServer(t, tt.status, tt.body, tt.headers)

			result, err := relayGitHubMirror(t, srv.URL)

			assert.Nil(t, result, "a non-2xx must never yield a response")
			tt.check(t, err)
		})
	}
}

// TestGitHubMirrorErrors_RenderStably pins the human-readable forms.
//
// Failure prevented: Go map iteration is randomized, so an unsorted rendering
// would shuffle a 400's fields between two runs of the same cycle — noise in
// the daemon's last-error line and impossible to assert on in `ox doctor`'s
// tests. A busy error that hid its status would make a 429 and a 503 look
// identical in a log.
func TestGitHubMirrorErrors_RenderStably(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "validation: fields sorted by name",
			err: &api.GitHubMirrorValidationError{
				Message: "invalid batch",
				Fields:  map[string]string{"repo": "unknown", "items": "too many", "a.b": "first"},
			},
			want: "invalid batch (a.b: first; items: too many; repo: unknown)",
		},
		{
			name: "validation: message only",
			err:  &api.GitHubMirrorValidationError{Message: "invalid batch"},
			want: "invalid batch",
		},
		{
			name: "validation: empty falls back to the sentinel text",
			err:  &api.GitHubMirrorValidationError{},
			want: "the server rejected the relay batch",
		},
		{
			name: "validation: fields without a message",
			err:  &api.GitHubMirrorValidationError{Fields: map[string]string{"items": "too many"}},
			want: "the server rejected the relay batch (items: too many)",
		},
		{
			name: "busy: status and retry-after",
			err:  &api.GitHubMirrorBusyError{Status: http.StatusTooManyRequests, Message: "slow down", RetryAfter: 90 * time.Second},
			want: "slow down (HTTP 429, retry after 1m30s)",
		},
		{
			name: "busy: status only",
			err:  &api.GitHubMirrorBusyError{Status: http.StatusServiceUnavailable, Message: "store unavailable"},
			want: "store unavailable (HTTP 503)",
		},
		{
			name: "busy: nothing known falls back to the sentinel text",
			err:  &api.GitHubMirrorBusyError{},
			want: "the GitHub mirror is busy",
		},
		{
			name: "busy: retry-after without a status",
			err:  &api.GitHubMirrorBusyError{RetryAfter: 5 * time.Second},
			want: "the GitHub mirror is busy (retry after 5s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}

	// the typed errors must also satisfy their sentinels without wrapping, so a
	// caller can use errors.Is on a value it built or received.
	assert.ErrorIs(t, &api.GitHubMirrorValidationError{}, api.ErrGitHubMirrorValidation)
	assert.ErrorIs(t, &api.GitHubMirrorBusyError{}, api.ErrGitHubMirrorBusy)
	assert.NotErrorIs(t, &api.GitHubMirrorValidationError{}, api.ErrGitHubMirrorBusy)
	assert.NotErrorIs(t, &api.GitHubMirrorBusyError{}, api.ErrGitHubMirrorValidation)
}

// TestRelayGitHubMirrorItems_VersionGateBeatsBodyParsing covers the 426 hard
// block.
//
// Failure prevented: if the body were parsed before the version gate, a 426
// carrying an error envelope would surface as a generic "HTTP 426" and the
// daemon would back off as if the batch were bad instead of telling the user
// to upgrade ox.
func TestRelayGitHubMirrorItems_VersionGateBeatsBodyParsing(t *testing.T) {
	t.Parallel()

	srv, _ := gitHubMirrorServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(api.HeaderMinVersion, "9.9.9")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = io.WriteString(w, `{"success":false,"error":"upgrade required"}`)
	})

	result, err := relayGitHubMirror(t, srv.URL)

	assert.Nil(t, result)
	requireOnlyGitHubMirrorSentinel(t, err, api.ErrVersionUnsupported)
}

// TestRelayGitHubMirrorItems_HostileErrorBodiesNeverPanicNeverSucceed sweeps
// the shapes of junk that can arrive where an error envelope was expected,
// across the statuses whose bodies are decoded.
//
// Failure prevented: the details decoder for 400 must survive any shape
// (number, array, null, nested junk) rather than panic — and no non-2xx may
// ever turn into a nil error, which the daemon would record as a relayed
// batch.
func TestRelayGitHubMirrorItems_HostileErrorBodiesNeverPanicNeverSucceed(t *testing.T) {
	t.Parallel()

	bodies := []string{
		`null`, `{}`, `[]`, `"boom"`, `42`,
		`{"error":123}`, `{"error":null}`, `{"error":{}}`, `{"error":["a"]}`,
		`{"error":{"code":"x","message":"m","details":null}}`,
		`{"error":{"code":"x","message":"m","details":42}}`,
		`{"error":{"code":"x","message":"m","details":"string"}}`,
		`{"error":{"code":"x","message":"m","details":{"items":["a","b"]}}}`,
		`{"error":{"code":"x","message":"m","details":{"path":123,"state":null}}}`,
		`{"error":{"code":"x"`,
	}
	statuses := []int{
		http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusServiceUnavailable,
	}

	for _, status := range statuses {
		for i, body := range bodies {
			t.Run(fmt.Sprintf("%d/%d", status, i), func(t *testing.T) {
				t.Parallel()
				srv, _ := gitHubMirrorStaticServer(t, status, body, nil)

				result, err := relayGitHubMirror(t, srv.URL)
				assert.Nil(t, result)
				require.Error(t, err, "status %d with body %q must not succeed", status, body)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// D. Transport safety: redirects, oversized responses, cancellation
// ---------------------------------------------------------------------------

// TestRelayGitHubMirrorItems_RedirectIsAnError covers an HTTP redirect on the
// relay route.
//
// Failure prevented: Go's default policy rewrites a 301/302/303 POST into a
// GET and drops the body, and silently re-POSTs on 307/308. A canonicalizing
// redirect in front of the API would then have the client GET (or re-POST to)
// some other resource and decode whatever it returns as the relay response —
// a "relayed" outcome for items no handler ever saw, remembered by the daemon
// as done. The redirect target must never be hit and the 3xx must surface as
// an error.
func TestRelayGitHubMirrorItems_RedirectIsAnError(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/teams/team_abc/github-mirror/items", func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				http.Redirect(w, r, "/api/v1/teams/team_abc", status)
			})
			var targetHit atomic.Bool
			mux.HandleFunc("/api/v1/teams/team_abc", func(w http.ResponseWriter, r *http.Request) {
				targetHit.Store(true)
				rec.record(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, gitHubMirrorOKBody)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			result, err := relayGitHubMirror(t, srv.URL)

			assert.Nil(t, result)
			requireNoGitHubMirrorSentinel(t, err)
			assert.Contains(t, err.Error(), fmt.Sprint(status))
			assert.False(t, targetHit.Load(), "the redirect target must never be requested")
			require.Len(t, rec.all(), 1, "exactly one request — the original POST — may be made")
			assert.Equal(t, http.MethodPost, rec.all()[0].Method)
		})
	}
}

// TestRelayGitHubMirrorItems_OversizedResponseIsBounded covers a response
// whose body never ends.
//
// Failure prevented: a hostile or broken server streaming an unbounded body
// makes the daemon buffer it all (the HTTP timeout bounds time, not memory).
// The read must stop at the cap and report an error rather than hang or grow —
// on the success path and on the error path alike.
func TestRelayGitHubMirrorItems_OversizedResponseIsBounded(t *testing.T) {
	t.Parallel()

	const junkSize = 2 << 20 // 2 MiB, double the 1 MiB cap
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			srv, _ := gitHubMirrorServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				chunk := strings.Repeat("x", 64<<10)
				for written := 0; written < junkSize; written += len(chunk) {
					if _, err := io.WriteString(w, chunk); err != nil {
						return // the client stopped reading, which is the point
					}
				}
			})

			result, err := relayGitHubMirror(t, srv.URL)

			assert.Nil(t, result)
			requireNoGitHubMirrorSentinel(t, err)
			assert.Contains(t, err.Error(), "exceeds")
		})
	}
}

// TestRelayGitHubMirrorItems_ResponseJustUnderTheCapIsAccepted guards the other
// side of the cap.
//
// Failure prevented: an off-by-one in the overflow detection (reading exactly
// the cap and calling it oversized) would reject a legitimate maximal response.
func TestRelayGitHubMirrorItems_ResponseJustUnderTheCapIsAccepted(t *testing.T) {
	t.Parallel()

	const limit = 1 << 20
	prefix := `{"repo_status":"enabled","results":[],"pad":"`
	suffix := `"}`
	body := prefix + strings.Repeat("x", limit-len(prefix)-len(suffix)) + suffix
	require.Len(t, body, limit)

	srv, _ := gitHubMirrorStaticServer(t, http.StatusOK, body, nil)

	got, err := relayGitHubMirror(t, srv.URL)
	require.NoError(t, err, "a body of exactly the cap is not oversized")
	assert.Equal(t, githubmirror.RepoEnabled, got.RepoStatus)
}

// TestRelayGitHubMirrorItems_CanceledContextIsANetworkError covers a daemon
// shutting down mid-relay.
//
// Failure prevented: a canceled context reported as a server problem (or
// swallowed) would make the daemon back off for hours, or record a half-sent
// batch as done, because the process was merely stopping.
func TestRelayGitHubMirrorItems_CanceledContextIsANetworkError(t *testing.T) {
	t.Parallel()

	srv, _ := gitHubMirrorStaticServer(t, http.StatusOK, gitHubMirrorOKBody, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		RelayGitHubMirrorItems(ctx, "team_abc", validGitHubMirrorRequest())

	assert.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	requireNoGitHubMirrorSentinel(t, err)
	assert.Contains(t, err.Error(), "network error")
}
