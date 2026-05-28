//go:build !short

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Pure parsing logic ---
//
// These tests cover parseUnmergedPaths / isUnmergedCode in isolation. They
// don't shell out to git, so they run in -short and on any CI box.
// Failure prevented: a regression in the XY-code table that lets a wedge
// (the only failure mode the unmerged-paths check is designed to catch)
// slip past parsing and be reported as "no conflicts."

func TestIsUnmergedCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		x, y byte
		want bool
	}{
		// unmerged codes — every one of these MUST be detected
		{"DD both deleted", 'D', 'D', true},
		{"AU added by us", 'A', 'U', true},
		{"UD deleted by them", 'U', 'D', true},
		{"UA added by them", 'U', 'A', true},
		{"DU deleted by us", 'D', 'U', true},
		{"AA both added", 'A', 'A', true},
		{"UU both modified", 'U', 'U', true},

		// NOT unmerged — these are the most common false-positive risks
		{"untracked", '?', '?', false},
		{"modified workdir", ' ', 'M', false},
		{"staged modify", 'M', ' ', false},
		{"staged + workdir modify", 'M', 'M', false},
		{"added", 'A', ' ', false},
		{"renamed", 'R', ' ', false},
		{"deleted index-only", 'D', ' ', false},
		{"deleted workdir-only", ' ', 'D', false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isUnmergedCode(tc.x, tc.y)
			assert.Equal(t, tc.want, got, "XY=%c%c", tc.x, tc.y)
		})
	}
}

func TestParseUnmergedPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		porcelain string
		want      []unmergedPath
	}{
		{
			name:      "empty",
			porcelain: "",
			want:      nil,
		},
		{
			name:      "clean workdir",
			porcelain: "",
			want:      nil,
		},
		{
			name: "only modified files",
			porcelain: " M sessions/x/raw.jsonl\n" +
				"?? sessions/y/scratch.md\n",
			want: nil,
		},
		{
			name:      "single UU file — the canonical wedge shape",
			porcelain: "UU sessions/2026-05-21T15-42-ryan-OxbDbL/session.md\n",
			want: []unmergedPath{
				{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/session.md"},
			},
		},
		{
			name: "ox-8zd3 incident shape: three UU files",
			porcelain: "UU sessions/2026-05-21T15-42-ryan-OxbDbL/session.md\n" +
				"UU sessions/2026-05-21T15-42-ryan-OxbDbL/summary.json\n" +
				"UU sessions/2026-05-21T15-42-ryan-OxbDbL/summary.md\n",
			want: []unmergedPath{
				{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/session.md"},
				{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/summary.json"},
				{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/summary.md"},
			},
		},
		{
			name: "mixed AA/DD/UU/AU/UA/DU/UD",
			porcelain: "DD del-by-both.txt\n" +
				"AA add-by-both.txt\n" +
				"UU mod-by-both.txt\n" +
				"AU added-by-us.txt\n" +
				"UA added-by-them.txt\n" +
				"DU deleted-by-us.txt\n" +
				"UD deleted-by-them.txt\n",
			want: []unmergedPath{
				{Code: "DD", Path: "del-by-both.txt"},
				{Code: "AA", Path: "add-by-both.txt"},
				{Code: "UU", Path: "mod-by-both.txt"},
				{Code: "AU", Path: "added-by-us.txt"},
				{Code: "UA", Path: "added-by-them.txt"},
				{Code: "DU", Path: "deleted-by-us.txt"},
				{Code: "UD", Path: "deleted-by-them.txt"},
			},
		},
		{
			name: "wedge mixed with benign changes — wedge must still surface",
			porcelain: " M sessions/x/scratch.md\n" +
				"UU sessions/y/session.md\n" +
				"?? sessions/z/notes.md\n" +
				"M  staged-only.txt\n",
			want: []unmergedPath{
				{Code: "UU", Path: "sessions/y/session.md"},
			},
		},
		{
			name:      "garbage line is silently skipped",
			porcelain: "x\nUU good.txt\n",
			want: []unmergedPath{
				{Code: "UU", Path: "good.txt"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUnmergedPaths(tc.porcelain)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- B. detectInProgressGitOp ---
//
// Failure prevented: doctor --fix tries `git merge --abort` when the
// wedge is from a rebase (or vice versa) and the abort fails, leaving
// the user worse off than no fix.

func TestDetectInProgressGitOp(t *testing.T) {
	t.Parallel()

	// helper: create a fake .git dir with the given marker file
	mkRepo := func(t *testing.T, marker string) string {
		root := t.TempDir()
		gitDir := filepath.Join(root, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		if marker != "" {
			if strings.HasSuffix(marker, "/") {
				// directory marker (rebase-merge, rebase-apply)
				require.NoError(t, os.MkdirAll(filepath.Join(gitDir, marker), 0755))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(gitDir, marker), []byte("ref"), 0644))
			}
		}
		return root
	}

	cases := []struct {
		name   string
		marker string
		wantOp string
	}{
		{"no markers", "", ""},
		{"MERGE_HEAD", "MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "REVERT_HEAD", "revert"},
		{"rebase-merge dir", "rebase-merge/", "rebase"},
		{"rebase-apply dir", "rebase-apply/", "rebase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := mkRepo(t, tc.marker)
			op, _ := detectInProgressGitOp(root)
			assert.Equal(t, tc.wantOp, op)
		})
	}
}

// --- C. unmergedPathsFailure shape ---
//
// Failure prevented: the P0 status that surfaces from the doctor must
// be loud (Critical, not Warning) and must mention BOTH the file count
// and the recovery path (`ox doctor --fix`). Without that, the wedge
// surfaces as just another warning in the doctor output and a coworker
// scrolls past it.

func TestUnmergedPathsFailure_LoudAndActionable(t *testing.T) {
	t.Parallel()

	unmerged := []unmergedPath{
		{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/session.md"},
		{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/summary.json"},
		{Code: "UU", Path: "sessions/2026-05-21T15-42-ryan-OxbDbL/summary.md"},
	}
	r := unmergedPathsFailure("Ledger unmerged paths", "/tmp/ledger", unmerged)

	// must be a P0 (Critical), not a warning that gets buried
	assert.False(t, r.passed, "wedge must surface as a failure, not a warning")
	assert.Equal(t, "critical", r.priority,
		"wedge must be priority=critical so it floats to the top of the doctor summary")

	// message must mention the count so coworkers can grep for it
	assert.Contains(t, r.message, "3 unresolved conflict")

	// detail must point at the fix path
	assert.Contains(t, r.detail, "ox doctor --fix",
		"detail must point at the recovery action — without it the user has no path forward")
	assert.Contains(t, r.detail, "UU sessions/2026-05-21T15-42-ryan-OxbDbL/session.md",
		"detail must include a sample path so the failure is reproducible")

	// slug must be attached so --fix-slug can target it
	assert.Equal(t, CheckSlugLedgerUnmergedPaths, r.slug)
}

// --- D. fixLedgerUnmergedPaths integration ---
//
// Real-git integration test. Forces a stuck merge in an isolated git
// repo, then asserts the fix clears it. Uses cmd.Dir to keep the test
// from touching the real $HOME / global git config. NEVER calls
// `git config --global` — the helper above shows the canonical isolation
// pattern.

// runIsolatedGit is a test-local git runner that always pins cmd.Dir to the
// supplied repo and never touches the global config. Tests must not
// modify the host's git identity.
func runIsolatedGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// scrub HOME/XDG_CONFIG_HOME so per-user gitconfig (including merge.tool
	// hooks that could open an editor) can't interfere with the test.
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"XDG_CONFIG_HOME="+filepath.Join(dir, ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := runIsolatedGit(t, dir, args...)
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// buildStuckMergeRepo creates a real git repo with a live MERGE_HEAD + UU
// conflict. Returns the repo path. The returned repo is in EXACTLY the
// state the ox-8zd3 incident left the ledger in.
func buildStuckMergeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	mustRunGit(t, repo, "init", "--initial-branch=main")
	// initial commit on main
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("base\n"), 0644))
	mustRunGit(t, repo, "add", "session.md")
	mustRunGit(t, repo, "commit", "-m", "base")

	// branch off, change file
	mustRunGit(t, repo, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("feature\n"), 0644))
	mustRunGit(t, repo, "commit", "-am", "feature change")

	// back to main, conflicting change
	mustRunGit(t, repo, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("main\n"), 0644))
	mustRunGit(t, repo, "commit", "-am", "main change")

	// trigger a merge that conflicts but does NOT auto-abort
	out, err := runIsolatedGit(t, repo, "merge", "--no-ff", "--no-edit", "feature")
	require.Error(t, err, "merge must conflict; got success: %s", out)

	// sanity: we have a real wedge now
	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Contains(t, status, "UU session.md",
		"test setup did not produce a U-state wedge; cannot validate fix")
	mergeHead := filepath.Join(repo, ".git", "MERGE_HEAD")
	_, err = os.Stat(mergeHead)
	require.NoError(t, err, "MERGE_HEAD must exist for the fix path to engage")

	return repo
}

// TestFixLedgerUnmergedPaths_ClearsMergeHead is the load-bearing
// regression. It reproduces the ox-8zd3 incident (UU files + live
// MERGE_HEAD) and asserts that --fix actually clears the wedge.
//
// Without the fix code, every push-summary on a wedged ledger fails;
// with the fix, the abort restores the ledger to a clean committable
// state. This test runs the abort exactly as the doctor does.
func TestFixLedgerUnmergedPaths_ClearsMergeHead(t *testing.T) {
	skipIntegration(t)
	repo := buildStuckMergeRepo(t)

	// detection must agree it's a merge wedge BEFORE the fix
	op, _ := detectInProgressGitOp(repo)
	require.Equal(t, "merge", op, "test prerequisite: a live merge must be in progress")

	// parse the status output the same way the check does
	statusOut, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	unmerged := parseUnmergedPaths(statusOut + "\n")
	require.NotEmpty(t, unmerged, "wedge must be detected pre-fix")

	// apply the fix — same code path the doctor uses
	r := fixLedgerUnmergedPaths(repo, unmerged)
	assert.True(t, r.passed, "fix must succeed on a live merge wedge: %+v", r)
	assert.Contains(t, r.message, "aborted stuck merge")

	// MERGE_HEAD must be gone (the load-bearing post-condition)
	_, err = os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	assert.True(t, errors.Is(err, os.ErrNotExist),
		"MERGE_HEAD must be removed after fix; if it survives, the next commit will still be blocked")

	// status must be clean (no UU files left)
	postStatus, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	postUnmerged := parseUnmergedPaths(postStatus + "\n")
	assert.Empty(t, postUnmerged, "no UU files may survive the abort; got: %q", postStatus)
}

// TestFixLedgerUnmergedPaths_NoStateMarkers_NoAutoResolve covers the rare
// case where unmerged paths exist with no MERGE_HEAD / rebase / cherry-pick
// in progress. This happens when conflicts are staged manually via
// `git update-index --cacheinfo` and we MUST NOT auto-resolve — the
// right action depends on what the user intended.
func TestFixLedgerUnmergedPaths_NoStateMarkers_NoAutoResolve(t *testing.T) {
	skipIntegration(t)

	repo := t.TempDir()
	mustRunGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("base\n"), 0644))
	mustRunGit(t, repo, "add", "session.md")
	mustRunGit(t, repo, "commit", "-m", "base")

	// stage a 3-way conflict manually via update-index --index-info.
	// This is the supported way to populate stages 1/2/3 for a single path
	// without going through merge/rebase/cherry-pick (and therefore without
	// the corresponding state markers in .git/).
	hashBlob := func(name, content string) string {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0644))
		out, err := runIsolatedGit(t, repo, "hash-object", "-w", name)
		require.NoError(t, err)
		return strings.TrimSpace(out)
	}
	hBase := hashBlob("blob-base", "base\n")
	hOurs := hashBlob("blob-ours", "ours\n")
	hTheirs := hashBlob("blob-theirs", "theirs\n")

	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"HOME="+repo,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	cmd.Stdin = strings.NewReader(
		"100644 " + hBase + " 1\tmanual.txt\n" +
			"100644 " + hOurs + " 2\tmanual.txt\n" +
			"100644 " + hTheirs + " 3\tmanual.txt\n",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "update-index --index-info: %s", string(out))

	// verify we have UU manual.txt with NO state markers
	status, _ := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.Contains(t, status, "manual.txt", "manual conflict not staged: %q", status)
	unmerged := parseUnmergedPaths(status + "\n")
	require.NotEmpty(t, unmerged, "expected unmerged paths from manual --cacheinfo")

	// detect must report no in-progress op (this is the case the docstring covers)
	op, _ := detectInProgressGitOp(repo)
	require.Equal(t, "", op,
		"manually-staged conflicts must have no in-progress op marker; got %q", op)

	// fix must NOT auto-resolve — must surface for human attention
	r := fixLedgerUnmergedPaths(repo, unmerged)
	assert.False(t, r.passed,
		"manual conflict must NOT be auto-resolved; got passed=true which would silently destroy intent")
	assert.Contains(t, r.detail, "manual",
		"detail must explain that human action is required")
}

// TestCheckLedgerUnmergedPaths_ReportsCriticalWithoutFix verifies the
// no-fix path (the path `ox doctor` takes by default). The wedge MUST
// be reported as a critical failure so it floats to the top of the
// doctor summary, not buried as a warning.
//
// This test exercises the in-process check directly by pointing it at
// a tmp repo via the underlying primitives (parseUnmergedPaths +
// unmergedPathsFailure), since checkLedgerUnmergedPaths resolves the
// ledger path from cwd config which we deliberately don't mutate.
func TestCheckLedgerUnmergedPaths_ReportsCriticalWithoutFix(t *testing.T) {
	skipIntegration(t)
	repo := buildStuckMergeRepo(t)

	statusOut, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	unmerged := parseUnmergedPaths(statusOut + "\n")
	require.NotEmpty(t, unmerged)

	r := unmergedPathsFailure("Ledger unmerged paths", repo, unmerged)
	assert.False(t, r.passed)
	assert.Equal(t, "critical", r.priority)
	assert.Equal(t, CheckSlugLedgerUnmergedPaths, r.slug)
}
