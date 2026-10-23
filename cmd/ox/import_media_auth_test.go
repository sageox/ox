package main

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/errkind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ox import <media>: the sign-in is refreshed before it is used ---

// tokenServer answers the OAuth refresh in front of a fakeRecordingServer and
// records the Authorization header of every recording API call.
type tokenServer struct {
	next   *fakeRecordingServer
	status int // refresh answer; 0 = 200 with freshToken

	mu        sync.Mutex
	refreshes int
	auths     map[string][]string // "upload" / "confirm" -> Authorization headers in order
	onStorage func()              // runs on every presigned PUT, before the fake stores it
}

const freshAccessToken = "fresh-access-token"

func newTokenServer(t *testing.T, f *mediaFixture, mode string) *tokenServer {
	t.Helper()
	ts := &tokenServer{next: newFakeRecordingServer(t, f.importRetryFixture, mode), auths: map[string][]string{}}
	var h http.Handler = ts
	f.api.Store(&h)
	return ts
}

func (ts *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == auth.TokenEndpoint:
		ts.mu.Lock()
		ts.refreshes++
		status := ts.status
		ts.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": freshAccessToken, "token_type": "Bearer", "expires_in": 3600})
		return
	case strings.HasSuffix(r.URL.Path, "/recordings/upload"):
		ts.record("upload", r)
	case strings.HasSuffix(r.URL.Path, "/confirm"):
		ts.record("confirm", r)
	case strings.HasPrefix(r.URL.Path, "/storage/"):
		ts.mu.Lock()
		hook := ts.onStorage
		ts.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	// the JWT exchange after a refresh falls through to a 404, so ox keeps the
	// refreshed opaque token — the token the API calls must then carry
	ts.next.ServeHTTP(w, r)
}

func (ts *tokenServer) record(route string, r *http.Request) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.auths[route] = append(ts.auths[route], r.Header.Get("Authorization"))
}

func (ts *tokenServer) seen() (refreshes int, auths map[string][]string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make(map[string][]string, len(ts.auths))
	for k, v := range ts.auths {
		out[k] = append([]string(nil), v...)
	}
	return ts.refreshes, out
}

// saveExpiredToken replaces the fixture's sign-in with one that ran out a
// minute ago but can still be refreshed. It may run on a handler goroutine,
// so it reports with Errorf, never FailNow.
func saveExpiredToken(t *testing.T, f *mediaFixture) {
	t.Helper()
	if err := auth.SaveTokenForEndpoint(f.endpoint, &auth.StoredToken{
		AccessToken: "expired-access-token", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(-time.Minute), TokenType: "Bearer",
	}); err != nil {
		t.Errorf("save expired token: %v", err)
	}
}

// TestImportMedia_ExpiredSignInIsRefreshedBeforeUpload: a sign-in that has run
// out is refreshed before the first upload request, and every API call carries
// the refreshed token.
// Failure prevented: an import that starts with a dead token and fails with
// 401 on the upload request, or — for a token with minutes left — at confirm,
// after every byte was sent.
func TestImportMedia_ExpiredSignInIsRefreshedBeforeUpload(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	saveExpiredToken(t, f)
	ts := newTokenServer(t, f, "single")

	stdout, stderr, err := f.importMedia(false, false)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stdout, "Imported: standup")

	refreshes, auths := ts.seen()
	assert.Equal(t, 1, refreshes, "the expired sign-in is refreshed once, before the upload")
	assert.Equal(t, []string{"Bearer " + freshAccessToken}, auths["upload"], "the upload request carries the refreshed token")
	assert.Equal(t, []string{"Bearer " + freshAccessToken}, auths["confirm"], "confirm carries the refreshed token")
}

// TestImportMedia_RefreshRefusedStopsBeforeUpload: when the refresh is refused,
// nothing is uploaded and the person is told to sign in again.
// Failure prevented: an import that uploads the file under a dead sign-in and
// then fails with an unexplained 401, leaving an orphaned upload behind.
func TestImportMedia_RefreshRefusedStopsBeforeUpload(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	saveExpiredToken(t, f)
	ts := newTokenServer(t, f, "single")
	ts.status = http.StatusBadRequest

	stdout, _, err := f.importMedia(false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ox login")
	assert.Equal(t, errkind.NotLoggedIn, errkind.Of(err))
	assert.NotContains(t, stdout, "Imported:")

	_, auths := ts.seen()
	assert.Empty(t, auths["upload"], "nothing is uploaded under a sign-in that could not be refreshed")
	assert.Empty(t, ts.next.snapshot().attempts, "no bytes reach storage")
}

// TestImportMedia_SignInExpiringDuringUploadIsRefreshedBeforeConfirm: a
// sign-in that runs out while the bytes are uploading is refreshed before
// confirm, so the upload is not lost to a 401 at the last step.
// Failure prevented: a long upload of a big recording that sends every byte
// and then fails at confirm because the token it started with has expired.
func TestImportMedia_SignInExpiringDuringUploadIsRefreshedBeforeConfirm(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	ts := newTokenServer(t, f, "single")
	// the token is good when the import starts and has run out by the time the
	// bytes are stored
	ts.onStorage = func() { saveExpiredToken(t, f) }

	_, stderr, err := f.importMedia(false, false)
	require.NoError(t, err, "stderr: %s", stderr)

	refreshes, auths := ts.seen()
	assert.Equal(t, []string{"Bearer test-access-token"}, auths["upload"], "a valid sign-in is used as it is")
	assert.Equal(t, 1, refreshes, "the sign-in that ran out during the upload is refreshed once")
	assert.Equal(t, []string{"Bearer " + freshAccessToken}, auths["confirm"], "confirm carries the refreshed token")
}

// TestImportMedia_RefreshFailingAfterUpload: when the refresh before confirm
// fails, a token that has not run out yet still confirms the upload; one that
// has run out stops the import with the sign-in error, sending no confirm the
// server would refuse.
// Failure prevented: a multi-gigabyte upload discarded because a network blip
// hit the refresh while the token in hand had minutes left — or a dead token
// sent to confirm and answered with a bare "unauthorized".
func TestImportMedia_RefreshFailingAfterUpload(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn time.Duration // of the stored sign-in once the bytes are uploaded
		wantErr   bool
	}{
		{"sign-in still has minutes left", 4 * time.Minute, false},
		{"sign-in ran out during the upload", -time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMediaFixture(t, "standup.m4a", 4096)
			ts := newTokenServer(t, f, "single")
			ts.onStorage = func() {
				// by the time the bytes are stored the sign-in is inside the
				// refresh buffer (or past it), and the refresh then fails
				ts.mu.Lock()
				ts.status = http.StatusServiceUnavailable
				ts.mu.Unlock()
				if err := auth.SaveTokenForEndpoint(f.endpoint, &auth.StoredToken{
					AccessToken: "test-access-token", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(tt.expiresIn), TokenType: "Bearer",
				}); err != nil {
					t.Errorf("save token: %v", err)
				}
			}

			stdout, stderr, err := f.importMedia(false, false)
			refreshes, auths := ts.seen()
			assert.Equal(t, 1, refreshes, "the refresh before confirm was attempted")
			if tt.wantErr {
				f.assertNotImported(t, stdout, err)
				assert.Equal(t, errkind.NotLoggedIn, errkind.Of(err))
				assert.Empty(t, auths["confirm"], "no confirm with a sign-in that has run out")
				return
			}
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, stdout, "Imported: standup")
			assert.Equal(t, []string{"Bearer test-access-token"}, auths["confirm"], "confirm goes out with the sign-in that still has time left")
		})
	}
}
