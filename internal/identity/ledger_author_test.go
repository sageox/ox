package identity

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/gitserver"
)

// isolateSignIns gives a test its own auth store and git-credential records,
// outside the OS keychain, and no SAGEOX_TOKEN. It returns the config home
// the records are under.
func isolateSignIns(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "sageox"), 0o755))
	for _, name := range []string{"SAGEOX_TOKEN", "OX_XDG_DISABLE"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	return xdg
}

func newServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func saveSignIn(t *testing.T, serverURL, accessToken, userID, name string) {
	t.Helper()
	require.NoError(t, auth.SaveTokenForEndpoint(serverURL, &auth.StoredToken{
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    auth.UserInfo{UserID: userID, Email: userID + "@example.com", Name: name},
	}))
}

func saveGitServer(t *testing.T, apiURL, gitServer, fetchedWith string) {
	t.Helper()
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiURL, gitserver.GitCredentials{
		Token: "pat", ServerURL: gitServer, ExpiresAt: time.Now().Add(time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint(fetchedWith),
	}))
}

// Failure prevented: Ledger commits carry the account's full name or real
// email, which stay local by the privacy rule; or a configured display_name
// is ignored; or a server nobody is signed in on gets someone's identity.
func TestLedgerAuthor_DisplayNameAndAccountAddress(t *testing.T) {
	isolateSignIns(t)
	api := newServer(t)
	saveSignIn(t, api, "token-devon", "usr_devon", "Devon Example")

	name, email := LedgerAuthor(api+"/team/ledger.git", "")
	assert.Equal(t, "Devon E.", name)
	assert.Equal(t, "usr_devon@sageox-account.invalid", email)

	name, _ = LedgerAuthor(api+"/team/ledger.git", "  Dev  ")
	assert.Equal(t, "Dev", name, "a configured display_name names the author")

	name, email = LedgerAuthor(newServer(t)+"/team/ledger.git", "")
	assert.Empty(t, name+email, "nobody is signed in on that server")
}

// Failure prevented: two local stacks on one host share a git-credential
// record (records are keyed by host), so the account signed in on one server
// authors the other server's Ledger commits.
func TestLedgerAuthor_SharedCredentialRecordBelongsToItsOwner(t *testing.T) {
	isolateSignIns(t)
	ledgerAPI, otherAPI := newServer(t), newServer(t)
	saveSignIn(t, ledgerAPI, "token-devon", "usr_devon", "Devon Example")
	saveSignIn(t, otherAPI, "token-quinn", "usr_quinn", "Quinn Other")
	saveGitServer(t, ledgerAPI, ledgerAPI, "token-devon")

	// Sign-ins are visited in random order; enough calls visit Quinn's first.
	for range 32 {
		_, email := LedgerAuthor(ledgerAPI+"/team/ledger.git", "")
		require.Equal(t, "usr_devon@sageox-account.invalid", email, "another server's account must not author")
	}
}

// Failure prevented: credentials fetched for one local stack (say with a
// SAGEOX_TOKEN since unset) are read by the only remaining sign-in, on another
// port of the same host, which then authors the first stack's Ledger commits.
func TestLedgerAuthor_OnlySignInDoesNotOwnAnotherOriginsRecord(t *testing.T) {
	isolateSignIns(t)
	signedInAPI, otherAPI := newServer(t), newServer(t)
	saveSignIn(t, signedInAPI, "token-devon", "usr_devon", "Devon Example")
	saveGitServer(t, otherAPI, otherAPI, "an-unset-sageox-token")

	name, email := LedgerAuthor(otherAPI+"/team/ledger.git", "")

	assert.Empty(t, name+email, "a sign-in on another port must not author this server's commits")
}

// Failure prevented: on a deployment whose git server has its own host, an
// OAuth refresh (or a record saved without a bearer hash) drops the Ledger
// author until git credentials are fetched again, and Ledger commits fail on
// a machine without a git identity.
func TestLedgerAuthor_RefreshedLoginKeepsItsGitServer(t *testing.T) {
	const gitServer = "http://source.example.net"
	for name, fetchedWith := range map[string]string{
		"refreshed since the fetch":   "token-before-refresh",
		"saved without a bearer hash": "",
	} {
		t.Run(name, func(t *testing.T) {
			isolateSignIns(t)
			api := newServer(t)
			saveSignIn(t, api, "token-after-refresh", "usr_devon", "Devon Example")
			hash := ""
			if fetchedWith != "" {
				hash = gitserver.BearerTokenFingerprint(fetchedWith)
			}
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(api, gitserver.GitCredentials{
				Token: "pat", ServerURL: gitServer, ExpiresAt: time.Now().Add(time.Hour), BearerTokenHash: hash,
			}))

			_, email := LedgerAuthor(gitServer+"/team/ledger.git", "")

			assert.Equal(t, "usr_devon@sageox-account.invalid", email)
		})
	}
}

// Failure prevented: a git-credential record older ox wrote, with no API
// origin, either loses a distinct git host its own token fetched, or is
// trusted after a refresh when nothing says which server owns it.
func TestLedgerAuthor_LegacyRecordTrustedOnlyForItsOwnToken(t *testing.T) {
	const gitServer = "http://source.example.net"
	for name, tc := range map[string]struct {
		fetchedWith string
		wantEmail   string
	}{
		"fetched with the current token": {"token-current", "usr_devon@sageox-account.invalid"},
		"fetched with an earlier token":  {"token-earlier", ""},
	} {
		t.Run(name, func(t *testing.T) {
			xdg := isolateSignIns(t)
			api := newServer(t)
			saveSignIn(t, api, "token-current", "usr_devon", "Devon Example")
			saveGitServer(t, api, gitServer, tc.fetchedWith)
			stripAPIOrigin(t, xdg)

			_, email := LedgerAuthor(gitServer+"/team/ledger.git", "")

			assert.Equal(t, tc.wantEmail, email)
		})
	}
}

// Failure prevented: an OAuth sign-in that expired (and that a refresh would
// renew) drops the author, so Ledger commits fail on a machine without a git
// identity; or a login stored before user_id existed is not a sign-in.
func TestLedgerAuthor_ExpiredAndLegacyLoginsStillSignIn(t *testing.T) {
	isolateSignIns(t)
	api := newServer(t)
	require.NoError(t, auth.SaveTokenForEndpoint(api, &auth.StoredToken{
		AccessToken: "e30." + jwtSegment(t, map[string]string{"sub": "usr_devon"}) + ".sig",
		ExpiresAt:   time.Now().Add(-time.Hour),
		UserInfo:    auth.UserInfo{Email: "devon@example.com", Name: "Devon Example"},
	}))

	name, email := LedgerAuthor(api+"/team/ledger.git", "")

	assert.Equal(t, "Devon E.", name)
	assert.Equal(t, "usr_devon@sageox-account.invalid", email)
}

// serveTeamToken binds a valid team SAGEOX_TOKEN to a server whose
// introspection answer names coworker verbatim, and returns the server's URL.
func serveTeamToken(t *testing.T, coworker string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":`+coworker+`}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	return srv.URL
}

// Failure prevented: an AI coworker's Ledger commits carry the machine's git
// identity (or fail without one) wherever its git server is not the API's
// own host, because only the API host was ever compared with the clone's.
func TestLedgerAuthor_TeamTokenAuthorsAsItsCoworker(t *testing.T) {
	for name, gitServer := range map[string]string{
		"git server on the API's host": "",
		"git server on its own host":   "http://source.example.net",
	} {
		t.Run(name, func(t *testing.T) {
			isolateSignIns(t)
			api := serveTeamToken(t, `{"id":"agt_rip","display_name":"Rip"}`)
			remote := api + "/team/ledger.git"
			if gitServer != "" {
				saveGitServer(t, api, gitServer, "oxt_test_1ljPfr")
				remote = gitServer + "/team/ledger.git"
			}

			name, email := LedgerAuthor(remote, "a person's display name")

			assert.Equal(t, "Rip <agt_rip@ai-coworker.invalid>", name+" <"+email+">")
		})
	}
}

// Failure prevented: a team token the server names no coworker for authors
// commits as an Anonymous person, under an address built from nothing.
func TestLedgerAuthor_TeamTokenWithoutACoworkerIsNobody(t *testing.T) {
	isolateSignIns(t)
	api := serveTeamToken(t, `null`)

	name, email := LedgerAuthor(api+"/team/ledger.git", "")

	assert.Empty(t, name+email)
}

// Failure prevented: a person's own credential is taken for an AI coworker,
// or a coworker without a display name gets no git name.
func TestCoworkerAuthor(t *testing.T) {
	isolateSignIns(t)
	api := newServer(t)
	saveSignIn(t, api, "token-devon", "usr_devon", "Devon Example")
	name, email := CoworkerAuthor(api)
	assert.Empty(t, name+email, "a signed-in person is not an AI coworker")

	api = serveTeamToken(t, `{"id":"agt_rip"}`)
	name, email = CoworkerAuthor(api)
	assert.Equal(t, "agt_rip <agt_rip@ai-coworker.invalid>", name+" <"+email+">")
}

func jwtSegment(t *testing.T, claims map[string]string) string {
	t.Helper()
	data, err := json.Marshal(claims)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(data)
}

// stripAPIOrigin rewrites every git-credential record under xdg the way older
// ox wrote them, without api_origin.
func stripAPIOrigin(t *testing.T, xdg string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(xdg, "sageox", "git-credentials*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no git-credential record to rewrite")
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		var record map[string]any
		require.NoError(t, json.Unmarshal(data, &record))
		require.Contains(t, record, "api_origin")
		delete(record, "api_origin")
		data, err = json.Marshal(record)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(f, data, 0o600))
	}
}
