package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initBareRepo creates a bare git repo with an initial commit containing a
// .sageox/config.json file — mimicking a real ledger remote.
func initBareRepo(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: git clone")
	}

	// create a regular repo, commit something, then clone --bare
	src := t.TempDir()
	cmds := [][]string{
		{"git", "-C", src, "init"},
		{"git", "-C", src, "config", "user.email", "test@test.com"},
		{"git", "-C", src, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%v: %s", args, out)
	}

	// create .sageox/ and sessions/ to mimic real ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(src, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, ".sageox", "config.json"), []byte(`{}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sessions", ".gitkeep"), []byte(""), 0o644))

	cmd := exec.Command("git", "-C", src, "add", "-A")
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", src, "commit", "-m", "initial")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "commit: %s", out)

	// clone to bare
	bare := t.TempDir()
	require.NoError(t, os.RemoveAll(bare)) // git clone needs target to not exist
	cmd = exec.Command("git", "clone", "--bare", src, bare)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "bare clone: %s", out)
	return bare
}

// --------------------------------------------------------------------------
// CloneWithSparseCheckout
// --------------------------------------------------------------------------

func TestCloneWithSparseCheckout_SageoxInCone(t *testing.T) {
	// real-world failure prevented: if .sageox is not in the sparse-checkout
	// cone, ConfigureSparseCheckout will delete .sageox/cache/ (codedb indexes)
	// on every pull — the root cause of the perpetual indexing loop bug
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")

	err := CloneWithSparseCheckout(dest, bare)
	require.NoError(t, err)

	// read sparse-checkout file and verify .sageox is included
	scFile := filepath.Join(dest, ".git", "info", "sparse-checkout")
	content, err := os.ReadFile(scFile)
	require.NoError(t, err, "sparse-checkout file must exist after clone")
	assert.Contains(t, string(content), ".sageox",
		".sageox MUST be in sparse-checkout cone to prevent cache wipe")
}

func TestCloneWithSparseCheckout_SessionsDirCheckedOut(t *testing.T) {
	// real-world failure prevented: if sessions/ is not in the cone, session
	// data written by the CLI won't be visible after clone
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")

	err := CloneWithSparseCheckout(dest, bare)
	require.NoError(t, err)

	assert.DirExists(t, filepath.Join(dest, "sessions"),
		"sessions/ must be checked out for session recording to work")
}

func TestCloneWithSparseCheckout_EmptyURL_ReturnsError(t *testing.T) {
	// real-world failure prevented: passing empty URL to git clone produces a
	// confusing git error; validate early with a clear message
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "clone")
	err := CloneWithSparseCheckout(dest, "")
	assert.Error(t, err, "empty URL must return error")
	assert.Contains(t, err.Error(), "empty")
}

func TestCloneWithSparseCheckout_InvalidURL_ReturnsError(t *testing.T) {
	// real-world failure prevented: if daemon tries to clone with a stale or
	// corrupt URL, must get a clear error, not a panic or partial clone
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone")
	}

	dest := filepath.Join(t.TempDir(), "clone")

	err := CloneWithSparseCheckout(dest, "https://invalid.example.com/nonexistent.git")
	assert.Error(t, err, "invalid URL must return error")
	assert.Contains(t, err.Error(), "git clone")
}

func TestCloneWithSparseCheckout_ExistingNonEmptyDir_Fails(t *testing.T) {
	// real-world failure prevented: if two clone operations race (e.g. daemon
	// GC reclone + manual clone), the second must fail cleanly rather than
	// corrupt the first clone's state
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "blocker.txt"), []byte("existing"), 0o644))

	err := CloneWithSparseCheckout(dest, bare)
	// git clone into non-empty dir should fail
	assert.Error(t, err,
		"cloning into non-empty directory must fail to prevent data corruption")
}

func TestCloneWithSparseCheckout_EmptyDirReplacedByClone(t *testing.T) {
	// real-world failure prevented: GC reclone creates an empty dir first,
	// then clones into it. The function should handle this by removing the
	// empty dir before git clone (which requires the path to not exist)
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	// dest exists but is empty — should be auto-removed

	err := CloneWithSparseCheckout(dest, bare)
	require.NoError(t, err, "clone into empty dir should succeed (dir auto-removed)")

	// verify it's a valid git repo
	assert.FileExists(t, filepath.Join(dest, ".git", "HEAD"))
}

func TestCloneWithSparseCheckout_CacheSurvivesReconfigure(t *testing.T) {
	// real-world failure prevented (regression #359): daemon clones ledger,
	// codedb writes indexes into .sageox/cache/codedb/, then sync scheduler
	// calls ConfigureSparseCheckout again ~60s later. The cache must survive.
	// This is the exact scenario that caused the perpetual indexing loop.
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")

	require.NoError(t, CloneWithSparseCheckout(dest, bare))

	// simulate codedb writing an index file into the cache
	cacheDir := filepath.Join(dest, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	sentinel := filepath.Join(cacheDir, "index.bleve")
	require.NoError(t, os.WriteFile(sentinel, []byte("bleve-index-data"), 0o644))

	// simulate the sync scheduler reconfiguring sparse checkout (every ~60s)
	require.NoError(t, ConfigureSparseCheckout(dest))

	_, err := os.Stat(sentinel)
	assert.NoError(t, err,
		".sageox/cache/codedb/index.bleve must survive ConfigureSparseCheckout; "+
			"if missing, the sync scheduler is destroying codedb indexes on every pull cycle")
}
