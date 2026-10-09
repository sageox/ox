package lfs

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installMissingPointerHook makes the bare remote behave like GitLab's
// pre-receive LFS check: a push is declined when any blob in the pack is an LFS
// pointer naming one of missingOIDs. It inspects every new object (not just the
// tip), which is exactly why intermediate commits can poison a push.
func installMissingPointerHook(t *testing.T, bare string, missingOIDs ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook fixture needs a POSIX shell")
	}
	listPath := filepath.Join(t.TempDir(), "missing-oids.txt")
	require.NoError(t, os.WriteFile(listPath, []byte(strings.Join(missingOIDs, "\n")+"\n"), 0o644))
	hook := `#!/bin/sh
while read old new ref; do
  for oid in $(git rev-list --objects "$new" --not --all | cut -d' ' -f1); do
    [ "$(git cat-file -t "$oid")" = blob ] || continue
    [ "$(git cat-file -s "$oid")" -le 200 ] || continue
    lfs=$(git cat-file blob "$oid" | sed -n 's/^oid sha256://p')
    [ -n "$lfs" ] || continue
    if grep -qx "$lfs" "` + listPath + `"; then
      echo "remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\"." >&2
      exit 1
    fi
  done
done
exit 0
`
	hookPath := filepath.Join(bare, "hooks", "pre-receive")
	require.NoError(t, os.MkdirAll(filepath.Dir(hookPath), 0o755))
	require.NoError(t, os.WriteFile(hookPath, []byte(hook), 0o755))
}

// tryPush runs a plain `git push` and reports whether the remote took it.
func tryPush(t *testing.T, ledger string) (ok bool, output string) {
	t.Helper()
	cmd := exec.Command("git", "-C", ledger, "push", "--quiet")
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

func writeAndCommit(t *testing.T, ledger, message string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(ledger, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
		git(t, ledger, "add", "-f", "--sparse", rel)
	}
	git(t, ledger, "commit", "-m", message, "--no-verify")
}

func metaFor(files string) string {
	return `{"title":"t","files":{` + files + `}}`
}

func metaRef(name, oid string, size int) string {
	return `"` + name + `":{"oid":"sha256:` + oid + `","size":` + strconv.Itoa(size) + `}`
}

// mustPush is for setup: the push is expected to be accepted.
func mustPush(t *testing.T, ledger string) {
	t.Helper()
	ok, out := tryPush(t, ledger)
	require.True(t, ok, "setup push failed: %s", out)
}

// Failure prevented: the reconcile walked the WHOLE working tree, so one
// coworker's already-pushed session whose metadata disagreed with its pointer
// aborted every repair ("metadata reference ... disagrees with missing
// pointer") before it reached the squash that unblocks the push. The paths named
// in the production error were not touched by any unpushed commit.
//
// Observable difference: with a pre-receive hook that rejects the own session's
// missing pointer, the push is accepted after reconcile even though the
// coworker's session is still broken on the remote.
func TestReconcile_PushRangeScope_IgnoresPointersTheRemoteAlreadyHas(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS batch API")
	}
	teammateOID := strings.Repeat("7", 64)
	teammateMetaOID := strings.Repeat("8", 64)
	ownMissingOID := strings.Repeat("a", 64)
	ownPresentOID := strings.Repeat("b", 64)

	tests := []struct {
		name         string
		missing      map[string]int // OIDs the LFS store 404s
		wantRefused  bool
		wantPushable bool
	}{
		{
			name:         "teammate breakage outside range is ignored; the own missing pointer is reported, never removed",
			missing:      map[string]int{teammateOID: http.StatusNotFound, ownMissingOID: http.StatusNotFound},
			wantRefused:  true,
			wantPushable: false,
		},
		{
			name:         "nothing in range is missing: teammate breakage is not this push's problem",
			missing:      map[string]int{teammateOID: http.StatusNotFound},
			wantRefused:  false,
			wantPushable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger, bare := initLedgerWithRemote(t)

			// a coworker's session, already accepted by the remote: its meta
			// registers a different OID than the pointer on disk
			writeAndCommit(t, ledger, "teammate session", map[string]string{
				"sessions/teammate/summary.md": lfsPointerContent(teammateOID, 10),
				"sessions/teammate/meta.json":  metaFor(metaRef("summary.md", teammateMetaOID, 10)),
			})
			mustPush(t, ledger)

			// the local user's own unpushed session
			writeAndCommit(t, ledger, "own session", map[string]string{
				"sessions/own/raw.jsonl": lfsPointerContent(ownMissingOID, 42),
				"sessions/own/trace.md":  lfsPointerContent(ownPresentOID, 43),
				"sessions/own/meta.json": metaFor(metaRef("raw.jsonl", ownMissingOID, 42) + "," + metaRef("trace.md", ownPresentOID, 43)),
			})

			// the remote's pre-receive check and the LFS store agree on what is gone
			var gone []string
			for oid := range tt.missing {
				gone = append(gone, oid)
			}
			installMissingPointerHook(t, bare, gone...)
			client := fakeLFSDownloadServer(t, tt.missing)

			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
				func() (*Client, error) { return client, nil })

			assert.Equal(t, 2, result.ScannedPointers, "only the two pointers introduced by the unpushed commit are examined")
			assert.FileExists(t, filepath.Join(ledger, "sessions", "teammate", "summary.md"), "coworker files are never touched")
			assert.FileExists(t, filepath.Join(ledger, "sessions", "own", "raw.jsonl"), "no pointer is ever removed")
			if tt.wantRefused {
				var unrecoverable *UnrecoverablePointersError
				require.ErrorAs(t, err, &unrecoverable)
				require.Len(t, unrecoverable.Pointers, 1, "only the in-range pointer is reported")
				assert.Equal(t, filepath.Join("sessions", "own", "raw.jsonl"), unrecoverable.Pointers[0].Path)
			} else {
				require.NoError(t, err, "pointers outside @{u}..HEAD must never abort the repair")
			}

			ok, out := tryPush(t, ledger)
			assert.Equal(t, tt.wantPushable, ok, "push after reconcile: %s", out)
		})
	}
}

// Negative control for the test above: the whole-tree variant still sees the
// coworker's broken session, so the scoping — not a weaker check — is what lets
// the push-range repair through. `ox doctor` relies on the whole-tree variant to
// clear pointers the remote already holds.
func TestReconcile_WholeTreeScope_StillSeesAlreadyPushedPointers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS batch API")
	}
	teammateOID := strings.Repeat("7", 64)
	teammateMetaOID := strings.Repeat("8", 64)

	setup := func(t *testing.T) (string, *Client) {
		ledger, _ := initLedgerWithRemote(t)
		writeAndCommit(t, ledger, "teammate session", map[string]string{
			"sessions/teammate/summary.md": lfsPointerContent(teammateOID, 10),
			"sessions/teammate/meta.json":  metaFor(metaRef("summary.md", teammateMetaOID, 10)),
		})
		mustPush(t, ledger)
		return ledger, fakeLFSDownloadServer(t, map[string]int{teammateOID: http.StatusNotFound})
	}

	t.Run("whole tree reports the coworker's pointer and removes nothing", func(t *testing.T) {
		ledger, client := setup(t)
		_, err := reconcileAllPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })
		var unrecoverable *UnrecoverablePointersError
		require.ErrorAs(t, err, &unrecoverable)
		assert.Equal(t, "sessions/teammate/summary.md", filepath.ToSlash(unrecoverable.Pointers[0].Path))
		assert.FileExists(t, filepath.Join(ledger, "sessions", "teammate", "summary.md"))
	})

	t.Run("push range has nothing to do", func(t *testing.T) {
		ledger, client := setup(t)
		result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })
		require.NoError(t, err)
		assert.Zero(t, result.ScannedPointers)
		assert.False(t, result.Changed())
	})

	t.Run("whole tree never removes an already-pushed pointer with a dead blob", func(t *testing.T) {
		ledger, _ := initLedgerWithRemote(t)
		writeAndCommit(t, ledger, "plan with a dead blob", map[string]string{
			"data/plans/p1/plan.html": lfsPointerContent(teammateOID, 500),
		})
		mustPush(t, ledger)
		client := fakeLFSDownloadServer(t, map[string]int{teammateOID: http.StatusNotFound})

		result, err := reconcileAllPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })
		var unrecoverable *UnrecoverablePointersError
		require.ErrorAs(t, err, &unrecoverable)
		assert.False(t, result.Changed())
		assert.FileExists(t, filepath.Join(ledger, "data", "plans", "p1", "plan.html"))
	})
}

// Failure prevented: a missing object referenced only by an intermediate
// unpushed commit is invisible to a tip-only repair ("no changes"), so the push
// stayed rejected forever. GitLab checks every commit in the pack. The incident
// (ox-zmjc.27): the tip had already been cleaned by a later "replace orphaned
// pointers with empty stubs" commit, but history still carried the pointers.
//
// Observable difference: the pre-receive hook declines the push before reconcile
// and accepts it after.
func TestReconcile_SquashesMissingObjectReferencedOnlyByIntermediateCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS batch API")
	}
	deadOID := strings.Repeat("d", 64)

	ledger, bare := initLedgerWithRemote(t)
	writeAndCommit(t, ledger, "finalize session with a pointer whose upload was lost", map[string]string{
		"sessions/s1/raw.jsonl": lfsPointerContent(deadOID, 987),
		"sessions/s1/meta.json": metaFor(metaRef("raw.jsonl", deadOID, 987)),
	})
	// the tip no longer references the dead object: a later commit dropped the
	// artifact and its metadata reference
	git(t, ledger, "rm", "--quiet", "--sparse", "sessions/s1/raw.jsonl")
	writeAndCommit(t, ledger, "drop orphaned pointer", map[string]string{"sessions/s1/meta.json": `{"title":"t"}`})
	writeAndCommit(t, ledger, "unrelated murmur", map[string]string{"data/murmurs/m1.json": `{"content":"m"}`})
	require.Equal(t, 3, unpushedCount(t, ledger))

	installMissingPointerHook(t, bare, deadOID)
	ok, out := tryPush(t, ledger)
	require.False(t, ok, "precondition: the intermediate commit's pointer must poison the push")
	require.Contains(t, out, "LFS objects are missing")

	client := fakeLFSDownloadServer(t, map[string]int{deadOID: http.StatusNotFound})
	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })
	require.NoError(t, err)

	assert.Equal(t, 1, result.HistoryOnly, "the dead object is referenced only by history")
	assert.True(t, result.Squashed, "squash is the whole repair")
	assert.True(t, result.Changed(), "callers retry the push only when Changed() says so")
	assert.Equal(t, 1, unpushedCount(t, ledger))

	ok, out = tryPush(t, ledger)
	assert.True(t, ok, "push must be accepted once the poisoned commit is squashed away: %s", out)
	assert.FileExists(t, filepath.Join(ledger, "data", "murmurs", "m1.json"), "squash must keep every unpushed change")
}

// Failure prevented: the repair rewrites the user's working copy, so it must not
// destroy a pointer path whose file no longer matches what was committed, and it
// must not guess about a missing object it has no way to clean up.
func TestReconcile_RefusesWhatItCannotRepairSafely(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS batch API")
	}
	deadOID := strings.Repeat("c", 64)

	tests := []struct {
		name    string
		files   map[string]string
		mutate  func(t *testing.T, ledger string)
		wantErr string
		path    string
	}{
		{
			name:  "working copy no longer matches the committed pointer",
			files: map[string]string{"sessions/s1/raw.jsonl": lfsPointerContent(deadOID, 5)},
			mutate: func(t *testing.T, ledger string) {
				writeFile(t, filepath.Join(ledger, "sessions/s1/raw.jsonl"), "hydrated real content\n")
			},
			wantErr: "working copy differs from the committed pointer",
			path:    "sessions/s1/raw.jsonl",
		},
		{
			name:    "pointer outside the repairable trees",
			files:   map[string]string{"data/other/blob.bin": lfsPointerContent(deadOID, 5)},
			wantErr: "outside sessions/ and data/plans/",
			path:    "data/other/blob.bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)
			writeAndCommit(t, ledger, "unpushed pointer", tt.files)
			if tt.mutate != nil {
				tt.mutate(t, ledger)
			}
			headBefore := git(t, ledger, "rev-parse", "HEAD")
			before, readErr := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(tt.path)))
			require.NoError(t, readErr)
			client := fakeLFSDownloadServer(t, map[string]int{deadOID: http.StatusNotFound})

			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

			require.ErrorContains(t, err, tt.wantErr)
			assert.False(t, result.Squashed)
			assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
			after, readErr := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(tt.path)))
			require.NoError(t, readErr)
			assert.Equal(t, before, after, "nothing may be modified before the refusal")
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// Failure prevented: metadata disagreement is reported for whichever coworker
// session the map iteration reached first, so the operator saw a different path
// every run and could not tell one stuck path from many. In range, disagreement
// still fails closed (never silently rewriting metadata) — but deterministically.
func TestReconcile_InRangeMetadataDisagreementIsReportedInSortedOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote and mocked LFS batch API")
	}
	oidA := strings.Repeat("1", 64)
	oidB := strings.Repeat("2", 64)
	other := strings.Repeat("9", 64)

	ledger, _ := initLedgerWithRemote(t)
	files := map[string]string{}
	for _, name := range []string{"zeta", "alpha", "mid", "beta"} {
		files["sessions/"+name+"/raw.jsonl"] = lfsPointerContent(oidA, 5)
		files["sessions/"+name+"/meta.json"] = metaFor(metaRef("raw.jsonl", other, 5))
	}
	files["sessions/gamma/raw.jsonl"] = lfsPointerContent(oidB, 5)
	files["sessions/gamma/meta.json"] = metaFor(metaRef("raw.jsonl", other, 5))
	writeAndCommit(t, ledger, "unpushed sessions with stale metadata", files)

	client := fakeLFSDownloadServer(t, map[string]int{oidA: http.StatusNotFound, oidB: http.StatusNotFound})
	for run := 0; run < 20; run++ {
		_, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })
		require.Error(t, err)
		require.ErrorContains(t, err, filepath.Join("sessions", "alpha", "raw.jsonl"), "run %d: the first sorted path must be the one reported", run)
	}
}

// Failure prevented: reset --soft to an upstream HEAD does not contain commits
// the index back over whatever HEAD lacks, reverting a coworker's work.
func TestSquashUnpushed_RefusesDivergedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote")
	}
	ledger, bare := initLedgerWithRemote(t)
	writeAndCommit(t, ledger, "local one", map[string]string{"a.txt": "a"})
	writeAndCommit(t, ledger, "local two", map[string]string{"b.txt": "b"})

	// a coworker pushes, and this clone fetches without rebasing
	other := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", "--quiet", bare, other).Run())
	git(t, other, "config", "user.email", "o@test.local")
	git(t, other, "config", "user.name", "Other")
	writeAndCommit(t, other, "coworker commit", map[string]string{"coworker.txt": "c"})
	git(t, other, "push", "--quiet")
	git(t, ledger, "fetch", "--quiet")
	headBefore := git(t, ledger, "rev-parse", "HEAD")

	err := squashUnpushed(context.Background(), ledger, "squash")

	require.ErrorContains(t, err, "not an ancestor")
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
}
