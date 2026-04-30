package ledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiWriter exercises the structural ledger bug class: two writers
// committing to a shared remote with no shared base or with concurrent edits
// to root metadata files.
//
// With merge=union declared in .git/info/attributes (via EnsureMergeAttributes),
// the system auto-recovers — rebases concatenate instead of wedging.
// Without it, the rebase halts with conflict markers.
//
// Note: info/attributes is per-clone (not version-controlled). The client
// performing the rebase is the one that must have it configured; the seed
// remote and other clones don't carry it.
//
// This is the regression harness for epic ox-hs1b. Each subtest is a
// self-contained scenario in its own tempdir; no network, no real ox CLI.
func TestMultiWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses for clones, commits, rebases")
	}

	t.Run("addAdd_RootAGENTS_UnionMerges", testAddAddRootAGENTSUnionMerges)
	t.Run("addAdd_WithoutGitAttributes_Fails", testAddAddWithoutGitAttributesFails)
	t.Run("contentContent_RootAGENTS_UnionMerges", testContentContentRootAGENTSUnionMerges)
	t.Run("nonUnionPath_StillConflicts", testNonUnionPathStillConflicts)
	t.Run("cleanRebase_NoConflict", testCleanRebaseNoConflict)
}

// mwGit runs a git command in dir with isolated config and a deterministic
// identity, returning stdout+stderr. Fails the test on non-zero exit.
func mwGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := mwGitTry(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// mwGitTry is mwGit but returns the error instead of failing — used for
// commands that we expect to fail (e.g. a rebase that we want to assert
// halted with conflicts).
func mwGitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: tempdir git subprocess; HOME + GIT_CONFIG_NOSYSTEM below isolate from user's git config
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=init.defaultBranch", "GIT_CONFIG_VALUE_1=main",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// setupBareRemoteWithSeed creates a bare remote and seeds it with an initial
// commit containing a README.md so subsequent clones have a shared base.
//
// Note: per-clone info/attributes lives in each working clone's .git/info/,
// not in the bare remote, so this helper no longer needs a "withUnionAttrs"
// flag. Tests that want union-merge behavior call EnsureMergeAttributes on
// the rebasing clone directly.
//
// Returns the absolute path to the bare repo.
func setupBareRemoteWithSeed(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "bare.git")
	seed := filepath.Join(root, "seed")

	mwGit(t, root, "init", "--bare", "--initial-branch=main", bare)

	mwGit(t, root, "clone", bare, seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644))
	mwGit(t, seed, "add", "README.md")
	mwGit(t, seed, "commit", "-m", "seed")
	mwGit(t, seed, "push", "origin", "main")
	return bare
}

// cloneFresh returns a working clone of remote at a fresh tempdir path.
func cloneFresh(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	mwGit(t, filepath.Dir(dir), "clone", remote, dir)
	return dir
}

// hasConflictMarkers returns true if the file at path contains git conflict
// markers (<<<<<<< / =======/ >>>>>>>).
func hasConflictMarkers(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "<<<<<<<") &&
		strings.Contains(s, "=======") &&
		strings.Contains(s, ">>>>>>>")
}

// --- A. Add/Add on AGENTS.md with union merge ---
//
// Failure prevented: first session push wedges the ledger forever because
// two writers each independently created AGENTS.md, and the rebase halted
// on an add/add conflict instead of concatenating.
func testAddAddRootAGENTSUnionMerges(t *testing.T) {
	bare := setupBareRemoteWithSeed(t)

	// Client B (representing "server seed") commits its AGENTS.md first.
	clientB := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(clientB, "AGENTS.md"),
		[]byte("server content\n"), 0o644))
	mwGit(t, clientB, "add", "AGENTS.md")
	mwGit(t, clientB, "commit", "-m", "server: add AGENTS.md")
	mwGit(t, clientB, "push", "origin", "main")

	// Client A independently created AGENTS.md from the original base,
	// before B's push reached it. Re-clone from the seed-only state by
	// resetting to seed; simplest is a separate clone before B pushed —
	// but B already pushed, so clone fresh and rewind to seed^{commit}.
	clientA := cloneFresh(t, bare)
	// rewind clientA to the seed commit so it diverges from origin/main
	seedSHA := mwGit(t, clientA, "rev-list", "--max-parents=0", "HEAD")
	mwGit(t, clientA, "reset", "--hard", seedSHA)

	// Client A configures merge=union for its own clone via the per-clone
	// info/attributes file. This is what the production EnsureMergeAttributes
	// call does inside ledger.Init and the push pre-flight.
	_, err := EnsureMergeAttributes(clientA)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(clientA, "AGENTS.md"),
		[]byte("client content\n"), 0o644))
	mwGit(t, clientA, "add", "AGENTS.md")
	mwGit(t, clientA, "commit", "-m", "client: add AGENTS.md")

	// Pull --rebase --autostash. With merge=union this should auto-merge.
	mwGit(t, clientA, "fetch", "origin")
	out, rebaseErr := mwGitTry(t, clientA, "pull", "--rebase", "--autostash", "origin", "main")
	require.NoErrorf(t, rebaseErr, "rebase should succeed under merge=union; output:\n%s", out)

	// AGENTS.md should contain BOTH sides.
	data, err := os.ReadFile(filepath.Join(clientA, "AGENTS.md"))
	require.NoError(t, err)
	merged := string(data)
	assert.Contains(t, merged, "client content", "client side missing from union merge")
	assert.Contains(t, merged, "server content", "server side missing from union merge")
	assert.False(t, hasConflictMarkers(t, filepath.Join(clientA, "AGENTS.md")),
		"union merge should not leave conflict markers")

	// And client A can push.
	pushOut, pushErr := mwGitTry(t, clientA, "push", "origin", "main")
	require.NoErrorf(t, pushErr, "push after union merge should succeed; output:\n%s", pushOut)
}

// --- B. Same scenario WITHOUT .gitattributes — documents pre-fix behavior. ---
//
// This test intentionally documents the broken state. It asserts that
// without merge=union the rebase wedges on add/add. When ox-1qz9 ships the
// fix is to ensure .gitattributes is seeded; the test above proves the fix.
// This test ensures we do not silently lose the "before" behavior — if the
// rebase ever stops conflicting here for unrelated reasons, we want to know.
func testAddAddWithoutGitAttributesFails(t *testing.T) {
	bare := setupBareRemoteWithSeed(t)
	// Intentionally do NOT call EnsureMergeAttributes on clientA below.

	clientB := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(clientB, "AGENTS.md"),
		[]byte("server content\n"), 0o644))
	mwGit(t, clientB, "add", "AGENTS.md")
	mwGit(t, clientB, "commit", "-m", "server: add AGENTS.md")
	mwGit(t, clientB, "push", "origin", "main")

	clientA := cloneFresh(t, bare)
	seedSHA := mwGit(t, clientA, "rev-list", "--max-parents=0", "HEAD")
	mwGit(t, clientA, "reset", "--hard", seedSHA)

	require.NoError(t, os.WriteFile(filepath.Join(clientA, "AGENTS.md"),
		[]byte("client content\n"), 0o644))
	mwGit(t, clientA, "add", "AGENTS.md")
	mwGit(t, clientA, "commit", "-m", "client: add AGENTS.md")

	mwGit(t, clientA, "fetch", "origin")
	_, rebaseErr := mwGitTry(t, clientA, "pull", "--rebase", "--autostash", "origin", "main")
	assert.Error(t, rebaseErr, "without merge=union, rebase should halt on add/add conflict")

	// File should contain conflict markers.
	assert.True(t, hasConflictMarkers(t, filepath.Join(clientA, "AGENTS.md")),
		"AGENTS.md should contain conflict markers when union merge is absent")

	// Clean up so t.TempDir teardown doesn't trip on a wedged rebase state.
	_, _ = mwGitTry(t, clientA, "rebase", "--abort")
}

// --- C. Content/Content on AGENTS.md — both sides edit different sections ---
//
// Failure prevented: two coworkers updating their own sections of AGENTS.md
// in parallel cause the second-pusher to wedge.
func testContentContentRootAGENTSUnionMerges(t *testing.T) {
	bare := setupBareRemoteWithSeed(t)

	// Seed AGENTS.md with a base version on the remote.
	seeder := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(seeder, "AGENTS.md"),
		[]byte("base line\n"), 0o644))
	mwGit(t, seeder, "add", "AGENTS.md")
	mwGit(t, seeder, "commit", "-m", "seed AGENTS.md")
	mwGit(t, seeder, "push", "origin", "main")
	baseSHA := mwGit(t, seeder, "rev-parse", "HEAD")

	// Client B edits — appends a server-side section, pushes.
	clientB := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(clientB, "AGENTS.md"),
		[]byte("base line\nserver edit\n"), 0o644))
	mwGit(t, clientB, "add", "AGENTS.md")
	mwGit(t, clientB, "commit", "-m", "server: append section")
	mwGit(t, clientB, "push", "origin", "main")

	// Client A edits independently from baseSHA.
	clientA := cloneFresh(t, bare)
	mwGit(t, clientA, "reset", "--hard", baseSHA)
	_, err := EnsureMergeAttributes(clientA)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(clientA, "AGENTS.md"),
		[]byte("base line\nclient edit\n"), 0o644))
	mwGit(t, clientA, "add", "AGENTS.md")
	mwGit(t, clientA, "commit", "-m", "client: append section")

	mwGit(t, clientA, "fetch", "origin")
	out, rebaseErr := mwGitTry(t, clientA, "pull", "--rebase", "--autostash", "origin", "main")
	require.NoErrorf(t, rebaseErr, "rebase should succeed under merge=union; output:\n%s", out)

	data, _ := os.ReadFile(filepath.Join(clientA, "AGENTS.md"))
	merged := string(data)
	assert.Contains(t, merged, "client edit")
	assert.Contains(t, merged, "server edit")
	assert.False(t, hasConflictMarkers(t, filepath.Join(clientA, "AGENTS.md")))
}

// --- D. Non-union path still conflicts ---
//
// Failure prevented: union merge silently masking real conflicts on
// non-metadata paths. We want union scoped strictly to the listed root
// metadata files; arbitrary repo paths should still wedge a rebase as
// expected so callers (doctor, daemon) can surface them.
func testNonUnionPathStillConflicts(t *testing.T) {
	bare := setupBareRemoteWithSeed(t)

	// Seed data/something.json on remote.
	seeder := cloneFresh(t, bare)
	require.NoError(t, os.MkdirAll(filepath.Join(seeder, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(seeder, "data/something.json"),
		[]byte("{\"v\":0}\n"), 0o644))
	mwGit(t, seeder, "add", "data/something.json")
	mwGit(t, seeder, "commit", "-m", "seed data file")
	mwGit(t, seeder, "push", "origin", "main")
	baseSHA := mwGit(t, seeder, "rev-parse", "HEAD")

	clientB := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(clientB, "data/something.json"),
		[]byte("{\"v\":\"server\"}\n"), 0o644))
	mwGit(t, clientB, "add", "data/something.json")
	mwGit(t, clientB, "commit", "-m", "server edit data")
	mwGit(t, clientB, "push", "origin", "main")

	clientA := cloneFresh(t, bare)
	mwGit(t, clientA, "reset", "--hard", baseSHA)
	_, err := EnsureMergeAttributes(clientA)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(clientA, "data/something.json"),
		[]byte("{\"v\":\"client\"}\n"), 0o644))
	mwGit(t, clientA, "add", "data/something.json")
	mwGit(t, clientA, "commit", "-m", "client edit data")

	mwGit(t, clientA, "fetch", "origin")
	_, rebaseErr := mwGitTry(t, clientA, "pull", "--rebase", "--autostash", "origin", "main")
	assert.Error(t, rebaseErr, "non-union path should still produce a rebase conflict")
	assert.True(t, hasConflictMarkers(t, filepath.Join(clientA, "data/something.json")),
		"non-union path should have conflict markers")

	_, _ = mwGitTry(t, clientA, "rebase", "--abort")
}

// --- E. Clean rebase, no conflict ---
//
// Failure prevented: regression where union or other merge tweaks
// accidentally turn a happy-path divergent rebase into a failure.
func testCleanRebaseNoConflict(t *testing.T) {
	bare := setupBareRemoteWithSeed(t)

	// Client B edits one path.
	clientB := cloneFresh(t, bare)
	require.NoError(t, os.WriteFile(filepath.Join(clientB, "server-only.txt"),
		[]byte("server\n"), 0o644))
	mwGit(t, clientB, "add", "server-only.txt")
	mwGit(t, clientB, "commit", "-m", "server only file")
	mwGit(t, clientB, "push", "origin", "main")

	// Client A edits a different path from the seed base.
	clientA := cloneFresh(t, bare)
	seedSHA := mwGit(t, clientA, "rev-list", "--max-parents=0", "HEAD")
	mwGit(t, clientA, "reset", "--hard", seedSHA)
	require.NoError(t, os.WriteFile(filepath.Join(clientA, "client-only.txt"),
		[]byte("client\n"), 0o644))
	mwGit(t, clientA, "add", "client-only.txt")
	mwGit(t, clientA, "commit", "-m", "client only file")

	mwGit(t, clientA, "fetch", "origin")
	out, rebaseErr := mwGitTry(t, clientA, "pull", "--rebase", "--autostash", "origin", "main")
	require.NoErrorf(t, rebaseErr, "clean rebase should succeed; output:\n%s", out)

	// Both files should exist post-rebase.
	_, err := os.Stat(filepath.Join(clientA, "server-only.txt"))
	assert.NoError(t, err, "server-only.txt should be present after rebase")
	_, err = os.Stat(filepath.Join(clientA, "client-only.txt"))
	assert.NoError(t, err, "client-only.txt should be present after rebase")
}
