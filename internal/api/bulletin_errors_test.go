package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The typed bulletin errors render themselves in logs and in the command's
// generic fallback, so their wording must be stable and must never be empty.
//
// Failure prevented: an empty Message from the server producing an empty
// error string, which the CLI would print as a blank headline.
func TestBulletinTypedErrors_RenderWithAndWithoutServerWording(t *testing.T) {
	var verr error = &api.BulletinValidationError{}
	assert.Equal(t, api.ErrBulletinValidation.Error(), verr.Error())
	assert.ErrorIs(t, verr, api.ErrBulletinValidation)

	verr = &api.BulletinValidationError{Message: "rejected", Fields: map[string]string{"ttl": "too long", "board": "unknown"}}
	assert.Equal(t, "rejected (board: unknown; ttl: too long)", verr.Error(), "fields render sorted by key")

	var derr error = &api.BulletinDuplicateError{}
	assert.Equal(t, api.ErrBulletinDuplicate.Error(), derr.Error())
	assert.ErrorIs(t, derr, api.ErrBulletinDuplicate)
	derr = &api.BulletinDuplicateError{Message: "dup", Duplicate: api.BulletinDuplicate{Path: "bulletin/general/posts/x-abc.md"}}
	assert.Equal(t, "dup (existing post: bulletin/general/posts/x-abc.md)", derr.Error())

	var uerr error = &api.BulletinUnavailableError{}
	assert.Equal(t, api.ErrBulletinUnavailable.Error(), uerr.Error())
	assert.ErrorIs(t, uerr, api.ErrBulletinUnavailable)
	uerr = &api.BulletinUnavailableError{Message: "store down", RetryAfter: 5 * time.Second}
	assert.Equal(t, "store down (retry after 5s)", uerr.Error())
}

// A 403 is outside this endpoint's contract, but a proxy or a future server
// may answer it; the server's own wording must survive rather than being
// replaced by an invented reason.
//
// Failure prevented: a 403 collapsing into the generic "HTTP 403" fallback
// and losing the one sentence that says why.
func TestPublishBulletinPost_ForbiddenCarriesTheServersReason(t *testing.T) {
	t.Parallel()
	srv, _ := bulletinStaticServer(t, http.StatusForbidden, `{"error":{"code":"forbidden","message":"owners only"}}`, nil)
	_, err := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	require.Error(t, err)
	var fe *api.ForbiddenError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "owners only", fe.Reason)
	assert.True(t, errors.Is(err, api.ErrInviteForbidden), "the shared 403 class must still match")
}

// A Retry-After that cannot be a number of seconds anyone meant (an epoch
// timestamp in milliseconds from a misconfigured proxy) must not wrap into a
// negative duration that reads as "retry at once".
//
// Failure prevented: the command retrying on its 1s floor against a store
// the server asked it to leave alone.
func TestPublishBulletinPost_RetryAfterOutOfRangeIsZero(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"1790000000000", "99999999999999999999", "-5", "5.5", "Wed, 21 Oct 2026 07:28:00 GMT"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			srv, _ := bulletinStaticServer(t, http.StatusServiceUnavailable,
				`{"error":{"code":"team_context_unavailable","message":"unavailable"}}`,
				map[string]string{"Retry-After": raw})
			_, err := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").
				PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
			var ue *api.BulletinUnavailableError
			require.ErrorAs(t, err, &ue)
			assert.Equal(t, time.Duration(0), ue.RetryAfter, "unparseable or out-of-range Retry-After must be 0, never negative")
		})
	}
	srv, _ := bulletinStaticServer(t, http.StatusServiceUnavailable,
		`{"error":{"code":"team_context_unavailable","message":"unavailable"}}`,
		map[string]string{"Retry-After": "7"})
	_, err := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").
		PublishBulletinPost(context.Background(), "team_abc", validBulletinRequest())
	var ue *api.BulletinUnavailableError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, 7*time.Second, ue.RetryAfter)
}
