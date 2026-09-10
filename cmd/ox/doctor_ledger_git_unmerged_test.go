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

// Doctor must repair the autostash state its own diagnostic tells users to fix.
func TestFixLedgerUnmergedPaths_RepairsAgreeingAutostash(t *testing.T) {
	skipIntegration(t)
	repo := t.TempDir()
	const rel = "sessions/test/meta.json"
	path := filepath.Join(repo, rel)
	mustRunGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"title":""}`+"\n"), 0o644))
	mustRunGit(t, repo, "add", "--sparse", rel)
	mustRunGit(t, repo, "commit", "-m", "base")
	const local = `{"summary_attempts":0,"title":"Recovered"}`
	require.NoError(t, os.WriteFile(path, []byte(local+"\n"), 0o644))
	mustRunGit(t, repo, "stash", "push", "-m", "autostash")
	require.NoError(t, os.WriteFile(path, []byte(`{"title":"Recovered"}`+"\n"), 0o644))
	mustRunGit(t, repo, "commit", "-am", "remote repair")
	out, err := runIsolatedGit(t, repo, "stash", "apply")
	require.Error(t, err, out)
	op, _ := detectInProgressGitOp(repo)
	require.Empty(t, op)
	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	result := fixLedgerUnmergedPaths(repo, parseUnmergedPaths(status))
	require.True(t, result.passed, "%+v", result)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, local, string(data))
	out, err = runIsolatedGit(t, repo, "ls-files", "--unmerged")
	require.NoError(t, err)
	assert.Empty(t, out)
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
	cmd.Env = append(os.Environ(), // safe: isolated git update-index in temp dir, HOME + GIT_CONFIG_* scrubbed
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

// --- E. fixLedgerDirtyWorkdir must not sweep a conflict into a commit ---
//
// Real-git integration test reproducing #749: a `git stash pop` conflict
// leaves UU files with NO MERGE_HEAD/rebase-merge marker (a stash pop isn't
// a resumable operation), so detectInProgressGitOp can't see it and
// checkLedgerCleanWorkdir's own dirty-count excludes U-state entries from
// what triggers the fix. But that exclusion never reached the actual
// `git add -A` inside the fix — so once an UNRELATED dirty file tripped the
// auto-commit, `-A` swept the conflicted file in too and committed the
// literal conflict markers into the ledger.
//
// buildAutostashConflictRepo reproduces that exact shape: a conflicted
// meta.json (UU, real conflict markers, no state-directory marker) sitting
// alongside a genuinely dirty, unrelated untracked file.
func buildAutostashConflictRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	mustRunGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(`{"attempts": 1}`+"\n"), 0644))
	mustRunGit(t, repo, "add", "meta.json")
	mustRunGit(t, repo, "commit", "-m", "base")

	// local uncommitted change to meta.json — this is what autostash stashes
	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(`{"attempts": 2}`+"\n"), 0644))
	mustRunGit(t, repo, "stash", "push", "-m", "autostash", "--", "meta.json")

	// a conflicting change lands on main in the meantime (the daemon's pull)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(`{"attempts": 99}`+"\n"), 0644))
	mustRunGit(t, repo, "commit", "-am", "conflicting upstream change")

	// an unrelated, genuinely dirty file — this is what trips
	// checkLedgerCleanWorkdir's dirty count and triggers the auto-commit
	require.NoError(t, os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("wip\n"), 0644))

	// popping the stash now conflicts on meta.json
	out, err := runIsolatedGit(t, repo, "stash", "pop")
	require.Error(t, err, "stash pop must conflict; got success: %s", out)

	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Contains(t, status, "UU meta.json",
		"test setup did not produce the UU wedge; cannot validate fix")
	require.Contains(t, status, "unrelated.txt",
		"test setup did not produce the unrelated dirty file; cannot validate fix")

	// sanity: NO state-directory marker exists for a stash-pop conflict —
	// this is the whole reason detectInProgressGitOp can't see it
	op, _ := detectInProgressGitOp(repo)
	require.Equal(t, "", op,
		"a stash-pop conflict must have no in-progress op marker; got %q", op)

	return repo
}

// TestFixLedgerDirtyWorkdir_RefusesUnrelatedDirtyFileWithConflictPresent is
// the load-bearing regression for #749. Without the fix, this test commits
// the literal conflict markers into the ledger's history — permanently.
func TestFixLedgerDirtyWorkdir_RefusesUnrelatedDirtyFileWithConflictPresent(t *testing.T) {
	skipIntegration(t)
	repo := buildAutostashConflictRepo(t)

	beforeLog, err := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, err)

	r := fixLedgerDirtyWorkdir(repo, 1)

	assert.False(t, r.passed,
		"must refuse to auto-commit while a conflict is present: %+v", r)

	// no new commit — especially not one containing the markers
	afterLog, err := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, err)
	assert.Equal(t, beforeLog, afterLog,
		"no commit should have been created")

	// the conflict must still be sitting there, unresolved, for
	// checkLedgerUnmergedPaths to pick up next
	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	assert.Contains(t, status, "UU meta.json",
		"conflict must survive the refusal, not be silently resolved")

	// and the working-tree file must still literally contain the markers —
	// proof nothing quietly rewrote it
	content, err := os.ReadFile(filepath.Join(repo, "meta.json"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "<<<<<<<",
		"conflict markers must still be present in the untouched file")
}

// TestFixLedgerDirtyWorkdir_RefusesPlainModifiedFileWithMarkers covers the
// doctor/session-upload parity gap flagged in review: the pre-add U-state
// check above only sees a LIVE conflict. A file that carries marker text but
// was never U-state at all — plain-modified, never went through a real git
// conflict — is invisible to that check both before AND after `git add -A`
// (add just stages it as an ordinary change; there's no U-state to clear).
// firstUnstageableFileInIndex's post-add blob scan is what has to catch this.
func TestFixLedgerDirtyWorkdir_RefusesPlainModifiedFileWithMarkers(t *testing.T) {
	skipIntegration(t)
	repo := t.TempDir()

	mustRunGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(`{"attempts": 1}`+"\n"), 0644))
	mustRunGit(t, repo, "add", "meta.json")
	mustRunGit(t, repo, "commit", "-m", "base")

	// plain, uncommitted modification carrying marker text — never went
	// through a real git conflict, so git status reports it as ordinary " M",
	// never "UU".
	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(
		"{\n<<<<<<< Updated upstream\n\"attempts\": 3,\n=======\n\"attempts\": 2,\n>>>>>>> Stashed changes\n}\n"),
		0644))

	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Empty(t, parseUnmergedPaths(status+"\n"),
		"test setup must produce a plain modification, not a real U-state conflict")
	require.Contains(t, status, "M meta.json",
		"test setup did not produce the expected plain-modified shape")

	beforeLog, err := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, err)

	r := fixLedgerDirtyWorkdir(repo, 1)
	assert.False(t, r.passed,
		"must refuse to auto-commit a plain-modified file that still carries conflict markers: %+v", r)

	afterLog, err := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, err)
	assert.Equal(t, beforeLog, afterLog, "no commit should have been created")
}
