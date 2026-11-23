package gitserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withGlobalIdentityOverride gives git a global config that sets author.* and
// committer.*, which outrank user.name and user.email in every scope.
func withGlobalIdentityOverride(t *testing.T) {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte("[author]\n\tname = Global Person\n\temail = global@example.com\n[committer]\n\tname = Global Person\n\temail = global@example.com\n"), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	for _, v := range identityEnvVarNames {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
}

var identityEnvVarNames = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

func commitIdentity(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "commit", "--allow-empty", "--quiet", "--no-gpg-sign", "-m", "x").CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(got))
}

// Failure prevented: on a machine whose global config sets author.email or
// committer.email, ox's Ledger commits carry that address instead of the AI
// teammate's, because those keys outrank the stamped local user.email.
func TestMigrateLedgerCredentials_StampOutranksAGlobalAuthorEmail(t *testing.T) {
	withGlobalIdentityOverride(t)
	dir := ledgerCloneOf(t, "https://git.sageox.ai/team/ledger.git", coworkerAuthor)

	_, err := MigrateLedgerCredentials(dir, "!ox git-credential-helper")
	require.NoError(t, err)
	want := stampOf(coworkerAuthor)
	assert.Equal(t, want+"|"+want, commitIdentity(t, dir))

	// Removing the stamp removes the overrides ox wrote with it.
	LedgerAuthorGetter = func(string) (string, string) { return "", "" }
	_, err = MigrateLedgerCredentials(dir, "!ox git-credential-helper")
	require.NoError(t, err)
	for _, key := range identityOverrideKeys {
		v, err := readGitConfigLocal(dir, key)
		require.NoError(t, err)
		assert.Empty(t, v, key)
	}
	assert.Equal(t, "Global Person <global@example.com>|Global Person <global@example.com>", commitIdentity(t, dir))
}

// Failure prevented: ox overwrites a local author.email a person set in their
// own repository, or the stamp in a stamped repository stops matching after a
// rename because an old override outlives it.
func TestStampProjectAuthor_Overrides(t *testing.T) {
	withGlobalIdentityOverride(t)

	t.Run("a person's local author.email is theirs", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
		require.NoError(t, exec.Command("git", "-C", dir, "config", "author.email", "mine@example.com").Run())
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		v, err := readGitConfigLocal(dir, "author.email")
		require.NoError(t, err)
		assert.Equal(t, "mine@example.com", v)
		assert.Equal(t, [2]string{}, localIdentity(t, dir))
	})

	t.Run("a person's replacement of the stamp drops the overrides", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		setLocalIdentity(t, dir, personAuthor, "")
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		for _, key := range identityOverrideKeys {
			v, err := readGitConfigLocal(dir, key)
			require.NoError(t, err)
			assert.Empty(t, v, key)
		}
		assert.Equal(t, personAuthor, localIdentity(t, dir))
	})

	t.Run("a person's edit of only author.email is kept", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		require.NoError(t, exec.Command("git", "-C", dir, "config", "author.email", "mine@example.com").Run())
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		v, err := readGitConfigLocal(dir, "author.email")
		require.NoError(t, err)
		assert.Equal(t, "mine@example.com", v)
	})

	t.Run("a rename replaces the overrides", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
		old := [2]string{"Old Rip", "agt_old@ai-coworker.invalid"}
		require.NoError(t, StampProjectAuthor(dir, old[0], old[1]))
		require.NoError(t, StampProjectAuthor(dir, coworkerAuthor[0], coworkerAuthor[1]))
		want := stampOf(coworkerAuthor)
		assert.Equal(t, want+"|"+want, commitIdentity(t, dir))
	})
}

// Failure prevented: a config write that fails while ox clears or rewrites its
// overrides is swallowed, or leaves a state the next call cannot finish.
func TestStampAuthor_OverrideWriteFailuresAreReported(t *testing.T) {
	oldAuthor := [2]string{"Old Rip", "agt_old@ai-coworker.invalid"}
	for _, tc := range []struct {
		name    string
		person  bool      // a person replaces user.* after the first stamp
		then    [2]string // the second stamp call's identity
		project bool
	}{
		{"person replaces the stamp", true, coworkerAuthor, true},
		{"removing the stamp", false, [2]string{}, true},
		{"replacing its own stamp", false, coworkerAuthor, true},
		{"an empty name drops the name overrides", false, [2]string{"", coworkerAuthor[1]}, false},
	} {
		for failAt := 1; ; failAt++ {
			dir := t.TempDir()
			require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
			require.NoError(t, stampAuthor(dir, oldAuthor[0], oldAuthor[1], true))
			if tc.person {
				setLocalIdentity(t, dir, personAuthor, "")
			}
			writes := 0
			failConfigWrite = func() error {
				if writes++; writes == failAt {
					return errors.New("interrupted")
				}
				return nil
			}
			interrupted := stampAuthor(dir, tc.then[0], tc.then[1], !tc.project) != nil
			failConfigWrite = nil
			require.Equal(t, writes >= failAt, interrupted, "%s: a failed write at %d must be reported", tc.name, failAt)
			require.NoError(t, stampAuthor(dir, tc.then[0], tc.then[1], !tc.project), "%s, interrupted at write %d", tc.name, failAt)

			wantIdentity, wantOverrides := tc.then, tc.then
			if tc.person {
				wantIdentity, wantOverrides = personAuthor, [2]string{}
			}
			assert.Equal(t, wantIdentity, localIdentity(t, dir), "%s, interrupted at write %d", tc.name, failAt)
			for _, key := range identityOverrideKeys {
				v, err := readGitConfigLocal(dir, key)
				require.NoError(t, err)
				assert.Equal(t, overrideValue(key, wantOverrides[0], wantOverrides[1]), v, "%s: %s, interrupted at write %d", tc.name, key, failAt)
			}
			if !interrupted {
				break // failAt is past the last write: every step has been interrupted
			}
		}
	}
}

// Failure prevented: a malformed stamp is read as an identity ox wrote.
func TestParseStamp_RejectsMalformed(t *testing.T) {
	for _, s := range []string{"true", "no-email", "Rip <agt_rip"} {
		_, _, ok := parseStamp(s)
		assert.False(t, ok, s)
	}
	name, email, ok := parseStamp(stampOf(coworkerAuthor))
	assert.True(t, ok)
	assert.Equal(t, coworkerAuthor, [2]string{name, email})
}
