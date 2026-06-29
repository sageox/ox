package gitserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialHelperArgs verifies the shared credential-helper argv used by
// both the ledger full-clone and the team-context two-phase clone.
// Failure prevented: one clone path drifts and stops supplying credentials,
// reintroducing the non-interactive username prompt.
func TestCredentialHelperArgs(t *testing.T) {
	orig := DefaultHelperCommand()
	t.Cleanup(func() { SetHelperCommand(orig) })
	SetHelperCommand("!ox git-credential-helper")
	args := CredentialHelperArgs()

	// exactly: clear inherited helpers, then install the ox helper
	assert.Equal(t, []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=!ox git-credential-helper",
	}, args)
}

// gitRepoWithBrokenSigning builds a real git repo whose local config enables
// SSH commit signing with a signing key that can't be used non-interactively
// — the exact state that wedges a ledger: commit dies with "failed to write
// commit object" because the signing key passphrase prompt has no TTY.
func gitRepoWithBrokenSigning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir // never mutate the real repo's identity/config
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init")
	run("config", "--local", "user.name", "Test")
	run("config", "--local", "user.email", "test@example.com")
	// point at a signing key that forces a passphrase/agent interaction that
	// fails in this headless test — reproduces the production wedge.
	bogusKey := filepath.Join(dir, "nonexistent_signing_key")
	run("config", "--local", "gpg.format", "ssh")
	run("config", "--local", "user.signingkey", bogusKey)
	run("config", "--local", "commit.gpgsign", "true")
	return dir
}

func tryCommit(t *testing.T, dir string, extraArgs ...string) error {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	require.NoError(t, add.Run())
	args := append(append([]string{}, extraArgs...), "commit", "-m", "probe")
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// TestDisableCommitSigning_UnwedgesSignedRepo proves the recovery contract for
// the whole class of "ox-managed repo inherited the user's commit signing and
// can't commit non-interactively". Failure prevented: ledger/team commits die
// with "failed to write commit object", sessions stage but never sync.
func TestDisableCommitSigning_UnwedgesSignedRepo(t *testing.T) {
	dir := gitRepoWithBrokenSigning(t)

	// Sanity: with signing on, the commit genuinely fails. If this ever
	// starts passing, the fixture no longer reproduces the bug.
	require.Error(t, tryCommit(t, dir), "expected signed commit to fail in headless env")

	changed, err := DisableCommitSigning(dir)
	require.NoError(t, err)
	assert.True(t, changed, "first disable should mutate config")

	// Now the same commit succeeds — the wedge is cleared persistently.
	require.NoError(t, tryCommit(t, dir), "commit should succeed after signing disabled")

	got, err := readGitConfig(dir, "commit.gpgsign")
	require.NoError(t, err)
	assert.Equal(t, "false", got)

	// Idempotent: a second call is a no-op (no further mutation).
	changed, err = DisableCommitSigning(dir)
	require.NoError(t, err)
	assert.False(t, changed, "second disable should be a no-op")
}

// gitRepoWithInheritedSigning builds a repo with NO local signing config whose
// signing is enabled purely through an inherited global config (GIT_CONFIG_GLOBAL
// pointed at a temp file) — the actual production wedge, where the user's
// ~/.config/git/config sets commit.gpgsign=true and managed repos inherit it.
// Returns the repo path; the caller's git subprocesses inherit the temp global
// via t.Setenv, so DisableCommitSigning sees the inherited "true".
func gitRepoWithInheritedSigning(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	globalCfg := filepath.Join(home, "global.gitconfig")
	// isolate from the real machine config for every git subprocess in this test
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(home, "no-system"))

	setGlobal := func(k, v string) {
		cmd := exec.Command("git", "config", "--file", globalCfg, k, v)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git config --file: %s", out)
	}
	setGlobal("gpg.format", "ssh")
	setGlobal("user.signingkey", filepath.Join(home, "nope"))
	setGlobal("commit.gpgsign", "true")

	dir := t.TempDir()
	for _, args := range [][]string{
		{"-C", dir, "init"},
		{"-C", dir, "config", "--local", "user.name", "Test"},
		{"-C", dir, "config", "--local", "user.email", "test@example.com"},
	} {
		require.NoError(t, exec.Command("git", args...).Run())
	}
	return dir
}

// TestDisableCommitSigning_PersistsLocalDespiteInheritedConfig is the core
// regression for the CodeRabbit major finding: the skip check must read the
// repo-LOCAL value, not the merged value. With signing enabled via inherited
// global config, DisableCommitSigning must still write a local "false" so the
// repair is durable against a later global change. Failure prevented: a managed
// repo left unprotected because the merged read saw an inherited value.
func TestDisableCommitSigning_PersistsLocalDespiteInheritedConfig(t *testing.T) {
	dir := gitRepoWithInheritedSigning(t)

	// merged read sees the inherited "true"; local read sees no override yet.
	merged, err := readGitConfig(dir, "commit.gpgsign")
	require.NoError(t, err)
	assert.Equal(t, "true", merged, "inherited global signing should be visible via merged read")
	local, err := readGitConfigLocal(dir, "commit.gpgsign")
	require.NoError(t, err)
	assert.Empty(t, local, "repo must start with no local override")

	// sanity: a raw commit fails because it inherits the (unusable) signing key
	require.Error(t, tryCommit(t, dir), "inherited signing should fail the commit")

	changed, err := DisableCommitSigning(dir)
	require.NoError(t, err)
	assert.True(t, changed, "must persist a local override even when merged config already reads true")

	local, err = readGitConfigLocal(dir, "commit.gpgsign")
	require.NoError(t, err)
	assert.Equal(t, "false", local, "local override must be written so the repair survives global changes")
	require.NoError(t, tryCommit(t, dir), "commit should succeed after local signing disabled")
}

// TestMigrateLedgerCredentials_DisablesSigningWithoutRemote proves the
// self-heal fires for repos that have no migratable https origin (the early
// return paths) — signing must still be disabled so a freshly-set-up or
// SSH-origin ledger isn't left wedged.
func TestMigrateLedgerCredentials_DisablesSigningWithoutRemote(t *testing.T) {
	dir := gitRepoWithBrokenSigning(t) // no origin remote configured

	changed, err := MigrateLedgerCredentials(dir, "!ox git-credential-helper")
	require.NoError(t, err)
	assert.True(t, changed, "signing change should be reported even with no remote")

	got, err := readGitConfig(dir, "commit.gpgsign")
	require.NoError(t, err)
	assert.Equal(t, "false", got)
	require.NoError(t, tryCommit(t, dir), "commit should succeed post-migrate")
}
