package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Refresh vs. a login that changes while the refresh is on the server ---
// A refresh reads the stored login, then waits seconds on the token server
// (refresh grant plus JWT exchange). `ox logout`, `ox login`, or another
// process's refresh can rewrite auth.json during that wait, and the refresh
// may only save over the login it started from.

// refreshEntry is one way into the refresh code, bound to one auth file.
type refreshEntry struct {
	// client acts on the same auth file as another process would: `ox logout`
	// and `ox login` run there.
	client *AuthClient
	// refresh runs the refresh under test on the login stored before it.
	refresh func(stored *StoredToken) (*StoredToken, error)
	// stored reads the login the refresh's own storage holds afterwards.
	stored func() (*StoredToken, error)
}

// refreshEntries lists every refresh that saves a token. The package-level ones
// use the real auth file location, so the test must not run in parallel.
var refreshEntries = []struct {
	name string
	open func(t *testing.T, ep string) refreshEntry
}{
	{
		name: "daemon proactive refresh",
		open: func(t *testing.T, ep string) refreshEntry {
			isolateAuthFile(t)
			return refreshEntry{
				client: NewAuthClient().WithEndpoint(ep),
				refresh: func(*StoredToken) (*StoredToken, error) {
					return EnsureValidTokenForEndpointContext(context.Background(), ep, 300)
				},
				stored: func() (*StoredToken, error) { return GetTokenForEndpoint(ep) },
			}
		},
	},
	{
		name: "command refresh after a 401",
		open: func(t *testing.T, ep string) refreshEntry {
			isolateAuthFile(t)
			return refreshEntry{
				client:  NewAuthClient().WithEndpoint(ep),
				refresh: func(stored *StoredToken) (*StoredToken, error) { return Handle401ErrorForEndpoint(stored, ep) },
				stored:  func() (*StoredToken, error) { return GetTokenForEndpoint(ep) },
			}
		},
	},
	{
		name: "AuthClient proactive refresh",
		open: func(t *testing.T, ep string) refreshEntry {
			dir := filepath.Join(t.TempDir(), "sageox")
			own := NewAuthClientWithDir(dir).WithEndpoint(ep)
			return refreshEntry{
				client:  NewAuthClientWithDir(dir).WithEndpoint(ep),
				refresh: func(*StoredToken) (*StoredToken, error) { return own.EnsureValidToken(300) },
				stored:  own.GetToken,
			}
		},
	},
	{
		name: "AuthClient refresh after a 401",
		open: func(t *testing.T, ep string) refreshEntry {
			dir := filepath.Join(t.TempDir(), "sageox")
			own := NewAuthClientWithDir(dir).WithEndpoint(ep)
			return refreshEntry{
				client:  NewAuthClientWithDir(dir).WithEndpoint(ep),
				refresh: own.Handle401Error,
				stored:  own.GetToken,
			}
		},
	},
}

// isolateAuthFile points the package-level auth file into a temp dir and
// proves it did, so a failed isolation cannot touch the real login.
func isolateAuthFile(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(EnvVarToken, "")
	path, err := GetAuthFilePath()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(path, dir), "auth file %s is outside the test dir %s", path, dir)
}

// TestRefresh_SavesOnlyOverTheLoginItRefreshed changes the stored login while
// the refresh grant is on the server, through every refresh that saves.
// Failure prevented: a refresh saved blindly when the server answered, so a
// login `ox logout` had just revoked and deleted came back on disk after the
// logout reported success, and an older login overwrote a newer `ox login`.
// Also prevented: the refresh token a rotating server issued mid-logout stayed
// live on the server after the logout, with no local copy left to revoke.
func TestRefresh_SavesOnlyOverTheLoginItRefreshed(t *testing.T) {
	otherAccount := &StoredToken{
		AccessToken:  "other-account-access",
		RefreshToken: "other-account-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		UserInfo:     UserInfo{UserID: "user-other", Email: "other@example.com"},
	}
	siblingRefresh := createTestToken(time.Hour)
	siblingRefresh.AccessToken = "sibling-refresh-jwt"
	siblingRefresh.RefreshToken = "sibling-refresh-rt"
	logout := func(c *AuthClient) error {
		status, err := c.RevokeTokenStatus()
		if err == nil && status != RevocationSuccess {
			return errors.New("logout did not report success")
		}
		return err
	}

	races := []struct {
		name string
		// meanwhile runs, as another process, while the refresh grant is on
		// the server; nil changes nothing.
		meanwhile func(c *AuthClient) error
		// keepsRefreshToken makes the server answer with the refresh token it
		// was sent, as a Better Auth session token does, instead of rotating it.
		keepsRefreshToken bool
		// wantAccess is the access token the refresh returns and auth.json
		// holds afterwards; "" means a logout that must stay logged out.
		wantAccess string
		// wantRevoked lists the tokens the server is asked to revoke, in order.
		wantRevoked []string
	}{
		{
			name:        "ox logout revoked and removed the login",
			meanwhile:   logout,
			wantRevoked: []string{"test-refresh-token", "refreshed-rt"},
		},
		{
			name:              "ox logout revoked the session the server keeps",
			meanwhile:         logout,
			keepsRefreshToken: true,
			wantRevoked:       []string{"test-refresh-token"},
		},
		{
			name:       "ox login saved another account",
			meanwhile:  func(c *AuthClient) error { return c.SaveToken(otherAccount) },
			wantAccess: "other-account-access",
		},
		{
			name:       "another process's refresh saved first",
			meanwhile:  func(c *AuthClient) error { return c.SaveToken(siblingRefresh) },
			wantAccess: "sibling-refresh-jwt",
		},
		{
			name:       "nothing changed, so the refreshed login is saved",
			wantAccess: "refreshed-jwt",
		},
	}

	for _, entry := range refreshEntries {
		t.Run(entry.name, func(t *testing.T) {
			for _, race := range races {
				t.Run(race.name, func(t *testing.T) {
					var meanwhileErr error
					var revokedMu sync.Mutex
					var revoked []string
					srv := httptest.NewUnstartedServer(nil)
					ep := "http://" + srv.Listener.Addr().String()
					e := entry.open(t, ep)
					srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case TokenEndpoint:
							if race.meanwhile != nil {
								meanwhileErr = race.meanwhile(e.client)
							}
							rt := "refreshed-rt"
							if race.keepsRefreshToken {
								rt = r.FormValue("refresh_token")
							}
							_ = json.NewEncoder(w).Encode(map[string]any{
								"access_token":  "refreshed-opaque",
								"refresh_token": rt,
								"token_type":    "Bearer",
								"expires_in":    3600,
							})
						case "/api/v1/cli/auth/token":
							_ = json.NewEncoder(w).Encode(map[string]any{
								"access_token": "refreshed-jwt",
								"token_type":   "Bearer",
								"expires_in":   900,
							})
						case RevokeEndpoint:
							revokedMu.Lock()
							revoked = append(revoked, r.FormValue("token"))
							revokedMu.Unlock()
							w.WriteHeader(http.StatusOK)
						default:
							t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					})
					srv.Start()
					defer srv.Close()

					startedFrom := createTestToken(-time.Hour)
					require.NoError(t, e.client.SaveToken(startedFrom))

					got, err := e.refresh(startedFrom)
					require.NoError(t, meanwhileErr, "the concurrent change itself failed")

					stored, readErr := e.stored()
					require.NoError(t, readErr)

					revokedMu.Lock()
					assert.Equal(t, race.wantRevoked, revoked,
						"logout must leave no refreshed token live on the server, and nothing else may be revoked")
					revokedMu.Unlock()

					if race.wantAccess == "" {
						assert.Nil(t, stored, "a login logout removed came back on disk")
						require.ErrorIs(t, err, ErrLoggedOutDuringRefresh)
						var refreshErr *TokenRefreshError
						assert.ErrorAs(t, err, &refreshErr, "callers branch on TokenRefreshError")
						assert.Nil(t, got)
						return
					}
					if assert.NotNil(t, stored, "the stored login is gone") {
						assert.Equal(t, race.wantAccess, stored.AccessToken, "the refresh overwrote a newer login")
					}
					require.NoError(t, err)
					require.NotNil(t, got)
					assert.Equal(t, race.wantAccess, got.AccessToken, "the refresh must return the login on disk")
				})
			}
		})
	}
}
