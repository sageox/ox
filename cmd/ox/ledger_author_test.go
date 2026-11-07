package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
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

// Failure prevented: an AI coworker on a machine whose git has no identity (a
// fresh container) cannot commit its work in the project, or commits it as the
// machine; its identity outlives its team token; or a person's own identity in
// their repository is replaced.
func TestRunAgentPrime_AICoworkerCommitsItsWorkAsItself(t *testing.T) {
	commitAuthor := func(t *testing.T, repo string) (string, error) {
		t.Helper()
		// hooksPath off: ox init's hooks would run whatever ox is on PATH
		if out, err := exec.Command("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "work").CombinedOutput(); err != nil {
			return "", fmt.Errorf("%w: %s", err, out)
		}
		out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%an <%ae>").Output()
		return strings.TrimSpace(string(out)), err
	}
	for _, tc := range []struct {
		name         string
		repoIdentity bool // whether the repo keeps the person's own local identity
		want         string
	}{
		{"a container's repo", false, "Rip <agt_rip@ai-coworker.invalid>"},
		{"a person's repo", true, "E2E <e2e@example.com>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := initializedE2E(t)
			global := filepath.Join(t.TempDir(), "gitconfig")
			require.NoError(t, os.WriteFile(global, []byte("[user]\n\tuseConfigOnly = true\n"), 0o600))
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
				t.Setenv(k, "")
				require.NoError(t, os.Unsetenv(k))
			}
			if !tc.repoIdentity {
				mustRunGit(t, env.Root, "config", "--unset", "user.name")
				mustRunGit(t, env.Root, "config", "--unset", "user.email")
			}
			env.Handle(auth.IntrospectEndpoint, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":{"id":"agt_rip","display_name":"Rip"}}`)
			})
			t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")

			runPrimeJSON(t)
			author, err := commitAuthor(t, env.Root)
			require.NoError(t, err, "the AI coworker must be able to commit its work")
			assert.Equal(t, tc.want, author)

			t.Setenv("SAGEOX_TOKEN", "")
			require.NoError(t, os.Unsetenv("SAGEOX_TOKEN"))
			runPrimeJSON(t)
			author, err = commitAuthor(t, env.Root)
			if tc.repoIdentity {
				require.NoError(t, err)
				assert.Equal(t, tc.want, author, "a person's own identity survives")
				return
			}
			assert.Error(t, err, "without the team token, the coworker's identity is gone and git has none: %s", author)
		})
	}
}
