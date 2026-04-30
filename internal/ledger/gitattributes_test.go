package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const infoAttrsRel = ".git/info/attributes"

// mkGitDir returns a tempdir that satisfies EnsureMergeAttributes' "must
// be a git repo" precondition by creating an empty .git/ directory.
// We do not run `git init` because these tests don't need a real repo —
// they only exercise the path-handling logic of EnsureMergeAttributes.
func mkGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	return dir
}

func TestEnsureMergeAttributes_CreatesFileWhenMissing(t *testing.T) {
	dir := mkGitDir(t)

	changed, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)
	require.True(t, changed, "expected changed=true on initial write")

	data, err := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	require.NoError(t, err, "read info/attributes")
	got := string(data)

	for _, want := range []string{
		mergeAttrsHeader,
		mergeAttrsFooter,
		"AGENTS.md",
		"merge=union",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

// TestEnsureMergeAttributes_NotInWorkingTree verifies our chosen path
// (.git/info/attributes) keeps the user's git-lfs install and any
// user-authored top-level .gitattributes untouched.
//
// Failure prevented: regression where someone "helpfully" reverts the
// path to .gitattributes in the working tree and breaks every user with
// git-lfs configured globally.
func TestEnsureMergeAttributes_NotInWorkingTree(t *testing.T) {
	dir := mkGitDir(t)
	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(dir, ".gitattributes")); !os.IsNotExist(err) {
		t.Errorf("EnsureMergeAttributes must NOT create a tracked .gitattributes; got err=%v", err)
	}
}

func TestEnsureMergeAttributes_Idempotent(t *testing.T) {
	dir := mkGitDir(t)

	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err, "first call")

	changed, err := EnsureMergeAttributes(dir)
	require.NoError(t, err, "second call")
	require.False(t, changed, "expected changed=false on second call")
}

// TestEnsureMergeAttributes_PreservesExistingContent verifies that a
// user-authored line in info/attributes is not clobbered by our managed
// block.
//
// Failure prevented: a user who manually configures attributes loses
// their config the next time ox runs.
func TestEnsureMergeAttributes_PreservesExistingContent(t *testing.T) {
	dir := mkGitDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git/info"), 0o755))
	userLine := "*.bin binary\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(userLine), 0o644))

	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	require.NoError(t, err, "read info/attributes")
	got := string(data)
	if !strings.Contains(got, userLine) {
		t.Errorf("user content %q was lost; got:\n%s", userLine, got)
	}
	if !strings.Contains(got, "merge=union") {
		t.Errorf("managed block missing; got:\n%s", got)
	}
}

// TestEnsureMergeAttributes_ReplacesManagedBlock verifies that stale
// ox-managed content is rewritten while user content outside the markers
// survives.
//
// Failure prevented: an old managed block (e.g. from a different version
// with different paths) accumulates across runs instead of being replaced.
func TestEnsureMergeAttributes_ReplacesManagedBlock(t *testing.T) {
	dir := mkGitDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git/info"), 0o755))
	stale := mergeAttrsHeader + "\nAGENTS.md merge=stale-driver\n" + mergeAttrsFooter + "\n"
	userLine := "*.bin binary\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(userLine+stale), 0o644))

	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	require.NoError(t, err)
	got := string(data)
	if strings.Contains(got, "stale-driver") {
		t.Errorf("stale managed content not replaced; got:\n%s", got)
	}
	if !strings.Contains(got, userLine) {
		t.Errorf("user content lost; got:\n%s", got)
	}
	if !strings.Contains(got, "merge=union") {
		t.Errorf("canonical managed block missing; got:\n%s", got)
	}
}

// TestEnsureMergeAttributes_HandlesMissingFooter verifies that a
// truncated managed block (header but no footer) is replaced rather
// than appended to.
//
// Failure prevented: corruption (interrupted write, hand-edit) causes
// the managed block to grow without bound on each Ensure call.
func TestEnsureMergeAttributes_HandlesMissingFooter(t *testing.T) {
	dir := mkGitDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git/info"), 0o755))
	corrupted := mergeAttrsHeader + "\nAGENTS.md merge=truncated\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(corrupted), 0o644))

	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	require.NoError(t, err)
	got := string(data)
	if strings.Contains(got, "merge=truncated") {
		t.Errorf("truncated managed block not replaced; got:\n%s", got)
	}
	if !strings.Contains(got, mergeAttrsFooter) {
		t.Errorf("footer not restored; got:\n%s", got)
	}
}

func TestEnsureMergeAttributes_EmptyPath(t *testing.T) {
	_, err := EnsureMergeAttributes("")
	require.Error(t, err)
}

// TestEnsureMergeAttributes_NotAGitRepo verifies the precondition guard:
// callers passing a non-git path get a clear error rather than a silent
// MkdirAll into the wrong location.
//
// Failure prevented: a refactor that changes how callers compute
// ledgerPath silently writes attributes into a non-git directory where
// git will never read them, leaving the union driver disabled.
func TestEnsureMergeAttributes_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // intentionally NO .git inside
	_, err := EnsureMergeAttributes(dir)
	require.Error(t, err, "should fail without a .git directory")
}
