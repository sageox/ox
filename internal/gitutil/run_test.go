package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("git version succeeds", func(t *testing.T) {
		output, err := RunGit(context.Background(), "", "version")
		assert.NoError(t, err)
		assert.Contains(t, output, "git version")
	})

	t.Run("invalid command returns error", func(t *testing.T) {
		_, err := RunGit(context.Background(), "", "not-a-real-command")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "git not-a-real-command")
	})

	t.Run("canceled context returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := RunGit(ctx, "", "version")
		assert.Error(t, err)
	})

	t.Run("timeout context returns error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		time.Sleep(1 * time.Millisecond) // ensure timeout fires

		_, err := RunGit(ctx, "", "version")
		assert.Error(t, err)
	})

	t.Run("with repo path", func(t *testing.T) {
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())

		output, err := RunGit(context.Background(), repo, "status", "--porcelain")
		assert.NoError(t, err)
		// fresh repo has no output
		assert.Empty(t, output)
	})

	t.Run("without repo path omits -C flag", func(t *testing.T) {
		// git version works without -C
		output, err := RunGit(context.Background(), "", "version")
		assert.NoError(t, err)
		assert.Contains(t, output, "git version")
	})

	t.Run("commit succeeds despite inherited signing config", func(t *testing.T) {
		// Reproduces the ledger-wedge class: a repo whose config enables SSH
		// commit signing with an unusable key. A bare `git commit` dies with
		// "failed to write commit object"; RunGit must override it inline so
		// ox's machine-managed commits never depend on a TTY passphrase prompt.
		repo := t.TempDir()
		setup := [][]string{
			{"-C", repo, "init", "--quiet"},
			{"-C", repo, "config", "--local", "user.name", "Test"},
			{"-C", repo, "config", "--local", "user.email", "test@example.com"},
			{"-C", repo, "config", "--local", "gpg.format", "ssh"},
			{"-C", repo, "config", "--local", "user.signingkey", filepath.Join(repo, "nope")},
			{"-C", repo, "config", "--local", "commit.gpgsign", "true"},
		}
		for _, a := range setup {
			require.NoError(t, exec.Command("git", a...).Run())
		}
		require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o644))
		require.NoError(t, exec.Command("git", "-C", repo, "add", "-A").Run())

		// sanity: a raw signed commit fails in this headless env
		require.Error(t, exec.Command("git", "-C", repo, "commit", "-m", "raw").Run())

		// RunGit's injected -c commit.gpgsign=false clears the wedge
		_, err := RunGit(context.Background(), repo, "commit", "-m", "via RunGit")
		assert.NoError(t, err)
	})

	t.Run("output is auto-sanitized", func(t *testing.T) {
		// create a repo with a remote that has credentials embedded
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())

		// set a remote with embedded credentials
		cmd = exec.Command("git", "-C", repo, "remote", "add", "origin",
			"https://oauth2:secret-token@gitlab.com/org/repo.git")
		require.NoError(t, cmd.Run())

		// git remote -v will show the credential URL
		output, err := RunGit(context.Background(), repo, "remote", "-v")
		assert.NoError(t, err)
		assert.NotContains(t, output, "secret-token")
		assert.Contains(t, output, "oauth2:***@")
	})

	t.Run("insulated from a global git-lfs install (ox-baz5.4)", func(t *testing.T) {
		// Simulates a machine with git-lfs installed globally: filter.lfs.*
		// configured in the GLOBAL gitconfig (not this repo's), the way
		// `git lfs install --global` leaves it. GIT_CONFIG_GLOBAL (git
		// 2.32+) redirects git's notion of "global config" to this fixture
		// file, isolated from the real user running the test.
		globalConfig := filepath.Join(t.TempDir(), "gitconfig")
		// git already runs a filter command through the shell itself, so the
		// config value is the raw shell command — no extra `sh -c` wrapper.
		// Double-quoted: gitconfig(5) treats an unquoted `;` as a trailing
		// comment marker, which would silently truncate this value.
		require.NoError(t, os.WriteFile(globalConfig, []byte(
			"[filter \"lfs\"]\n"+
				"\tsmudge = \"cat >/dev/null; printf SMUDGED\"\n"+
				"\tclean = cat\n"+
				"\trequired = true\n"), 0o644))
		t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
		// GIT_CONFIG_GLOBAL redirects only the GLOBAL file — the RAW
		// checkout below (the fixture-sanity step, run without RunGit's
		// insulation) still reads whatever SYSTEM config the host actually
		// has. filter.<driver>.process (a long-running filter protocol)
		// takes precedence over smudge/clean when set, so a host with a
		// real system-level git-lfs config could silently override this
		// fixture's smudge and break the sanity check. GIT_CONFIG_NOSYSTEM
		// makes the raw checkout hermetic regardless of host state.
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

		repo := t.TempDir()
		run := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoErrorf(t, err, "git %v: %s", args, out)
		}
		run("init", "--quiet")
		run("config", "--local", "user.name", "Test")
		run("config", "--local", "user.email", "test@example.com")
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("test.bin filter=lfs\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "test.bin"), []byte("REAL-CONTENT"), 0o644))
		run("add", "-A")
		run("commit", "--quiet", "-m", "init")

		// Fixture sanity: a RAW checkout (no insulation) IS smudged by the
		// global filter — proves this fixture actually exercises the bug
		// this test guards, not a no-op.
		require.NoError(t, os.Remove(filepath.Join(repo, "test.bin")))
		run("checkout", "--", "test.bin")
		raw, err := os.ReadFile(filepath.Join(repo, "test.bin"))
		require.NoError(t, err)
		require.Equal(t, "SMUDGED", string(raw), "fixture sanity: raw checkout must be smudged by the global filter")

		// The fix: a checkout run through RunGit must NOT be smudged, and
		// the worktree must stay clean afterward. Without RunGit's
		// filter.lfs.* overrides this is the ox-baz5.4 wedge: a nested LFS
		// pointer can never equal HEAD, autostash leaks one entry per pull
		// cycle, and `ox doctor --fix` cannot help.
		require.NoError(t, os.Remove(filepath.Join(repo, "test.bin")))
		_, err = RunGit(context.Background(), repo, "checkout", "--", "test.bin")
		assert.NoError(t, err)
		insulated, err := os.ReadFile(filepath.Join(repo, "test.bin"))
		require.NoError(t, err)
		assert.Equal(t, "REAL-CONTENT", string(insulated),
			"RunGit must insulate checkout from the global git-lfs smudge filter")

		status, err := RunGit(context.Background(), repo, "status", "--porcelain")
		assert.NoError(t, err)
		assert.Empty(t, status, "worktree must be clean after an insulated checkout — this is what 'daemon pull succeeds and stays clean' means")
	})

	t.Run("error output is also sanitized", func(t *testing.T) {
		// create a repo pointing to a nonexistent remote with credentials
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())
		cmd = exec.Command("git", "-C", repo, "remote", "add", "origin",
			"https://oauth2:secret-token@nonexistent.example.com/repo.git")
		require.NoError(t, cmd.Run())

		// create a file and commit so we have something to push
		require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("test"), 0644))
		cmd = exec.Command("git", "-C", repo, "add", "file.txt")
		require.NoError(t, cmd.Run())
		cmd = exec.Command("git", "-C", repo, "commit", "-m", "init", "--no-verify")
		require.NoError(t, cmd.Run())

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := RunGit(ctx, repo, "push", "--quiet")
		if err != nil {
			// the error message should not contain the token
			assert.NotContains(t, err.Error(), "secret-token")
		}
	})
}
