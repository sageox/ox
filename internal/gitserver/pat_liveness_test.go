package gitserver

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePATLiveness_NilCreds(t *testing.T) {
	result := ValidatePATLiveness(context.Background(), nil)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_EmptyToken(t *testing.T) {
	creds := &GitCredentials{Token: ""}
	result := ValidatePATLiveness(context.Background(), creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_NoRepos(t *testing.T) {
	creds := &GitCredentials{Token: "some-token"}
	result := ValidatePATLiveness(context.Background(), creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no repo URL")
}

func TestValidatePATLiveness_Timeout(t *testing.T) {
	// unreachable host — context timeout should cause skip
	creds := &GitCredentials{
		Token: "some-token",
		Repos: map[string]RepoEntry{
			"test": {URL: "https://192.0.2.1:1/test/repo.git"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := ValidatePATLiveness(ctx, creds)
	assert.True(t, result.Skipped, "should skip on timeout")
	assert.Contains(t, result.Reason, "network unreachable")
}

func TestPickProbeRepoURL(t *testing.T) {
	tests := []struct {
		name  string
		creds *GitCredentials
		want  string
	}{
		{
			name:  "nil repos",
			creds: &GitCredentials{},
			want:  "",
		},
		{
			name: "empty repos map",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{},
			},
			want: "",
		},
		{
			name: "picks first available URL",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{
					"team1": {URL: "https://git.sageox.ai/team1/repo.git"},
				},
			},
			want: "https://git.sageox.ai/team1/repo.git",
		},
		{
			name: "skips empty URLs",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{
					"empty": {URL: ""},
					"valid": {URL: "https://git.sageox.ai/valid/repo.git"},
				},
			},
			want: "https://git.sageox.ai/valid/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickProbeRepoURL(tt.creds)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildProbeURL(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    string
		wantErr bool
	}{
		{
			name:    "https URL gets oauth2 user",
			repoURL: "https://git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			name:    "http URL with port",
			repoURL: "http://localhost:3000/team/repo.git",
			want:    "http://oauth2@localhost:3000/team/repo.git",
		},
		{
			name:    "bare URL gets https scheme",
			repoURL: "git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			// an API response that embeds a password in the repo URL must not
			// leak through the probe — buildProbeURL replaces userinfo with the
			// bare oauth2 username (token comes via GIT_ASKPASS, never the URL)
			name:    "drops injected userinfo password",
			repoURL: "https://attacker:secret@git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			name:    "replaces pre-existing username",
			repoURL: "https://olduser@git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			name:    "invalid URL",
			repoURL: "://not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProbeURL(tt.repoURL)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			gotURL, _ := url.Parse(got)
			wantURL, _ := url.Parse(tt.want)
			assert.Equal(t, wantURL.Scheme, gotURL.Scheme)
			assert.Equal(t, wantURL.Host, gotURL.Host)
			assert.Equal(t, wantURL.Path, gotURL.Path)
			assert.Equal(t, wantURL.User.Username(), gotURL.User.Username())
			// no password — token is via GIT_ASKPASS
			_, hasPass := gotURL.User.Password()
			assert.False(t, hasPass, "token should not be in URL")
		})
	}
}

// TestValidatePATLiveness_CredentialHelperSuppressed verifies that the liveness
// probe ignores system credential helpers (osxkeychain, libsecret, wincred, etc.)
// and uses only the token from GitCredentials via GIT_ASKPASS.
//
// The bug scenario: a user has a stale credential for git.sageox.ai stored in
// their OS keychain from a previous install. git's credential helper returns
// those stale creds before GIT_ASKPASS is ever consulted, causing a 401 and
// a false "PAT rejected" error even though the stored token is valid.
//
// The fix: set GIT_CONFIG_COUNT env vars to override credential.helper to
// empty for the subprocess, bypassing all helpers so only GIT_ASKPASS is used.
//
// Testing approach: inject a fake "git" binary (via a wrapper script) that
// simulates the helper-interference failure — it exits 128 with "401 Unauthorized"
// UNLESS GIT_CONFIG_COUNT is set suppressing the helper, in which case it
// exits 0 (success). This makes the test fail on the old code and pass on the fix.
func TestValidatePATLiveness_CredentialHelperSuppressed(t *testing.T) {
	// write a fake "git" script that simulates credential helper interference:
	// if GIT_CONFIG_COUNT is NOT set, the "helper" has overridden credentials
	// and the server returns 401; if it IS set, the helper was suppressed and
	// GIT_ASKPASS provided the right token, so the probe succeeds (0 refs, but exit 0)
	fakeGit, err := os.CreateTemp("", "fake-git-*")
	require.NoError(t, err)
	defer os.Remove(fakeGit.Name())

	_, err = fakeGit.WriteString(`#!/bin/sh
if [ -z "$GIT_CONFIG_COUNT" ]; then
  # credential helper was not suppressed — simulate 401 from stale keychain creds
  echo "fatal: unable to access 'https://git.sageox.ai/...': The requested URL returned error: 401" >&2
  exit 128
fi
# credential helper suppressed — GIT_ASKPASS provided our token, probe succeeds
exit 0
`)
	require.NoError(t, err)
	fakeGit.Close()
	require.NoError(t, os.Chmod(fakeGit.Name(), 0700))

	// put our fake git first in PATH
	fakeDir := t.TempDir()
	fakePath := fakeDir + "/git"
	require.NoError(t, os.Symlink(fakeGit.Name(), fakePath))
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	creds := &GitCredentials{
		Token: "valid-token",
		Repos: map[string]RepoEntry{
			"team": {URL: "https://git.sageox.ai/team/repo.git"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := ValidatePATLiveness(ctx, creds)

	// with GIT_CONFIG_COUNT suppression: fake git exits 0 → Valid=true
	// without it: fake git exits 128 with 401 → Valid=false (the bug)
	assert.False(t, result.Skipped, "should not be skipped")
	assert.True(t, result.Valid, "should succeed when credential helper is suppressed; got reason: %s", result.Reason)
}

func TestClearCredentialHelperEntry(t *testing.T) {
	t.Run("no-op on invalid URL", func(t *testing.T) {
		// should not panic or error
		ClearCredentialHelperEntry("")
		ClearCredentialHelperEntry("://not-a-url")
	})

	t.Run("runs git credential reject for valid URL", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short: flaky under high parallelism due to 3s internal timeout")
		}

		// inject a fake git that records whether it was called with "credential reject"
		fakeGit, err := os.CreateTemp("", "fake-git-cred-*")
		require.NoError(t, err)
		defer os.Remove(fakeGit.Name())

		recordFile := t.TempDir() + "/called"
		_, err = fakeGit.WriteString("#!/bin/sh\n" +
			"if [ \"$1\" = 'credential' ] && [ \"$2\" = 'reject' ]; then\n" +
			"  touch " + recordFile + "\n" +
			"fi\n" +
			"exit 0\n")
		require.NoError(t, err)
		fakeGit.Close()
		require.NoError(t, os.Chmod(fakeGit.Name(), 0700))

		fakeDir := t.TempDir()
		require.NoError(t, os.Symlink(fakeGit.Name(), fakeDir+"/git"))
		t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

		ClearCredentialHelperEntry("https://git.sageox.ai")

		_, statErr := os.Stat(recordFile)
		assert.False(t, os.IsNotExist(statErr), "expected git credential reject to be called")
	})
}

func TestWriteAskpassScript(t *testing.T) {
	path, err := writeAskpassScript("test-token-123")
	require.NoError(t, err)
	defer os.Remove(path)

	// verify file exists and is executable
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0100, "script should be executable")

	// verify content echoes the token
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test-token-123")
}

// TestWriteAskpassScript_EscapesAdversarialTokens verifies the askpass script
// treats a token as inert data: a hostile token value can neither break out of
// the single quotes to execute shell commands nor be mangled before it reaches
// git. The script's escaping is the only thing standing between an attacker-
// controlled (or simply weird) token and shell execution, so it gets exercised
// with the full range of metacharacters.
//
// Failure prevented: a token containing shell metacharacters ('; $(), backticks,
// pipes) injecting command execution into the liveness probe, or echo/printf
// corrupting the token so a valid PAT is read as rejected.
func TestWriteAskpassScript_EscapesAdversarialTokens(t *testing.T) {
	tokens := []struct {
		name  string
		token string
	}{
		{"quote break then injection", `'; touch PWNED; echo '`},
		{"command substitution", `$(touch PWNED)`},
		{"backtick substitution", "`touch PWNED`"},
		{"and operator", `tok && touch PWNED`},
		{"pipe operator", `tok | tee PWNED`},
		{"embedded single quote", `ab'cd`},
		{"real newline", "line1\nline2"},
		{"literal backslash n", `line1\nline2`},
		{"variable expansion", `$HOME`},
		{"braced variable", `${PATH}`},
		{"surrounding whitespace", `  pad  `},
	}

	for _, tt := range tokens {
		t.Run(tt.name, func(t *testing.T) {
			// run the script with its working dir inside a clean temp dir so an
			// injected `touch PWNED` would land here and be detectable
			dir := t.TempDir()

			path, err := writeAskpassScript(tt.token)
			require.NoError(t, err)
			defer os.Remove(path)

			cmd := exec.Command("/bin/sh", path)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "askpass script must run cleanly; output: %q", string(out))

			// printf adds exactly one trailing newline — strip only that
			got := strings.TrimSuffix(string(out), "\n")
			assert.Equal(t, tt.token, got, "token must round-trip verbatim — no injection, no mangling")

			// nothing should have been created in the working dir; a stray file
			// means a metacharacter executed as a command
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "no side-effect files: token must not execute as shell")
		})
	}
}

// TestValidatePATLiveness_NonAuthFailureNotMisreadAsRejected verifies a git
// failure that merely contains a digit string like "401" (in a repo path or a
// DNS error) is classified as a network skip, not a revoked PAT.
//
// Failure prevented: ox tells the user to re-run `ox login` for a perfectly
// valid token because the server was unreachable and the URL happened to
// contain "401".
func TestValidatePATLiveness_NonAuthFailureNotMisreadAsRejected(t *testing.T) {
	// non-auth git failures whose output coincidentally contains a "401"/"403"
	// (in a repo path, or alongside a generic "error") must classify as a network
	// skip — never a revoked PAT that sends the user back through `ox login`.
	tests := []struct {
		name      string
		gitStderr string
	}{
		{
			name:      "dns failure with 401 in repo path",
			gitStderr: "fatal: unable to access 'https://git.sageox.ai/team/project-401-archive.git/': Could not resolve host: git.sageox.ai",
		},
		{
			// the class CodeRabbit flagged: "401" in a path PLUS a bare "error"
			// word must not be enough to reject — only an auth-specific signal is
			name:      "tls failure with 401 in path and generic error word",
			gitStderr: "fatal: unable to access 'https://git.sageox.ai/team/repo-401.git/': error: SSL certificate problem: unable to get local issuer certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeGit(t, `echo "`+tt.gitStderr+`" >&2
exit 128`)

			creds := &GitCredentials{
				Token: "valid-token",
				Repos: map[string]RepoEntry{"team": {URL: "https://git.sageox.ai/team/repo.git"}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			result := ValidatePATLiveness(ctx, creds)
			assert.True(t, result.Skipped, "non-auth failure must skip, not reject; reason: %s", result.Reason)
			assert.False(t, result.Valid)
		})
	}
}

// TestValidatePATLiveness_RealAuthFailureRejected guards the other side of the
// tightened matcher: a genuine 401 from the git server must still be reported as
// a rejected PAT, not silently skipped.
//
// Failure prevented: over-tightening the auth-failure matcher so a revoked token
// looks like a transient network blip and the user is never told to re-login.
func TestValidatePATLiveness_RealAuthFailureRejected(t *testing.T) {
	installFakeGit(t, `echo "fatal: unable to access 'https://git.sageox.ai/team/repo.git/': The requested URL returned error: 401" >&2
exit 128`)

	creds := &GitCredentials{
		Token: "revoked-token",
		Repos: map[string]RepoEntry{"team": {URL: "https://git.sageox.ai/team/repo.git"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := ValidatePATLiveness(ctx, creds)
	assert.False(t, result.Skipped, "real 401 must not be skipped; reason: %s", result.Reason)
	assert.False(t, result.Valid, "real 401 must be reported as rejected")
	assert.Contains(t, result.Reason, "rejected")
}

// installFakeGit puts a fake `git` binary (running the given /bin/sh script body)
// at the front of PATH for the duration of the test, so liveness classification
// can be exercised against specific exit codes and messages without a real
// network call. Mirrors the inline pattern used by
// TestValidatePATLiveness_CredentialHelperSuppressed.
func installFakeGit(t *testing.T, scriptBody string) {
	t.Helper()
	fakeGit, err := os.CreateTemp("", "fake-git-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(fakeGit.Name()) })

	_, err = fakeGit.WriteString("#!/bin/sh\n" + scriptBody + "\n")
	require.NoError(t, err)
	require.NoError(t, fakeGit.Close())
	require.NoError(t, os.Chmod(fakeGit.Name(), 0700))

	fakeDir := t.TempDir()
	require.NoError(t, os.Symlink(fakeGit.Name(), fakeDir+"/git"))
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
}
