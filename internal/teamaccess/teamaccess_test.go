package teamaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every test runs against the real auth store (under a temp HOME) and the
// real API client, pointed at an httptest server that plays SageOx. Nothing
// here can reach a live endpoint or the developer's own login.

const testTeam = "team_alpha"

// isolate reroutes every credential and cache path into a temp home.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv(auth.EnvVarToken, "")
	t.Setenv("SAGEOX_ENDPOINT", "")
	origNow := now
	t.Cleanup(func() { now = origNow })
}

// fakeSageOx plays the three routes the check can touch: the memberships
// list, token introspection, and the refresh grant.
type fakeSageOx struct {
	*httptest.Server
	mu sync.Mutex
	// teams is what GET /api/v1/cli/repos reports; nil omits the array (an
	// older server), in which case repos drives the derived list.
	teams     []api.TeamMembership
	repos     map[string]api.RepoInfo
	down      bool   // answer 503 everywhere
	reject    string // access token that /cli/repos answers 401 for
	introTeam string // team_id introspection reports for an oxt_ token
	refreshTo string // access token the refresh grant hands back ("" = 401)

	reposCalls   atomic.Int32
	introCalls   atomic.Int32
	refreshCalls atomic.Int32
}

func newFakeSageOx(t *testing.T) *fakeSageOx {
	t.Helper()
	f := &fakeSageOx{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSageOx) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/cli/repos":
		f.reposCalls.Add(1)
		if bearer == "" || bearer == f.reject {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := map[string]any{"repos": f.repos}
		if f.teams != nil {
			body["teams"] = f.teams
		}
		_ = json.NewEncoder(w).Encode(body)
	case auth.IntrospectEndpoint:
		f.introCalls.Add(1)
		if bearer == f.reject {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "principal_kind": auth.PrincipalKindTeamService,
			"team": map[string]string{"team_id": f.introTeam},
		})
	case auth.TokenEndpoint:
		f.refreshCalls.Add(1)
		if f.refreshTo == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.refreshTo, "refresh_token": "rt-next", "expires_in": 3600, "token_type": "Bearer",
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSageOx) set(fn func(f *fakeSageOx)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func login(t *testing.T, ep, access string, expiresIn time.Duration) {
	t.Helper()
	require.NoError(t, auth.SaveTokenForEndpoint(ep, &auth.StoredToken{
		AccessToken:  access,
		RefreshToken: "rt-" + access,
		ExpiresAt:    time.Now().Add(expiresIn),
		TokenType:    "Bearer",
	}))
}

func member(ids ...string) []api.TeamMembership {
	out := make([]api.TeamMembership, 0, len(ids))
	for _, id := range ids {
		out = append(out, api.TeamMembership{ID: id, Name: id})
	}
	return out
}

// --- A. No credential: refused, and the server is never asked ---

// TestCheck_NotSignedIn: no login for the endpoint refuses without a network
// call. Failure prevented: a logged-out machine reading a checkout that
// outlived `ox logout`.
func TestCheck_NotSignedIn(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)

	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, NotSignedIn, v.Status)
	assert.False(t, v.Allowed())
	assert.Contains(t, v.Message("Alpha"), "ox login")
	assert.Zero(t, f.reposCalls.Load(), "no membership call without a credential")
}

// TestCheck_ExpiredAndNotRefreshable: an expired login whose refresh is
// refused is Expired, never Allowed.
func TestCheck_ExpiredAndNotRefreshable(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	login(t, f.URL, "tok-old", -time.Hour)

	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Expired, v.Status)
	assert.Contains(t, v.Message("Alpha"), "expired")
	assert.Equal(t, int32(1), f.refreshCalls.Load())
	assert.Zero(t, f.reposCalls.Load())
}

// TestCheck_ExpiredRefreshThenAllowed: an expired login that refreshes is
// checked with the new token and allowed.
func TestCheck_ExpiredRefreshThenAllowed(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.refreshTo = "tok-new"; f.teams = member(testTeam) })
	login(t, f.URL, "tok-old", -time.Hour)

	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Allowed, v.Status)
	assert.False(t, v.FromCache)
	assert.Equal(t, int32(1), f.refreshCalls.Load())
}

// TestCheck_RefreshUnreachableIsUnverified: an expired login that cannot be
// renewed because the server is unreachable is Unverified (retryable), not
// "sign in again".
func TestCheck_RefreshUnreachableIsUnverified(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	ep := f.URL
	login(t, ep, "tok-old", -time.Hour)
	f.Close()

	v := Check(context.Background(), ep, testTeam)
	assert.Equal(t, Unverified, v.Status)
}

// --- B. Personal credentials: the server's membership list decides ---

func TestCheck_PersonalMembership(t *testing.T) {
	tests := []struct {
		name  string
		teams []api.TeamMembership
		repos map[string]api.RepoInfo
		want  Status
	}{
		{name: "member", teams: member("team_other", testTeam), want: Allowed},
		{name: "not a member", teams: member("team_other"), want: NoAccess},
		{name: "member of no team", teams: []api.TeamMembership{}, want: NoAccess},
		{
			// An older server omits teams; the list is derived from the
			// team-context repos it does report.
			name:  "older server, team-context repo present",
			repos: map[string]api.RepoInfo{"alpha": {Type: "team-context", TeamID: testTeam, Name: "alpha"}},
			want:  Allowed,
		},
		{
			name:  "older server, no team-context repo",
			repos: map[string]api.RepoInfo{"x": {Type: "team-context", TeamID: "team_other", Name: "x"}},
			want:  NoAccess,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			f := newFakeSageOx(t)
			f.set(func(f *fakeSageOx) { f.teams = tt.teams; f.repos = tt.repos })
			login(t, f.URL, "tok-live", time.Hour)

			v := Check(context.Background(), f.URL, testTeam)
			assert.Equal(t, tt.want, v.Status)
			if tt.want == NoAccess {
				assert.Contains(t, v.Message("Alpha"), "doesn't have access to Alpha")
			}
		})
	}
}

// TestCheck_401RefreshesOnceThenAllows: a token the server rejects is
// refreshed once and the check retried with the new one.
func TestCheck_401RefreshesOnceThenAllows(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.reject = "tok-revoked"; f.refreshTo = "tok-new"; f.teams = member(testTeam) })
	login(t, f.URL, "tok-revoked", time.Hour)

	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Allowed, v.Status)
	assert.Equal(t, int32(1), f.refreshCalls.Load())
	assert.Equal(t, int32(2), f.reposCalls.Load())

	// The cache is keyed by the token that was confirmed (the new one): the
	// next call is served from it.
	v = Check(context.Background(), f.URL, testTeam)
	assert.True(t, v.FromCache)
}

// TestCheck_401AndRefreshRefusedIsExpired: a rejected token with no way to
// refresh is Expired.
func TestCheck_401AndRefreshRefusedIsExpired(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.reject = "tok-revoked"; f.teams = member(testTeam) })
	login(t, f.URL, "tok-revoked", time.Hour)

	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Expired, v.Status)
}

// --- C. Team service tokens: introspection names the team ---

func TestCheck_TeamToken(t *testing.T) {
	tests := []struct {
		name      string
		introTeam string
		reject    bool
		want      Status
	}{
		{name: "right team", introTeam: testTeam, want: Allowed},
		{name: "wrong team", introTeam: "team_other", want: NoAccess},
		{name: "rejected", reject: true, want: Expired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			f := newFakeSageOx(t)
			tok := auth.TeamTokenPrefix + "svc"
			f.set(func(f *fakeSageOx) {
				f.introTeam = tt.introTeam
				if tt.reject {
					f.reject = tok
				}
			})
			login(t, f.URL, tok, time.Hour)

			v := Check(context.Background(), f.URL, testTeam)
			assert.Equal(t, tt.want, v.Status)
			assert.Equal(t, int32(1), f.introCalls.Load())
			assert.Zero(t, f.reposCalls.Load(), "a team token is never checked against a personal membership list")
		})
	}
}

// --- D. Cache: allowed only, under an hour, per token ---

// TestCheck_CacheHitUnderAnHour: a second check inside the hour is served
// from the cache with no network call.
func TestCheck_CacheHitUnderAnHour(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", 2*time.Hour)

	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())
	require.Equal(t, int32(1), f.reposCalls.Load())

	base := time.Now()
	now = func() time.Time { return base.Add(59 * time.Minute) }
	v := Check(context.Background(), f.URL, testTeam)
	assert.True(t, v.Allowed())
	assert.True(t, v.FromCache)
	assert.Equal(t, int32(1), f.reposCalls.Load(), "cache hit must not call the server")
}

// TestCheck_CacheStaleAsksAgain: after an hour the server is asked again,
// and a membership that was revoked meanwhile is refused.
func TestCheck_CacheStaleAsksAgain(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", 3*time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	f.set(func(f *fakeSageOx) { f.teams = member("team_other") })
	base := time.Now()
	now = func() time.Time { return base.Add(61 * time.Minute) }
	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, NoAccess, v.Status)
	assert.Equal(t, int32(2), f.reposCalls.Load())
}

// TestCheck_OfflineWithFreshCacheAllows: offline within the hour still reads.
func TestCheck_OfflineWithFreshCacheAllows(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", 2*time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	f.set(func(f *fakeSageOx) { f.down = true })
	v := Check(context.Background(), f.URL, testTeam)
	assert.True(t, v.Allowed())
	assert.True(t, v.FromCache)
}

// TestCheck_OfflineStaleIsUnverified: offline past the hour is refused as
// retryable, never passed.
func TestCheck_OfflineStaleIsUnverified(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", 3*time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	f.set(func(f *fakeSageOx) { f.down = true })
	base := time.Now()
	now = func() time.Time { return base.Add(2 * time.Hour) }
	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Unverified, v.Status)
	assert.Contains(t, v.Message("Alpha"), "connect and retry")
}

// TestCheck_UnreachableNoCacheIsUnverified: a server that cannot be reached
// and no cache is Unverified.
func TestCheck_UnreachableNoCacheIsUnverified(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	ep := f.URL
	login(t, ep, "tok-live", time.Hour)
	f.Close()

	v := Check(context.Background(), ep, testTeam)
	assert.Equal(t, Unverified, v.Status)
	assert.Equal(t, "the server could not be reached", v.Detail)
}

// TestCheck_TokenChangeInvalidatesCache: a different login (another account,
// or a re-login) never reuses the previous token's confirmation.
func TestCheck_TokenChangeInvalidatesCache(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-alice", time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	// Bob is signed in now, and is not a member.
	login(t, f.URL, "tok-bob", time.Hour)
	f.set(func(f *fakeSageOx) { f.teams = member("team_other") })
	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, NoAccess, v.Status)
	assert.False(t, v.FromCache)
}

// TestCheck_CacheIsPerTeam: a confirmation for one team never admits another.
func TestCheck_CacheIsPerTeam(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	v := Check(context.Background(), f.URL, "team_other")
	assert.Equal(t, NoAccess, v.Status)
}

// TestCheck_RefusalsAreNotCached: a refusal is re-asked every time, so a
// re-granted membership works on the next command.
func TestCheck_RefusalsAreNotCached(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member("team_other") })
	login(t, f.URL, "tok-live", time.Hour)
	require.Equal(t, NoAccess, Check(context.Background(), f.URL, testTeam).Status)

	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	v := Check(context.Background(), f.URL, testTeam)
	assert.Equal(t, Allowed, v.Status)
	assert.False(t, v.FromCache)
	assert.Equal(t, int32(2), f.reposCalls.Load())
}

// TestCheck_RevokedMembershipDropsCacheEntry: once the server refuses, the
// earlier confirmation is gone even if it was still inside the hour.
func TestCheck_RevokedMembershipDropsCacheEntry(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-live", 3*time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	f.set(func(f *fakeSageOx) { f.teams = member("team_other") })
	base := time.Now()
	now = func() time.Time { return base.Add(61 * time.Minute) }
	require.Equal(t, NoAccess, Check(context.Background(), f.URL, testTeam).Status)

	// Back inside the original hour on the clock, the entry must not revive.
	now = func() time.Time { return base.Add(time.Minute) }
	f.set(func(f *fakeSageOx) { f.down = true })
	assert.Equal(t, Unverified, Check(context.Background(), f.URL, testTeam).Status)
}

// TestCheck_FutureCheckedAtIsNotFresh: a cache entry stamped in the future
// (clock moved back, hand-edited file) is not a confirmation.
func TestCheck_FutureCheckedAtIsNotFresh(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	ep := f.URL
	login(t, ep, "tok-live", time.Hour)
	require.NoError(t, SeedAllowed(paths.CacheDir(), ep, "tok-live", testTeam, time.Now().Add(24*time.Hour)))
	f.Close()

	assert.Equal(t, Unverified, Check(context.Background(), ep, testTeam).Status)
}

// TestCache_FileHoldsNoToken: the cache file never contains the credential,
// and it is private to the user.
func TestCache_FileHoldsNoToken(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	f.set(func(f *fakeSageOx) { f.teams = member(testTeam) })
	login(t, f.URL, "tok-secret-value", time.Hour)
	require.True(t, Check(context.Background(), f.URL, testTeam).Allowed())

	path := filepath.Join(paths.CacheDir(), CacheFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "tok-secret-value")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSeedAllowed_AdmitsOffline: the harness seam writes exactly what a
// server confirmation would.
func TestSeedAllowed_AdmitsOffline(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	ep := f.URL
	login(t, ep, "tok-live", time.Hour)
	require.NoError(t, SeedAllowed(paths.CacheDir(), ep, "tok-live", testTeam, time.Now()))
	f.Close()

	v := Check(context.Background(), ep, testTeam)
	assert.True(t, v.Allowed())
	assert.True(t, v.FromCache)
}

// TestCheck_MissingTeamFailsClosed: no team id is never Allowed.
func TestCheck_MissingTeamFailsClosed(t *testing.T) {
	isolate(t)
	f := newFakeSageOx(t)
	login(t, f.URL, "tok-live", time.Hour)
	assert.Equal(t, Unverified, Check(context.Background(), f.URL, "").Status)
}

// TestFindMembership is the shared membership test doctor also uses.
func TestFindMembership(t *testing.T) {
	teams := []api.TeamMembership{{ID: "team_a", Name: "A"}, {ID: "team_b", Name: "B", Personal: true}}
	got, ok := FindMembership(teams, "team_b")
	assert.True(t, ok)
	assert.True(t, got.Personal)
	_, ok = FindMembership(teams, "team_c")
	assert.False(t, ok)
	_, ok = FindMembership(teams, "")
	assert.False(t, ok)
	_, ok = FindMembership(nil, "team_a")
	assert.False(t, ok)
}
