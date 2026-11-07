package main

import (
	"context"
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
)

// Failure prevented: on a machine whose git has no identity (a fresh Linux
// container), a signed-in person's session recordings never reach the Ledger
// because every Ledger commit fails; or their commits keep the SageOx account
// after sign-out instead of returning to the machine's git identity.
func TestLedgerCommitAuthor_SignedInAccountThenGitIdentity(t *testing.T) {
	skipIntegration(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "sageox"), 0o755))
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "")
	require.NoError(t, os.Unsetenv("SAGEOX_TOKEN"))

	// The machine's git: useConfigOnly stands in for a container, where git
	// cannot guess an email and refuses to commit.
	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte("[user]\n\tuseConfigOnly = true\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "") // restores testenv's value afterwards
		require.NoError(t, os.Unsetenv(k))
	}

	ledgerPath := t.TempDir()
	mustRunGit(t, ledgerPath, "init", "--initial-branch=main")
	mustRunGit(t, ledgerPath, "remote", "add", "origin", srv.URL+"/team/ledger.git")

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

	require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
		AccessToken: "test-access-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    auth.UserInfo{UserID: "usr_devon", Email: "devon@example.com", Name: "Devon Example"},
	}))
	author, err := commitAuthor("signed-in.md")
	require.NoError(t, err, "a signed-in account must be able to commit to the Ledger without a git identity")
	assert.Equal(t, "Devon E. <devon@example.com>", author, "the Ledger shows the account's display name, not its full name")

	require.NoError(t, auth.RemoveTokenForEndpoint(srv.URL))
	require.NoError(t, os.WriteFile(global, []byte("[user]\n\tname = Person B\n\temail = person-b@example.com\n"), 0o600))
	author, err = commitAuthor("signed-out.md")
	require.NoError(t, err)
	assert.Equal(t, "Person B <person-b@example.com>", author, "signed out, the machine's git identity applies again")
}
