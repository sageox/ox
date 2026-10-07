package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingRaceImport leaves a real import commit behind a rejected push.
func pendingRaceImport(t *testing.T) (*importRetryFixture, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" || testing.Short() {
		t.Skip("uses POSIX Git hooks and multiple real Git transports")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "Permission denied")
	require.NotEqual(t, initial, runGit(t, tcPath, "rev-parse", "HEAD"))
	require.NoError(t, os.Remove(hook))
	return f, tcPath, initial
}

// importPrepGap runs an external writer at credential refresh, after preparation
// releases its lock but before the first push validates the current history.
func importPrepGap(t *testing.T, tcPath, action string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	realGit, err = filepath.Abs(realGit)
	require.NoError(t, err)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "injected")
	t.Setenv("TEST_IMPORT_REAL_GIT", realGit)
	t.Setenv("TEST_IMPORT_REPO", tcPath)
	t.Setenv("TEST_IMPORT_MARKER", marker)
	script := `#!/bin/sh
set -eu
git() { "$TEST_IMPORT_REAL_GIT" "$@"; }
if [ "$#" -eq 5 ] && [ "$1" = '-C' ] && [ "$2" = "$TEST_IMPORT_REPO" ] && [ "$3" = 'remote' ] && [ "$4" = 'get-url' ] && [ "$5" = 'origin' ] && [ ! -e "$TEST_IMPORT_MARKER" ]; then
    touch "$TEST_IMPORT_MARKER"
` + action + `
fi
exec "$TEST_IMPORT_REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func TestImport_RetryRejectsPrivateChangesAfterPreparation(t *testing.T) {
	for _, amend := range []bool{false, true} {
		name := "extra commit"
		if amend {
			name = "amended import"
		}
		t.Run(name, func(t *testing.T) {
			f, tcPath, initial := pendingRaceImport(t)
			commit := `git -C "$TEST_IMPORT_REPO" commit --no-verify -m 'private notes'`
			if amend {
				commit += " --amend"
			}
			marker := importPrepGap(t, tcPath, `printf 'private notes\n' > "$TEST_IMPORT_REPO/private.md"
git -C "$TEST_IMPORT_REPO" add private.md
`+commit)
			out, err := f.importDoc(false)
			require.FileExists(t, marker, "the writer must run in the preparation/push gap")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "review outgoing commits")
			assert.NotContains(t, out, "Imported:")
			assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
			assert.Empty(t, runGit(t, f.bare, "log", "--format=%H", "HEAD", "--", "private.md"))
			assert.Equal(t, "private notes", runGit(t, tcPath, "show", "HEAD:private.md"))
			assert.Empty(t, runGit(t, tcPath, "status", "--porcelain"))
		})
	}
}

func TestImport_RetryRecognizesPublicationAfterPreparation(t *testing.T) {
	f, tcPath, _ := pendingRaceImport(t)
	pending := runGit(t, tcPath, "rev-parse", "HEAD")
	marker := importPrepGap(t, tcPath, `git -C "$TEST_IMPORT_REPO" push origin main
printf 'private notes\n' > "$TEST_IMPORT_REPO/private.md"
git -C "$TEST_IMPORT_REPO" add private.md
git -C "$TEST_IMPORT_REPO" commit --no-verify -m 'private notes'`)
	out, err := f.importDoc(false)
	require.FileExists(t, marker)
	require.NoError(t, err)
	assert.Contains(t, out, "Already imported")
	assert.Equal(t, pending, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "private.md"))
	assert.Equal(t, "private notes", runGit(t, tcPath, "show", "HEAD:private.md"))
}

func TestImport_RetryDoesNotResurrectDeletionDuringPush(t *testing.T) {
	f, tcPath, _ := pendingRaceImport(t)
	// The second writer starts from the same import and removes its document.
	other := cloneBare(t, tcPath)
	runGit(t, other, "remote", "set-url", "origin", f.bare)
	const doc = "data/docs/2026/09/19/q3-plan"
	runGit(t, other, "rm", "-r", doc)
	runGit(t, other, "commit", "--no-verify", "-m", "remove published document")
	deleted := runGit(t, other, "rev-parse", "HEAD")
	private := filepath.Join(tcPath, ".gitkeep")
	require.NoError(t, os.WriteFile(private, []byte("working private notes\n"), 0o644))

	t.Setenv("TEST_IMPORT_OTHER_REPO", other)
	marker := filepath.Join(t.TempDir(), "published-deletion")
	t.Setenv("TEST_IMPORT_DELETE_MARKER", marker)
	hook := `#!/bin/sh
set -eu
if [ ! -e "$TEST_IMPORT_DELETE_MARKER" ]; then
    touch "$TEST_IMPORT_DELETE_MARKER"
    unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
    git -C "$TEST_IMPORT_OTHER_REPO" push origin main
fi
`
	require.NoError(t, os.WriteFile(filepath.Join(tcPath, ".git", "hooks", "pre-push"), []byte(hook), 0o755))
	out, err := f.importDoc(false)
	require.FileExists(t, marker, "upstream must advance during the first push")
	require.Error(t, err, "retry must refuse the removed document")
	assert.NotContains(t, out, "Imported:")
	assert.Equal(t, deleted, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", doc))
	assert.Empty(t, runGit(t, f.bare, "show", "HEAD:.gitkeep"))
	content, readErr := os.ReadFile(private)
	require.NoError(t, readErr)
	assert.Equal(t, "working private notes\n", string(content))
}

func TestImport_RetryRefusesSynchronizationWithStagedPrivateChanges(t *testing.T) {
	f, tcPath, _ := pendingRaceImport(t)
	other := cloneBare(t, f.bare)
	require.NoError(t, os.WriteFile(filepath.Join(other, "upstream.md"), []byte("remote update\n"), 0o644))
	runGit(t, other, "add", "upstream.md")
	runGit(t, other, "commit", "--no-verify", "-m", "advance upstream")
	runGit(t, other, "push", "origin", "main")
	remoteBefore := runGit(t, f.bare, "show-ref")
	localBefore := runGit(t, tcPath, "show-ref")
	headBefore := runGit(t, tcPath, "rev-parse", "HEAD")
	private := filepath.Join(tcPath, "draft.md")
	require.NoError(t, os.WriteFile(private, []byte("staged private notes\n"), 0o644))
	runGit(t, tcPath, "add", "draft.md")
	require.NoError(t, os.WriteFile(private, []byte("working private notes\n"), 0o644))
	indexBefore := runGit(t, tcPath, "show", ":draft.md")

	out, err := f.importDoc(false)
	require.ErrorContains(t, err, "staged changes")
	assert.NotContains(t, out, "Imported:")
	assert.Equal(t, headBefore, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, localBefore, runGit(t, tcPath, "show-ref"), "refusing synchronization must not fetch or rewrite local refs")
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "show-ref"))
	assert.Equal(t, indexBefore, runGit(t, tcPath, "show", ":draft.md"))
	content, readErr := os.ReadFile(private)
	require.NoError(t, readErr)
	assert.Equal(t, "working private notes\n", string(content))
}

func TestImport_RetryRejectsPrivateHistoryAddedAfterRebase(t *testing.T) {
	f, tcPath, _ := pendingRaceImport(t)
	other := cloneBare(t, f.bare)
	require.NoError(t, os.WriteFile(filepath.Join(other, "upstream.md"), []byte("remote update\n"), 0o644))
	runGit(t, other, "add", "upstream.md")
	runGit(t, other, "commit", "--no-verify", "-m", "advance upstream")
	runGit(t, other, "push", "origin", "main")
	advanced := runGit(t, f.bare, "rev-parse", "HEAD")
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	realGit, err = filepath.Abs(realGit)
	require.NoError(t, err)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "rewritten")
	t.Setenv("TEST_IMPORT_REAL_GIT", realGit)
	t.Setenv("TEST_IMPORT_REPO", tcPath)
	t.Setenv("TEST_IMPORT_MARKER", marker)
	// Run the real rebase first so the writer adds history after synchronization,
	// while the next push still has to validate its ownership of outgoing commits.
	script := `#!/bin/sh
set -eu
"$TEST_IMPORT_REAL_GIT" "$@"
if [ "$#" -ge 7 ] && [ "$1" = '-C' ] && [ "$2" = "$TEST_IMPORT_REPO" ]; then
    shift 2
    while [ "$#" -ge 2 ] && [ "$1" = '-c' ]; do shift 2; done
    if [ "$#" -eq 5 ] && [ "$1" = 'rebase' ] && [ "$2" = '--autostash' ] && [ "$3" = '--fork-point' ] && [ "$4" = '--quiet' ] && [ ! -e "$TEST_IMPORT_MARKER" ]; then
        touch "$TEST_IMPORT_MARKER"
        printf 'private notes\n' > "$TEST_IMPORT_REPO/private.md"
        "$TEST_IMPORT_REAL_GIT" -C "$TEST_IMPORT_REPO" add private.md
        "$TEST_IMPORT_REAL_GIT" -C "$TEST_IMPORT_REPO" commit --no-verify -m 'private notes'
    fi
fi
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := f.importDoc(false)
	require.FileExists(t, marker, "the actual retry rebase must complete before the writer runs")
	require.ErrorContains(t, err, "review outgoing commits")
	assert.NotContains(t, out, "Imported:")
	assert.Equal(t, advanced, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "data/docs", "private.md"))
	assert.Empty(t, runGit(t, f.bare, "log", "--format=%H", "HEAD", "--", "private.md"))
	assert.Equal(t, "private notes", runGit(t, tcPath, "show", "HEAD:private.md"))
	assert.Equal(t, "private notes", runGit(t, tcPath, "log", "-1", "--format=%s"))
	assert.Equal(t, "import: doc q3-plan", runGit(t, tcPath, "log", "-1", "--format=%s", "HEAD^"))
	assert.Equal(t, advanced, runGit(t, tcPath, "rev-parse", "HEAD~2"))
}

func TestImport_PendingRetryRejectsDifferentPushAndUpstreamRefs(t *testing.T) {
	f, tcPath, initial := pendingRaceImport(t)
	runGit(t, tcPath, "remote", "add", "alternate", f.bare)
	runGit(t, tcPath, "fetch", "alternate")
	runGit(t, tcPath, "config", "branch.main.pushRemote", "alternate")
	runGit(t, tcPath, "config", "push.default", "current")
	localBefore := runGit(t, tcPath, "show-ref")
	out, err := f.importDoc(false)
	require.ErrorContains(t, err, "upstream and push tracking refs must match")
	assert.NotContains(t, out, "Imported:")
	assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Equal(t, localBefore, runGit(t, tcPath, "show-ref"))
}
