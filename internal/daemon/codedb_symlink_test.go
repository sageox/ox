package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// Failure prevented: the shared code index under the Ledger is refused (a
// tracked file or symlink at .sageox/cache/codedb), so the daemon falls back to
// the worktree's .sageox/cache/codedb, and a symlink the source repository
// tracks there aims every index write outside it. Neither path is opened.
func TestCodeDB_RefusesASymlinkedFallbackCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git project and code index")
	}
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path, outside string)
	}{
		{"the Ledger tracks a file there", func(t *testing.T, path, _ string) {
			require.NoError(t, os.WriteFile(path, []byte("tracked\n"), 0o644))
		}},
		{"the Ledger tracks a symlink there", func(t *testing.T, path, outside string) {
			require.NoError(t, os.Symlink(outside, path))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := sandboxLiveHome(t)
			const repoID = "repo_codedb_symlink"
			root := startupProject(t, repoID)
			mustGitIn(t, root, "init", "-q")
			require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
			mustGitIn(t, root, "add", "main.go")
			mustGitIn(t, root, "commit", "-q", "-m", "init")

			ledgerDir := paths.LedgersDataDir(repoID, endpoint.GetForProject(root))
			require.True(t, strings.HasPrefix(ledgerDir, home), "the Ledger must be in the sandbox: %s", ledgerDir)
			require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, ".sageox", "cache"), 0o755))
			ledgerOutside := t.TempDir()
			tc.plant(t, filepath.Join(ledgerDir, ".sageox", "cache", "codedb"), ledgerOutside)

			worktreeOutside := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox", "cache"), 0o755))
			require.NoError(t, os.Symlink(worktreeOutside, filepath.Join(root, ".sageox", "cache", "codedb")))

			logs := &syncBuffer{}
			m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(logs, nil)), nil)
			require.Empty(t, m.resolveSharedDataDir(), "a symlinked worktree fallback was selected")
			require.Empty(t, m.resolveLedgerDataDir(), "the Ledger index dir under a refused cache was selected")
			indexAndWait(t, m)

			for _, dir := range []string{worktreeOutside, ledgerOutside} {
				written, err := os.ReadDir(dir)
				require.NoError(t, err)
				require.Empty(t, written, "the code index was written through a symlink, outside its checkout")
			}
			require.Contains(t, logs.String(), "level=ERROR msg=codedb_index_refused")
		})
	}
}

// codeDBSymlinkProject is a git project with one commit and one uncommitted
// edit (so indexing builds a dirty overlay too), and its sandboxed Ledger dir.
func codeDBSymlinkProject(t *testing.T, repoID string) (root, ledgerDir string) {
	t.Helper()
	home := sandboxLiveHome(t)
	root = startupProject(t, repoID)
	mustGitIn(t, root, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	mustGitIn(t, root, "add", "main.go")
	mustGitIn(t, root, "commit", "-q", "-m", "init")
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() { helper() }\n\nfunc helper() {}\n"), 0o644))
	ledgerDir = paths.LedgersDataDir(repoID, endpoint.GetForProject(root))
	require.True(t, strings.HasPrefix(ledgerDir, home), "the Ledger must be in the sandbox: %s", ledgerDir)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, ".sageox", "cache"), 0o755))
	return root, ledgerDir
}

// indexAndWait runs a freshness check and a dirty-overlay refresh to the end.
func indexAndWait(t *testing.T, m *CodeDBManager) {
	t.Helper()
	m.CheckFreshness(context.Background())
	m.RefreshDirtyOverlay(context.Background())
	idle, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, m.WaitIdle(idle))
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.dirtyRefreshing
	}, 60*time.Second, 20*time.Millisecond, "the dirty overlay refresh never finished")
}

func requireNothingWritten(t *testing.T, dir string) {
	t.Helper()
	written, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, written, "a code index was created or opened through a symlink, outside its checkout")
}

// Failure prevented: the worktree index is fine, but the Ledger's
// .sageox/cache/codedb is a tracked symlink, and indexing also writes the
// dirty overlay into <that>/ledger: the writable open mkdirs through the link.
func TestCodeDB_RefusesALedgerIndexDirBehindASymlink(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git project and code index")
	}
	root, ledgerDir := codeDBSymlinkProject(t, "repo_codedb_ledger_link")
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(ledgerDir, ".sageox", "cache", "codedb")))

	logs := &syncBuffer{}
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(logs, nil)), nil)
	indexAndWait(t, m)

	requireNothingWritten(t, outside)
	require.DirExists(t, filepath.Join(root, ".sageox", "cache", "codedb"), "precondition: the worktree index was built")
	require.Contains(t, logs.String(), "level=ERROR")
}

// Failure prevented: the path is checked once and cached, then a pull checks
// a symlink out at .sageox/cache/codedb; every later mkdir and open must
// check again rather than follow it.
func TestCodeDB_ChecksEveryOpenNotOnlyTheFirstResolve(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git project and code index")
	}
	root, ledgerDir := codeDBSymlinkProject(t, "repo_codedb_late_link")
	require.NoError(t, os.Mkdir(filepath.Join(ledgerDir, ".git"), 0o755)) // cloned: doIndex indexes into it
	logs := &syncBuffer{}
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(logs, nil)), nil)
	shared := filepath.Join(ledgerDir, ".sageox", "cache", "codedb")
	require.Equal(t, shared, m.resolveSharedDataDir(), "precondition: the Ledger path was resolved and cached")

	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, shared))
	indexAndWait(t, m)

	requireNothingWritten(t, outside)
	require.Contains(t, logs.String(), "level=ERROR msg=codedb_index_refused")
}
