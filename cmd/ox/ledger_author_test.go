package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
)

// Failure prevented: on a machine whose git has no identity (a fresh Linux
// container), a signed-in person's session recordings never reach the Ledger
// because every Ledger commit fails. Also: an account signed in on another
// server on the same host authors this server's commits; the account's real
// email lands in shared history; an expired sign-in drops the author; or the
// account keeps authoring after sign-out instead of the machine's git identity.
func TestLedgerCommitAuthor_SignedInAccountThenGitIdentity(t *testing.T) {
	skipIntegration(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "sageox"), 0o755))
	ledgerServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ledgerServer.Close)
	otherServer := httptest.NewServer(http.NotFoundHandler()) // same host, another port
	t.Cleanup(otherServer.Close)
	t.Setenv("SAGEOX_ENDPOINT", ledgerServer.URL)
	t.Setenv("SAGEOX_TOKEN", "")
	require.NoError(t, os.Unsetenv("SAGEOX_TOKEN"))
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "") // restores testenv's value afterwards
		require.NoError(t, os.Unsetenv(k))
	}

	// The machine's git. useConfigOnly stands in for a container, where git
	// cannot guess an email and refuses to commit.
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	const noIdentity = "[user]\n\tuseConfigOnly = true\n"
	const personB = "[user]\n\tname = Person B\n\temail = person-b@example.com\n"
	machineGit := func(config string) {
		require.NoError(t, os.WriteFile(global, []byte(config), 0o600))
	}

	ledgerPath := t.TempDir()
	mustRunGit(t, ledgerPath, "init", "--initial-branch=main")
	mustRunGit(t, ledgerPath, "remote", "add", "origin", ledgerServer.URL+"/team/ledger.git")

	// commitAuthor refreshes the clone as the daemon does before every pull,
	// commits the way Ledger writers do, and returns the commit's author.
	commitAuthor := func(file string) (string, error) {
		require.NoError(t, gitserver.RefreshRemoteCredentials(ledgerPath, endpoint.Get()))
		require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, file), []byte(file), 0o644))
		mustRunGit(t, ledgerPath, "add", file)
		if _, err := gitutil.CommitLedgerSnapshot(context.Background(), ledgerPath, "add "+file, file); err != nil {
			return "", err
		}
		return runIsolatedGit(t, ledgerPath, "log", "-1", "--format=%an <%ae>")
	}
	signIn := func(serverURL, userID, name string, expiresAt time.Time) {
		require.NoError(t, auth.SaveTokenForEndpoint(serverURL, &auth.StoredToken{
			AccessToken: "token-" + userID,
			ExpiresAt:   expiresAt,
			UserInfo:    auth.UserInfo{UserID: userID, Email: userID + "@example.com", Name: name},
		}))
	}

	machineGit(personB)
	signIn(otherServer.URL, "usr_quinn", "Quinn Other", time.Now().Add(time.Hour))
	author, err := commitAuthor("other-server.md")
	require.NoError(t, err)
	assert.Equal(t, "Person B <person-b@example.com>", author, "an account on another server must not author this server's commits")

	machineGit(noIdentity)
	signIn(ledgerServer.URL, "usr_devon", "Devon Example", time.Now().Add(time.Hour))
	author, err = commitAuthor("signed-in.md")
	require.NoError(t, err, "a signed-in account must be able to commit to the Ledger without a git identity")
	assert.Equal(t, "Devon E. <usr_devon@sageox-account.invalid>", author,
		"the Ledger shows the display name and an account address, never the full name or real email")

	signIn(ledgerServer.URL, "usr_devon", "Devon Example", time.Now().Add(-time.Hour))
	author, err = commitAuthor("expired.md")
	require.NoError(t, err, "an expired sign-in is still a sign-in until the person signs out")
	assert.Equal(t, "Devon E. <usr_devon@sageox-account.invalid>", author)

	// A login stored before user_id existed: the account ID is only in the
	// access token's subject.
	legacyJWT := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"usr_devon"}`)) + ".sig"
	require.NoError(t, auth.SaveTokenForEndpoint(ledgerServer.URL, &auth.StoredToken{
		AccessToken: legacyJWT,
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    auth.UserInfo{Email: "usr_devon@example.com", Name: "Devon Example"},
	}))
	author, err = commitAuthor("legacy-login.md")
	require.NoError(t, err, "a login stored before user_id existed is still a sign-in")
	assert.Equal(t, "Devon E. <usr_devon@sageox-account.invalid>", author)

	require.NoError(t, auth.RemoveTokenForEndpoint(ledgerServer.URL))
	machineGit(personB)
	author, err = commitAuthor("signed-out.md")
	require.NoError(t, err)
	assert.Equal(t, "Person B <person-b@example.com>", author, "signed out, the machine's git identity applies again")
}

// isolateSignIns gives a test its own auth store and git-credential records,
// and no SAGEOX_TOKEN.
func isolateSignIns(t *testing.T) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "sageox"), 0o755))
	t.Setenv("SAGEOX_TOKEN", "")
	require.NoError(t, os.Unsetenv("SAGEOX_TOKEN"))
}

func saveSignIn(t *testing.T, serverURL, accessToken, userID, name string) {
	t.Helper()
	require.NoError(t, auth.SaveTokenForEndpoint(serverURL, &auth.StoredToken{
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    auth.UserInfo{UserID: userID, Email: userID + "@example.com", Name: name},
	}))
}

// Failure prevented: two local stacks on one host share a git-credential
// record (records are keyed by host), so the account signed in on one server
// authors the other server's Ledger commits.
func TestSignedInAuthor_SharedCredentialRecordBelongsToItsFetcher(t *testing.T) {
	skipIntegration(t)
	isolateSignIns(t)
	ledgerServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ledgerServer.Close)
	otherServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(otherServer.Close)
	saveSignIn(t, ledgerServer.URL, "token-devon", "usr_devon", "Devon Example")
	saveSignIn(t, otherServer.URL, "token-quinn", "usr_quinn", "Quinn Other")
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(ledgerServer.URL, gitserver.GitCredentials{
		Token: "pat", ServerURL: ledgerServer.URL, ExpiresAt: time.Now().Add(time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("token-devon"),
	}))

	// Sign-ins are visited in random order; enough calls visit Quinn's first.
	for range 32 {
		_, email := identity.SignedInAuthor(ledgerServer.URL+"/team/ledger.git", "")
		require.Equal(t, "usr_devon@sageox-account.invalid", email, "another server's account must not author")
	}
}

// Failure prevented: on a deployment whose git server has its own host, an
// OAuth refresh (or a record saved before bearer hashes existed) drops the
// Ledger author until git credentials are fetched again, and Ledger commits
// fail on a machine without a git identity.
func TestSignedInAuthor_RefreshedLoginKeepsItsGitServer(t *testing.T) {
	skipIntegration(t)
	const gitServer = "http://source.example.net"
	for name, fetchedWith := range map[string]string{
		"refreshed since the fetch":  gitserver.BearerTokenFingerprint("token-before-refresh"),
		"saved before bearer hashes": "",
	} {
		t.Run(name, func(t *testing.T) {
			isolateSignIns(t)
			api := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(api.Close)
			saveSignIn(t, api.URL, "token-after-refresh", "usr_devon", "Devon Example")
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(api.URL, gitserver.GitCredentials{
				Token: "pat", ServerURL: gitServer, ExpiresAt: time.Now().Add(time.Hour), BearerTokenHash: fetchedWith,
			}))

			author, email := identity.SignedInAuthor(gitServer+"/team/ledger.git", "")

			assert.Equal(t, "Devon E. <usr_devon@sageox-account.invalid>", author+" <"+email+">")
		})
	}
}
