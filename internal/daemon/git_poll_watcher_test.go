package daemon

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

// requireGit skips the test if git is unavailable or short mode is set.
// The end-to-end watcher tests shell out to real git and poll, so they exceed
// the fast-tier 500ms budget.
func requireGitAndNotShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: end-to-end git poll watcher test (real git + polling)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// porcelainZ builds a NUL-delimited `git status --porcelain -z` byte stream from
// pre-formatted records (e.g. " M file.go"). Records are joined and terminated
// by NUL, matching git's wire format.
func porcelainZ(records ...string) []byte {
	var out []byte
	for _, r := range records {
		out = append(out, []byte(r)...)
		out = append(out, 0)
	}
	return out
}

// --- A. Porcelain parsing ---

// TestParsePorcelainZ verifies path->XY extraction from NUL-delimited git output,
// including the rename case where the "from" path is a trailing NUL field that
// must not leak into the map as its own entry.
// Failure prevented: a parser regression silently drops or mis-attributes changed
// paths, so murmurs and dirty-overlay rebuilds miss real edits.
func TestParsePorcelainZ(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want map[string]string
	}{
		{
			name: "modified tracked file",
			in:   porcelainZ(" M main.go"),
			want: map[string]string{"main.go": " M"},
		},
		{
			name: "untracked file",
			in:   porcelainZ("?? notes.txt"),
			want: map[string]string{"notes.txt": "??"},
		},
		{
			name: "staged added file",
			in:   porcelainZ("A  new.go"),
			want: map[string]string{"new.go": "A "},
		},
		{
			name: "deleted tracked file",
			in:   porcelainZ(" D gone.go"),
			want: map[string]string{"gone.go": " D"},
		},
		{
			// rename layout: "R  newpath\0oldpath\0" — only the new path lands.
			name: "rename skips from path",
			in:   porcelainZ("R  newpath.go", "oldpath.go"),
			want: map[string]string{"newpath.go": "R "},
		},
		{
			name: "multi-entry mixed",
			in:   porcelainZ(" M a.go", "?? b.txt", "A  c.go", " D d.go"),
			want: map[string]string{
				"a.go":  " M",
				"b.txt": "??",
				"c.go":  "A ",
				"d.go":  " D",
			},
		},
		{
			name: "rename interleaved with other entries",
			in:   porcelainZ(" M a.go", "R  new.go", "old.go", "?? b.txt"),
			want: map[string]string{
				"a.go":   " M",
				"new.go": "R ",
				"b.txt":  "??",
			},
		},
		{
			// Y-column rename ("RM" has X=R; this covers X=M, Y=R, which
			// status.renames can emit for a work-tree rename). The from-path
			// must still be skipped or the next record gets corrupted.
			name: "work-tree-column rename skips from path",
			in:   porcelainZ("MR new.go", "old.go", "?? after.txt"),
			want: map[string]string{
				"new.go":    "MR",
				"after.txt": "??",
			},
		},
		{
			// Copy in the index column also carries a from-path.
			name: "copy skips from path",
			in:   porcelainZ("C  copy.go", "src.go", " M tail.go"),
			want: map[string]string{
				"copy.go": "C ",
				"tail.go": " M",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePorcelainZ(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPorcelainChangeType verifies that each porcelain XY code maps to the right
// ChangeType for murmur labeling.
// Failure prevented: murmurs mislabel changes (e.g. a deletion reported as a
// modification), confusing teammates reading the WIP feed.
func TestPorcelainChangeType(t *testing.T) {
	tests := []struct {
		code string
		want ChangeType
	}{
		{"??", ChangeCreated},
		{"A ", ChangeCreated},
		{"C ", ChangeCreated},
		{" M", ChangeModified},
		{"M ", ChangeModified},
		{"MM", ChangeModified},
		{" D", ChangeDeleted},
		{"D ", ChangeDeleted},
		{"R ", ChangeRenamed},
		{"RM", ChangeRenamed},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			assert.Equal(t, tt.want, porcelainChangeType(tt.code))
		})
	}
}

// --- B. End-to-end against a real git repo ---

// newPollWatcher wires a watcher to a fresh accumulator over a real repo at dir.
func newPollWatcher(t *testing.T) (string, *ChangeAccumulator, *GitPollWatcher) {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	acc := NewChangeAccumulator(20 * time.Millisecond)
	w := NewGitPollWatcher(dir, acc, slogDiscard())
	return dir, acc, w
}

// findChange waits for a settled change matching path+ct, accumulating drains
// across settle windows. Returns true if seen within the deadline.
func findChange(t *testing.T, acc *ChangeAccumulator, path string, ct ChangeType) bool {
	t.Helper()
	return assert.Eventually(t, func() bool {
		for _, fc := range acc.DrainSettled() {
			if fc.Path == path && fc.ChangeType == ct {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
}

// TestPoll_NewUntrackedFile verifies a brand-new file surfaces as a Created event.
// Failure prevented: newly authored files never trigger murmurs or overlay rebuilds.
func TestPoll_NewUntrackedFile(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()

	w.poll(ctx, true) // baseline

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fresh.go"), []byte("package p\n"), 0644))
	w.poll(ctx, false)

	require.True(t, findChange(t, acc, "fresh.go", ChangeCreated), "expected Created for fresh.go")
}

// TestPoll_ModifyTrackedFile verifies editing a committed file surfaces as Modified.
// Failure prevented: edits to existing tracked files go unnoticed by the daemon.
func TestPoll_ModifyTrackedFile(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()

	w.poll(ctx, true) // baseline

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\nmore\n"), 0644))
	w.poll(ctx, false)

	require.True(t, findChange(t, acc, "README.md", ChangeModified), "expected Modified for README.md")
}

// TestPoll_DeleteTrackedFile verifies removing a committed file surfaces as Deleted.
// Failure prevented: file deletions never propagate, leaving stale entries in the
// dirty overlay / index.
func TestPoll_DeleteTrackedFile(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()

	w.poll(ctx, true) // baseline

	require.NoError(t, os.Remove(filepath.Join(dir, "README.md")))
	w.poll(ctx, false)

	require.True(t, findChange(t, acc, "README.md", ChangeDeleted), "expected Deleted for README.md")
}

// TestPoll_BaselineIsSilent verifies that pre-existing dirty state present at the
// first (baseline) poll emits nothing — matching fsnotify "only future changes"
// semantics.
// Failure prevented: daemon startup against an already-dirty tree murmurs the
// entire pre-existing diff as if it all just happened.
func TestPoll_BaselineIsSilent(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()

	// dirty the tree BEFORE the baseline poll
	require.NoError(t, os.WriteFile(filepath.Join(dir, "preexisting.go"), []byte("package p\n"), 0644))

	w.poll(ctx, true) // baseline — must record but emit nothing

	// Give any erroneous settle timer a chance to fire; nothing should appear.
	assert.Never(t, func() bool {
		return acc.PendingCount() != 0 || acc.DrainSettled() != nil
	}, 200*time.Millisecond, 20*time.Millisecond)

	assert.Equal(t, 0, acc.PendingCount())
	assert.Nil(t, acc.DrainSettled())
}

// TestPoll_RepeatedSaveEmitsAgain verifies that a subsequent save to a file that
// is already in the dirty set (same porcelain XY code) still emits, via the
// mtime-advance path.
// Failure prevented: continued editing of a file already counted as dirty stops
// triggering rebuilds, so the overlay goes stale during active work.
func TestPoll_RepeatedSaveEmitsAgain(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()
	path := filepath.Join(dir, "wip.go")

	w.poll(ctx, true) // baseline

	require.NoError(t, os.WriteFile(path, []byte("package p\n"), 0644))
	w.poll(ctx, false)
	require.True(t, findChange(t, acc, "wip.go", ChangeCreated), "expected initial Created for wip.go")

	// Bump mtime well into the future so the advance is visible even on
	// coarse-granularity filesystems — do not rely on a same-second rewrite.
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
	w.poll(ctx, false)

	// XY code is still "??", so the emit must come from the mtime-advance path.
	require.True(t, assert.Eventually(t, func() bool {
		for _, fc := range acc.DrainSettled() {
			if fc.Path == "wip.go" {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond), "expected a second event after mtime advance")
}

// TestPoll_GitignoreHonored verifies that paths excluded by .gitignore never
// produce events, because `git status` omits ignored paths by default.
// Failure prevented: build output / node_modules churn floods murmurs and
// constantly retriggers overlay rebuilds.
func TestPoll_GitignoreHonored(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	ctx := context.Background()

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0644))
	w.poll(ctx, true) // baseline (with .gitignore in place)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "ignored"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored", "x.txt"), []byte("junk\n"), 0644))
	w.poll(ctx, false)

	// An ignored path must never appear, across the full settle window.
	assert.Never(t, func() bool {
		for _, fc := range acc.DrainSettled() {
			if fc.Path == filepath.Join("ignored", "x.txt") {
				return true
			}
		}
		return false
	}, 300*time.Millisecond, 20*time.Millisecond)
}

// --- C. Start lifecycle ---

// TestStart_DeliversThenStopsCleanly verifies the Start loop establishes a silent
// baseline, then delivers post-baseline changes on its ticker, and that canceling
// the context returns the goroutine promptly.
// Failure prevented: the watcher goroutine leaks (never returns on cancel) or
// never delivers events after startup.
func TestStart_DeliversThenStopsCleanly(t *testing.T) {
	requireGitAndNotShort(t)
	dir, acc, w := newPollWatcher(t)
	w.interval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		w.start(ctx, ready)
		close(done)
	}()

	// Wait for Start's silent baseline before creating the file, so live.go is
	// a genuine post-baseline change rather than part of the baseline.
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not establish its baseline")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live.go"), []byte("package p\n"), 0644))
	require.True(t, findChange(t, acc, "live.go", ChangeCreated), "expected Start loop to deliver Created for live.go")

	cancel()
	select {
	case <-done:
		// returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of context cancel — goroutine leak")
	}
}
