package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs a git command in dir and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), // safe: git needs PATH; identity and writes are isolated to dir.
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-m", "add "+name)
}

// makeStrandedWedge reproduces the bd ox-akab shape: an interactive rebase left
// mid-flight, with session commits landing on a DETACHED HEAD where no branch or
// remote can reach them.
//
// The rebase state directory is deliberately structurally incomplete (an
// autostash entry, no head-name/orig-head), which is the case AbortOrClearRebase
// refuses to --quit out of while HEAD is detached — correctly, since quitting
// would strand the replay. That refusal is exactly why nothing recovered ox-akab
// on its own, and why the rescue branch has to come first.
func makeStrandedWedge(t *testing.T, strandedCommits int) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	commitFile(t, dir, "base.txt", "base")

	// detach, then land commits that exist nowhere else
	gitIn(t, dir, "checkout", "--detach")
	for i := 0; i < strandedCommits; i++ {
		commitFile(t, dir, "session-"+string(rune('a'+i))+".txt", "session data")
	}

	// zombie rebase-merge: autostash only, no head-name / orig-head
	stateDir := filepath.Join(dir, ".git", "rebase-merge")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(stateDir, "autostash"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsRebaseInProgress(dir) {
		t.Fatal("fixture did not produce a rebase-in-progress state")
	}
	return dir
}

// TestRescueThenAbort_PreservesStrandedCommits is the data-loss invariant.
//
// The ledger holds the user's only copy of unpushed sessions. After recovery the
// stranded commits MUST still be reachable — from the rescue branch — whether or
// not the wedge itself could be cleared.
func TestRescueThenAbort_PreservesStrandedCommits(t *testing.T) {
	dir := makeStrandedWedge(t, 3)
	ctx := context.Background()

	before, err := StrandedCommitCount(ctx, dir)
	if err != nil {
		t.Fatalf("counting stranded commits: %v", err)
	}
	if before != 3 {
		t.Fatalf("fixture: expected 3 stranded commits, got %d", before)
	}
	headBefore := gitIn(t, dir, "rev-parse", "HEAD")

	rescueRef, rescueErr := RescueThenAbort(ctx, dir, "test", nil)

	// The rescue branch is the deliverable, and it must exist even when the
	// wedge could not be cleared (which is the ox-akab case: AbortOrClearRebase
	// correctly refuses to --quit a detached HEAD).
	if rescueRef == "" {
		t.Fatalf("no rescue branch created (err=%v)", rescueErr)
	}
	if !strings.HasPrefix(rescueRef, rescueBranchPrefix) {
		t.Errorf("rescue ref %q lacks the greppable %q prefix", rescueRef, rescueBranchPrefix)
	}

	// THE invariant: the commits are still reachable, from a named ref.
	rescueSHA, err := resolveRef(ctx, dir, rescueRef)
	if err != nil {
		t.Fatalf("rescue branch %s does not resolve after recovery: %v", rescueRef, err)
	}
	if rescueSHA != headBefore {
		t.Errorf("rescue branch points at %s, want the pre-recovery HEAD %s", rescueSHA, headBefore)
	}

	// And they are no longer stranded, because a branch now reaches them.
	after, err := StrandedCommitCount(ctx, dir)
	if err != nil {
		t.Fatalf("counting stranded commits after: %v", err)
	}
	if after != 0 {
		t.Errorf("expected 0 stranded commits after rescue (a branch now reaches them), got %d", after)
	}

	// Every original commit must still be present on the rescue branch.
	listed := gitIn(t, dir, "rev-list", "--count", rescueRef)
	if listed != "4" { // base + 3 session commits
		t.Errorf("rescue branch has %s commits, want 4 (base + 3 stranded)", listed)
	}
}

// TestRescueThenAbort_RefusesWhenNothingStranded keeps the function off the
// ordinary path: with nothing at risk there is no reason to litter the repo with
// rescue branches, and the caller should use the normal recovery.
func TestRescueThenAbort_RefusesWhenNothingStranded(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	commitFile(t, dir, "base.txt", "base")

	ref, err := RescueThenAbort(context.Background(), dir, "test", nil)
	if !errors.Is(err, ErrNoStrandedCommits) {
		t.Errorf("expected ErrNoStrandedCommits, got %v", err)
	}
	if ref != "" {
		t.Errorf("expected no rescue branch when nothing is stranded, got %q", ref)
	}
	branches := gitIn(t, dir, "branch", "--list", rescueBranchPrefix+"*")
	if branches != "" {
		t.Errorf("rescue branch created despite nothing being stranded: %q", branches)
	}
}

// TestStrandedCommitCount is the alarm that went unrung for six weeks in
// bd ox-akab: commits reachable only from HEAD, while every ref anyone reads
// stayed behind.
func TestStrandedCommitCount(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  int
	}{
		{
			name: "clean repo on a branch strands nothing",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				gitIn(t, dir, "init", "--initial-branch=main")
				commitFile(t, dir, "a.txt", "a")
				return dir
			},
			want: 0,
		},
		{
			name:  "detached HEAD with two commits strands both",
			setup: func(t *testing.T) string { return makeStrandedWedge(t, 2) },
			want:  2,
		},
		{
			name:  "detached HEAD with five commits strands all five",
			setup: func(t *testing.T) string { return makeStrandedWedge(t, 5) },
			want:  5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StrandedCommitCount(ctx, tc.setup(t))
			if err != nil {
				t.Fatalf("StrandedCommitCount: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d stranded commits, want %d", got, tc.want)
			}
		})
	}
}

// makeAbortableStrandedWedge builds the fixture that can actually DISCRIMINATE
// on ordering: a rebase state that `git rebase --abort` will succeed at, on a
// detached HEAD carrying commits reachable from nowhere else.
//
// This distinction matters and cost a round of red-first to find. In the zombie
// fixture above, abort refuses (correctly) and HEAD never moves — so a rescue
// branch cut before OR after the abort captures the same commit, and an ordering
// test built on it passes even with the ordering inverted. That is test theater.
//
// Here abort SUCCEEDS and resets HEAD to orig-head, so a rescue branch cut
// afterwards would capture the post-abort HEAD and the stranded commits would be
// unreferenced. Only the correct order survives.
func makeAbortableStrandedWedge(t *testing.T) (dir string, strandedHead string) {
	t.Helper()
	dir = t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	commitFile(t, dir, "base.txt", "base")
	origHead := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "--detach")
	commitFile(t, dir, "session-1.txt", "session data")
	commitFile(t, dir, "session-2.txt", "session data")
	strandedHead = gitIn(t, dir, "rev-parse", "HEAD")

	// A COMPLETE rebase-merge state: head-name + orig-head present, so
	// rebaseStateAbortable() is true and `git rebase --abort` will reset HEAD
	// back to orig-head.
	stateDir := filepath.Join(dir, ".git", "rebase-merge")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("head-name", "refs/heads/main")
	write("orig-head", origHead)
	write("onto", origHead)

	if !IsRebaseInProgress(dir) {
		t.Fatal("fixture did not produce a rebase-in-progress state")
	}
	if !rebaseStateAbortable(stateDir) {
		t.Fatal("fixture is not abortable, so it cannot discriminate on ordering")
	}
	return dir, strandedHead
}

// TestRescueThenAbort_RescueBranchPrecedesAbort proves the ORDERING itself.
//
// The rescue branch must capture the PRE-abort HEAD. On an abortable state the
// abort resets HEAD to orig-head, so a rescue cut afterwards would point at the
// wrong commit and the session data would be unreferenced. Inverting the order in
// RescueThenAbort must fail this test — that inversion was run and it does.
func TestRescueThenAbort_RescueBranchPrecedesAbort(t *testing.T) {
	dir, strandedHead := makeAbortableStrandedWedge(t)
	ctx := context.Background()

	rescueRef, err := RescueThenAbort(ctx, dir, "test", nil)
	if rescueRef == "" {
		t.Fatalf("no rescue branch created (err=%v)", err)
	}

	rescueSHA, resolveErr := resolveRef(ctx, dir, rescueRef)
	if resolveErr != nil {
		t.Fatalf("rescue branch does not resolve, so the commits are unreferenced: %v", resolveErr)
	}

	// THE ordering assertion: the branch holds the commits that existed before
	// recovery ran, not whatever HEAD became afterwards.
	if rescueSHA != strandedHead {
		t.Errorf("rescue branch points at %s, want the PRE-abort HEAD %s.\n"+
			"The rescue branch was cut after the abort had already moved HEAD — "+
			"the stranded session commits are now unreferenced.", rescueSHA, strandedHead)
	}

	// And the commits are genuinely still reachable by content, not just by sha.
	files := gitIn(t, dir, "ls-tree", "--name-only", "-r", rescueRef)
	for _, want := range []string{"session-1.txt", "session-2.txt"} {
		if !strings.Contains(files, want) {
			t.Errorf("%s missing from the rescue branch tree: %q", want, files)
		}
	}
}
