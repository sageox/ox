package gitserver

import (
	"errors"
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

// ledgerCloneOf returns a repo whose origin is the Ledger URL remoteURL, with
// LedgerAuthorGetter reporting author for it.
func ledgerCloneOf(t *testing.T, remoteURL string, author [2]string) string {
	t.Helper()
	orig := LedgerAuthorGetter
	t.Cleanup(func() { LedgerAuthorGetter = orig })
	LedgerAuthorGetter = func(got string) (string, string) {
		if got != remoteURL {
			return "", ""
		}
		return author[0], author[1]
	}

	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", remoteURL}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return dir
}

func localIdentity(t *testing.T, dir string) [2]string {
	t.Helper()
	var id [2]string
	for i, key := range []string{"user.name", "user.email"} {
		v, err := readGitConfigLocal(dir, key)
		require.NoError(t, err)
		id[i] = v
	}
	return id
}

// setLocalIdentity writes before as dir's local user.name and user.email, and
// stamp as ox's stamp when it is not "".
func setLocalIdentity(t *testing.T, dir string, before [2]string, stamp string) {
	t.Helper()
	for i, key := range []string{"user.name", "user.email"} {
		if before[i] != "" {
			require.NoError(t, exec.Command("git", "-C", dir, "config", key, before[i]).Run())
		}
	}
	if stamp != "" {
		require.NoError(t, exec.Command("git", "-C", dir, "config", authorStampKey, stamp).Run())
	}
}

// stampOf is the stamp ox leaves for the identity id.
func stampOf(id [2]string) string { return stampValue(id[0], id[1]) }

// Failure prevented: an AI coworker's git address changes shape, so ox no
// longer recognizes the stamps it wrote and never removes them.
func TestCoworkerEmail_EndsInTheStampDomain(t *testing.T) {
	assert.Equal(t, "agt_rip@ai-coworker.invalid", CoworkerEmail("agt_rip"))
}

var (
	coworkerAuthor = [2]string{"Rip", "agt_rip@ai-coworker.invalid"}
	accountAuthor  = [2]string{"Devon E.", "usr_devon@sageox-account.invalid"}
	personAuthor   = [2]string{"Person B", "person-b@example.com"}
)

// Failure prevented: a Ledger commit carries the machine's git identity
// instead of the coworker or account ox resolved, a renamed author keeps its
// old name, a stamp outlives its token or sign-in, or removing one strips an
// identity a person set or their other user settings.
func TestMigrateLedgerCredentials_StampsTheLedgerAuthor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		author [2]string // what LedgerAuthorGetter reports
		before [2]string // the clone's own user.name and user.email
		stamp  string    // ox's stamp in the clone, or ""
		want   [2]string
	}{
		{"stamps the coworker", coworkerAuthor, [2]string{}, "", coworkerAuthor},
		{"stamps the account", accountAuthor, [2]string{}, "", accountAuthor},
		{"a renamed author replaces its old stamp", coworkerAuthor, [2]string{"Old Rip", coworkerAuthor[1]}, "", coworkerAuthor},
		{"replaces a person's identity in ox's clone", accountAuthor, personAuthor, "", accountAuthor},
		{"replaces a person's edit of ox's stamp", accountAuthor, personAuthor, stampOf(accountAuthor), accountAuthor},
		{"no author removes the stamp", [2]string{}, accountAuthor, stampOf(accountAuthor), [2]string{}},
		{"no author removes a stamp that did not record its identity", [2]string{}, accountAuthor, "true", [2]string{}},
		{"no author removes a stamp from before the marker", [2]string{}, coworkerAuthor, "", [2]string{}},
		{"no author finishes a half-removed stamp", [2]string{}, [2]string{}, stampOf(accountAuthor), [2]string{}},
		{"no author finishes a half-removed old stamp", [2]string{}, [2]string{"", coworkerAuthor[1]}, "", [2]string{}},
		{"no author keeps a person's identity", [2]string{}, personAuthor, "", personAuthor},
		{"no author keeps a person's edit of ox's stamp", [2]string{}, personAuthor, stampOf(accountAuthor), personAuthor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := ledgerCloneOf(t, "https://git.sageox.ai/team/ledger.git", tc.author)
			require.NoError(t, exec.Command("git", "-C", dir, "config", "user.signingkey", "keep").Run())
			setLocalIdentity(t, dir, tc.before, tc.stamp)

			_, err := MigrateLedgerCredentials(dir, "!ox git-credential-helper")
			require.NoError(t, err)
			assert.Equal(t, tc.want, localIdentity(t, dir))
			stamp, err := readGitConfigLocal(dir, authorStampKey)
			require.NoError(t, err)
			wantStamp := ""
			if tc.author != [2]string{} {
				wantStamp = stampOf(tc.author)
			}
			assert.Equal(t, wantStamp, stamp, "the stamp must name exactly the identity ox wrote")
			signingKey, err := readGitConfigLocal(dir, "user.signingkey")
			require.NoError(t, err)
			assert.Equal(t, "keep", signingKey, "only user.name and user.email are ox's to change")
		})
	}
}

// Failure prevented: an AI coworker's commits in a person's repository fail on
// a machine without a git identity or carry the machine's identity; or ox
// overwrites or deletes an identity the person set in their own repository,
// including one they set over an identity ox stamped.
func TestStampProjectAuthor_NeverTouchesAPersonsIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		author [2]string
		before [2]string
		stamp  string
		want   [2]string
	}{
		{"stamps the coworker", coworkerAuthor, [2]string{}, "", coworkerAuthor},
		{"replaces its own earlier stamp", coworkerAuthor, [2]string{"Old Rip", "agt_old@ai-coworker.invalid"}, stampOf([2]string{"Old Rip", "agt_old@ai-coworker.invalid"}), coworkerAuthor},
		{"keeps a person's identity", coworkerAuthor, personAuthor, "", personAuthor},
		{"keeps a person's partial identity", coworkerAuthor, [2]string{personAuthor[0], ""}, "", [2]string{personAuthor[0], ""}},
		{"keeps a person's edit of its stamp", coworkerAuthor, personAuthor, stampOf(coworkerAuthor), personAuthor},
		{"keeps a person's edit of only the name", coworkerAuthor, [2]string{personAuthor[0], coworkerAuthor[1]}, stampOf(coworkerAuthor), [2]string{personAuthor[0], coworkerAuthor[1]}},
		{"no coworker removes its stamp", [2]string{}, coworkerAuthor, stampOf(coworkerAuthor), [2]string{}},
		{"no coworker keeps a person's identity", [2]string{}, personAuthor, "", personAuthor},
		{"no coworker keeps a person's edit of its stamp", [2]string{}, personAuthor, stampOf(coworkerAuthor), personAuthor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
			setLocalIdentity(t, dir, tc.before, tc.stamp)

			require.NoError(t, StampProjectAuthor(dir, tc.author[0], tc.author[1]))
			assert.Equal(t, tc.want, localIdentity(t, dir))
			stamp, err := readGitConfigLocal(dir, authorStampKey)
			require.NoError(t, err)
			wantStamp := ""
			if tc.want == coworkerAuthor {
				wantStamp = stampOf(coworkerAuthor)
			}
			assert.Equal(t, wantStamp, stamp, "the stamp must name exactly the identity ox wrote, or be gone")
		})
	}
}

// Failure prevented: a Ledger push fails because its author could not be
// stamped — here because another writer holds the lock on .git/config.
func TestMigrateLedgerCredentials_StampFailureDoesNotFail(t *testing.T) {
	dir := ledgerCloneOf(t, "https://git.sageox.ai/team/ledger.git", [2]string{})
	_, err := MigrateLedgerCredentials(dir, "!ox git-credential-helper") // nothing left to write but the stamp
	require.NoError(t, err)
	LedgerAuthorGetter = func(string) (string, string) { return coworkerAuthor[0], coworkerAuthor[1] }
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config.lock"), nil, 0o644))

	_, err = MigrateLedgerCredentials(dir, "!ox git-credential-helper")
	require.NoError(t, err)
	assert.Equal(t, [2]string{}, localIdentity(t, dir), "the stamp should have failed on the held lock")
}

// Failure prevented: a stamp interrupted between two git config writes (a
// crash, a held lock) leaves ox's own partial identity, which the next call
// takes for a person's: a project prime never finishes writing the coworker,
// or a sign-out leaves the account's address behind. Each case interrupts
// after every individual write, then runs again and expects the finished state.
func TestStampAuthor_FinishesAfterAnInterruptionAtEveryStep(t *testing.T) {
	oldAuthor := [2]string{"Old Rip", "agt_old@ai-coworker.invalid"}
	for _, tc := range []struct {
		name    string
		before  [2]string
		stamp   string
		author  [2]string
		project bool
	}{
		{"project: first stamp", [2]string{}, "", coworkerAuthor, true},
		{"project: replacing its own stamp", oldAuthor, stampOf(oldAuthor), coworkerAuthor, true},
		{"project: removing its stamp", coworkerAuthor, stampOf(coworkerAuthor), [2]string{}, true},
		{"ledger: first stamp", [2]string{}, "", accountAuthor, false},
		{"ledger: replacing its own stamp", accountAuthor, stampOf(accountAuthor), coworkerAuthor, false},
		{"ledger: removing its stamp", accountAuthor, stampOf(accountAuthor), [2]string{}, false},
	} {
		for failAt := 1; ; failAt++ {
			dir := t.TempDir()
			require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
			setLocalIdentity(t, dir, tc.before, tc.stamp)
			stamp := func() error {
				if tc.project {
					return StampProjectAuthor(dir, tc.author[0], tc.author[1])
				}
				return stampAuthor(dir, tc.author[0], tc.author[1], true)
			}

			writes := 0
			failConfigWrite = func() error {
				if writes++; writes == failAt {
					return errors.New("interrupted")
				}
				return nil
			}
			interrupted := stamp() != nil
			failConfigWrite = nil
			require.NoError(t, stamp(), "%s, interrupted at write %d", tc.name, failAt)

			assert.Equal(t, tc.author, localIdentity(t, dir), "%s, interrupted at write %d", tc.name, failAt)
			got, err := readGitConfigLocal(dir, authorStampKey)
			require.NoError(t, err)
			want := ""
			if tc.author != [2]string{} {
				want = stampOf(tc.author)
			}
			assert.Equal(t, want, got, "%s, interrupted at write %d", tc.name, failAt)
			if !interrupted {
				break // failAt is past the last write: every step has been interrupted
			}
		}
	}
}
