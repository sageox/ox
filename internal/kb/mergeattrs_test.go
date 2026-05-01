package kb

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

// TestEnsureMergeAttributes is table-driven across the read-modify-write
// scenarios. Each entry documents a specific failure that the case
// prevents — naming after the failure prevented per the project's testing
// rule, while keeping setup compact via the table.
//
// True invariant tests (paths that EnsureMergeAttributes is forbidden
// from touching, preconditions, errors-on-bad-input) live as separate
// named tests below — these cannot share the table's read-back assertion
// pattern.
func TestEnsureMergeAttributes(t *testing.T) {
	type tc struct {
		name string
		// failurePrevented documents what would break without this case
		// passing. Required for new entries; if you can't write one,
		// the case is test theater and shouldn't be added.
		failurePrevented string
		// existing seeds .git/info/attributes before EnsureMergeAttributes
		// runs; "" means the file does not pre-exist.
		existing string
		// wantChanged is the expected return value of EnsureMergeAttributes.
		wantChanged bool
		// mustContain are substrings that must appear in the output.
		mustContain []string
		// mustNotContain are substrings that must NOT appear.
		mustNotContain []string
	}

	cases := []tc{
		{
			name:             "creates_file_when_missing",
			failurePrevented: "fresh ledger has no merge driver, first push wedges on add/add",
			existing:         "",
			wantChanged:      true,
			mustContain:      []string{mergeAttrsHeader, mergeAttrsFooter, "AGENTS.md", "merge=union"},
		},
		{
			name:             "preserves_user_authored_lines",
			failurePrevented: "user who manually configured attributes loses their config the next ox run",
			existing:         "*.bin binary\n",
			wantChanged:      true,
			mustContain:      []string{"*.bin binary", "merge=union"},
		},
		{
			name:             "replaces_stale_managed_block_keeps_user_content",
			failurePrevented: "old managed block from a different version accumulates across runs",
			existing:         "*.bin binary\n" + mergeAttrsHeader + "\nAGENTS.md merge=stale-driver\n" + mergeAttrsFooter + "\n",
			wantChanged:      true,
			mustContain:      []string{"*.bin binary", "merge=union"},
			mustNotContain:   []string{"stale-driver"},
		},
		{
			name:             "recovers_from_truncated_block_missing_footer",
			failurePrevented: "interrupted write or hand-edit causes managed block to grow unbounded each call",
			existing:         mergeAttrsHeader + "\nAGENTS.md merge=truncated\n",
			wantChanged:      true,
			mustContain:      []string{mergeAttrsHeader, mergeAttrsFooter, "merge=union"},
			mustNotContain:   []string{"merge=truncated"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := mkGitDir(t)
			path := filepath.Join(dir, infoAttrsRel)

			if c.existing != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(c.existing), 0o644))
			}

			changed, err := EnsureMergeAttributes(dir)
			require.NoError(t, err)
			require.Equal(t, c.wantChanged, changed, "changed return value")

			data, err := os.ReadFile(path)
			require.NoError(t, err, "read info/attributes")
			got := string(data)

			for _, want := range c.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("[%s] expected output to contain %q\noutput:\n%s\n\nfailure prevented: %s",
						c.name, want, got, c.failurePrevented)
				}
			}
			for _, banned := range c.mustNotContain {
				if strings.Contains(got, banned) {
					t.Errorf("[%s] output unexpectedly contains %q\noutput:\n%s\n\nfailure prevented: %s",
						c.name, banned, got, c.failurePrevented)
				}
			}
		})
	}
}

// TestEnsureMergeAttributes_Idempotent is its own test because the
// assertion (changed=false on second call) is shape-different from the
// table above — it requires running EnsureMergeAttributes twice.
//
// Failure prevented: every CLI command that calls EnsureMergeAttributes
// (push pre-flight, ledger init, doctor) needlessly rewrites the file
// and triggers fsnotify churn / makes audit logs noisy.
func TestEnsureMergeAttributes_Idempotent(t *testing.T) {
	dir := mkGitDir(t)
	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err, "first call")
	changed, err := EnsureMergeAttributes(dir)
	require.NoError(t, err, "second call")
	require.False(t, changed, "expected changed=false on second call")
}

// TestEnsureMergeAttributes_NeverInWorkingTree is its own test because
// the assertion is about a path that must NOT exist — fundamentally
// different from the table's "what's in the file" model.
//
// Failure prevented: regression where someone reverts the path back to
// a tracked .gitattributes in the working tree, breaking every user
// with git-lfs configured globally.
func TestEnsureMergeAttributes_NeverInWorkingTree(t *testing.T) {
	dir := mkGitDir(t)
	_, err := EnsureMergeAttributes(dir)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(dir, ".gitattributes")); !os.IsNotExist(err) {
		t.Errorf("EnsureMergeAttributes must NOT create a tracked .gitattributes; got err=%v", err)
	}
}

// TestEnsureMergeAttributes_RejectsInvalidInput covers the
// fail-fast preconditions (empty path, not-a-git-repo). Returning an
// error here is the contract — the table cases above never trigger this
// path because they all use mkGitDir.
//
// Failure prevented: a refactor that changes how callers compute
// ledgerPath silently writes attributes into a non-git directory where
// git never reads them, leaving the union driver disabled and the
// wedge bug reintroduced.
func TestEnsureMergeAttributes_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{
			name: "empty_path",
			dir:  func(t *testing.T) string { return "" },
		},
		{
			name: "not_a_git_repo",
			dir:  func(t *testing.T) string { return t.TempDir() }, // no .git
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := EnsureMergeAttributes(c.dir(t))
			require.Error(t, err)
		})
	}
}
