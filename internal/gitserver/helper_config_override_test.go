package gitserver

import (
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
// coworker's, because those keys outrank the stamped local user.email.
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
