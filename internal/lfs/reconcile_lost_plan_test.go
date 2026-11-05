package lfs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lostPlan = "2026-10-24-launch-mockup"

// lostPlanFiles is a saved plan whose page pointer names a blob that exists
// nowhere: what a pre-fix `ox plan save` leaves once the store drops the blob.
func lostPlanFiles(oid string) map[string]string {
	dir := "data/plans/" + lostPlan + "/"
	return map[string]string{
		dir + "plan.md":      "# Launch mockup\n",
		dir + "meta.json":    `{"schema_version":"v1","slug":"launch-mockup","primary":"html"}` + "\n",
		dir + "events.jsonl": `{"plan_id":"pln_01","event":"created"}` + "\n",
		dir + "plan.html":    lfsPointerContent(oid, 2196945),
	}
}

func lostPlanHTML(ledger string) string {
	return filepath.Join(ledger, "data", "plans", lostPlan, "plan.html")
}

func headFile(t *testing.T, ledger, path string) []byte {
	t.Helper()
	out, err := exec.Command("git", "-C", ledger, "show", "HEAD:"+path).Output()
	require.NoError(t, err, "HEAD:%s", path)
	return out
}

// --- A. The repair ---

// TestReconcile_LostPlanPageIsTombstonedInUnpushedHistory: a plan page whose
// blob the store does not have and no local copy holds is replaced by a
// tombstone page in unpushed history, the unpushed commits are squashed so no
// commit in the push still carries the dead pointer, and nothing else changes.
// Failure prevented: one lost page refusing every push from the clone for good,
// with every later plan, session and murmur queued behind it.
func TestReconcile_LostPlanPageIsTombstonedInUnpushedHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, bare := initLedgerWithRemote(t)
	upstream := git(t, ledger, "rev-parse", "@{upstream}")
	oid := strings.Repeat("6", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
	writeAndCommit(t, ledger, "murmur: queued behind the wedge", map[string]string{"data/murmurs/m1.json": `{"text":"later work"}` + "\n"})
	before := map[string][]byte{}
	for _, name := range []string{"plan.md", "meta.json", "events.jsonl"} {
		before[name] = headFile(t, ledger, "data/plans/"+lostPlan+"/"+name)
	}
	var logs bytes.Buffer
	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})

	result, err := reconcileUnpushedPointers(context.Background(), ledger, slog.New(slog.NewTextHandler(&logs, nil)),
		func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.LostPlanRenders)
	assert.True(t, result.Squashed)
	assert.True(t, result.Changed(), "the push must be retried")

	tombstone := headFile(t, ledger, "data/plans/"+lostPlan+"/plan.html")
	_, _, parseErr := ParsePointer(string(tombstone))
	assert.Error(t, parseErr, "the tombstone must not read as a pointer")
	assert.Greater(t, len(tombstone), maxPointerSize, "GitLab only checks pointer-sized blobs")
	assert.Contains(t, string(tombstone), renderLostMetaName)
	assert.Contains(t, string(tombstone), "sha256:"+oid, "the lost object stays named")
	for name, content := range before {
		assert.Equal(t, content, headFile(t, ledger, "data/plans/"+lostPlan+"/"+name), "%s must be untouched", name)
	}
	assert.Equal(t, `{"text":"later work"}`+"\n", string(headFile(t, ledger, "data/murmurs/m1.json")), "queued work survives the squash")

	// pushed history is never rewritten: one new commit on the unchanged upstream
	assert.Equal(t, upstream, git(t, ledger, "rev-parse", "@{upstream}"))
	assert.Equal(t, upstream, git(t, bare, "rev-parse", "HEAD"))
	assert.Equal(t, upstream, git(t, ledger, "rev-parse", "HEAD^"))
	set, err := scanPushRange(context.Background(), ledger, slog.Default())
	require.NoError(t, err)
	for _, p := range set.repairable {
		assert.NotEqual(t, oid, p.ref.BareOID(), "the dead pointer is still in the push")
	}
	assert.NotContains(t, set.historyOnly, oid, "the dead pointer is still in the push")

	// the working copy matches, so no later `git add` re-commits the pointer
	assert.Equal(t, tombstone, mustReadFile(t, lostPlanHTML(ledger)))
	assert.Empty(t, git(t, ledger, "status", "--porcelain", "--", "data/plans"))

	// the pre-squash history stays reachable locally: the backup is the
	// tombstone commit, and its parent still holds the dead pointer
	backups := strings.Fields(git(t, ledger, "for-each-ref", "--format=%(refname)", "refs/ox-backup/"))
	require.Len(t, backups, 1)
	assert.Equal(t, lfsPointerContent(oid, 2196945), git(t, ledger, "show", backups[0]+"~1:data/plans/"+lostPlan+"/plan.html")+"\n")

	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "plan="+lostPlan)
}

// TestReconcile_LostPlanPageRepairIgnoresAStagedArtifactItCannotUpload: the
// repair runs even while the index holds the coworker's own session artifact
// that the secret guard refuses to upload, and leaves that artifact staged and
// uncommitted. Failure prevented: the 2026-11-02 wedge, where every reconcile
// stopped at that artifact and never reached the lost page.
func TestReconcile_LostPlanPageRepairIgnoresAStagedArtifactItCannotUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("7", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
	credential := []byte(`{"content":"` + "AKIA" + "QWERTYUIOPASDFGH" + `"}` + "\n")
	stagePlainSession(t, ledger, "own", "person-a", credential)
	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})
	ctx := withReconcileOwner(context.Background(), "person-a")

	result, err := reconcileUnpushedPointers(ctx, ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.LostPlanRenders)
	staged, readErr := exec.Command("git", "-C", ledger, "show", ":sessions/own/raw.jsonl").Output()
	require.NoError(t, readErr)
	assert.Equal(t, credential, staged, "the refused artifact stays staged as it was")
	_, headErr := exec.Command("git", "-C", ledger, "cat-file", "-e", "HEAD:sessions/own/raw.jsonl").Output()
	assert.Error(t, headErr, "the refused artifact must not ride along in the repair")
}

// TestReconcile_LostPlanPageAndOwnRawArtifactAreBothRepaired: the coworker's
// own session artifact committed plain in unpushed history is still uploaded
// and dehydrated when a lost plan page is repaired in the same run. Failure
// prevented: the tip validation refusing the plain artifact on every run, so
// neither repair ever happens.
func TestReconcile_LostPlanPageAndOwnRawArtifactAreBothRepaired(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("5", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
	recording := []byte(`{"type":"user","content":"finalize committed this before its upload landed"}` + "\n")
	stagePlainSession(t, ledger, "own", "person-a", recording)
	git(t, ledger, "commit", "-m", "finalize session own", "--no-verify")
	client, store := newFakeUploadStore(t, false, false)
	ctx := withReconcileOwner(context.Background(), "person-a")

	result, err := reconcileUnpushedPointers(ctx, ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.LostPlanRenders)
	assert.True(t, result.Squashed)
	ref := NewFileRef(recording)
	assert.Equal(t, FormatPointer(ref.OID, ref.Size), string(headFile(t, ledger, "sessions/own/raw.jsonl")))
	assert.Equal(t, recording, store.stored[ref.BareOID()], "the recording reached the store")
	assert.Contains(t, string(headFile(t, ledger, "data/plans/"+lostPlan+"/plan.html")), renderLostMetaName)
}

// --- B. What it never touches ---

// TestReconcile_LostPlanPageRepairFailsClosed: the repair runs only when lost
// plan pages are the whole problem. Next to an unrecoverable session, or when
// the page is already on the remote, nothing changes. Failure prevented: a plan
// rule reaching sacred session pointers or pushed history.
func TestReconcile_LostPlanPageRepairFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ledger, oid string) map[string]int
		check func(t *testing.T, result *ReconcileResult, err error)
	}{
		{
			name: "beside an unrecoverable session",
			setup: func(t *testing.T, ledger, oid string) map[string]int {
				writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
				_, _, _, session := commitMissingSessionPointer(t, ledger, "lost-recording", []byte("gone\n"))
				return map[string]int{oid: http.StatusNotFound, session.BareOID(): http.StatusNotFound}
			},
			check: func(t *testing.T, result *ReconcileResult, err error) {
				var unrecoverable *UnrecoverablePointersError
				require.ErrorAs(t, err, &unrecoverable)
				assert.Len(t, unrecoverable.Pointers, 2)
				assert.Zero(t, result.LostPlanRenders)
			},
		},
		{
			// a copy that cannot be inspected is not proof there is none
			name: "plan recovery cache unreadable",
			setup: func(t *testing.T, ledger, oid string) map[string]int {
				writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
				slot := RecoveryCachePath(ledger, "data/plans/"+lostPlan+"/plan.html")
				// a regular file where the slot's directory belongs fails Lstat with
				// ENOTDIR on every platform
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Dir(slot)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Dir(slot), []byte("not a directory"), 0o600))
				return map[string]int{oid: http.StatusNotFound}
			},
			check: func(t *testing.T, result *ReconcileResult, err error) {
				require.ErrorContains(t, err, "inspect plan recovery cache")
				assert.Zero(t, result.LostPlanRenders)
			},
		},
		{
			name: "already pushed",
			setup: func(t *testing.T, ledger, oid string) map[string]int {
				writeAndCommit(t, ledger, "plan: page", lostPlanFiles(oid))
				git(t, ledger, "push", "--quiet")
				writeAndCommit(t, ledger, "plan: edit text", map[string]string{"data/plans/" + lostPlan + "/plan.md": "# edited\n"})
				return map[string]int{oid: http.StatusNotFound}
			},
			check: func(t *testing.T, result *ReconcileResult, err error) {
				require.NoError(t, err)
				assert.Zero(t, result.ScannedPointers, "a pushed pointer is outside the push range")
				assert.False(t, result.Changed())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)
			oid := strings.Repeat("8", 64)
			codes := tc.setup(t, ledger, oid)
			head := git(t, ledger, "rev-parse", "HEAD")
			index := git(t, ledger, "write-tree")
			pointer := mustReadFile(t, lostPlanHTML(ledger))
			client := fakeLFSDownloadServer(t, codes)

			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

			tc.check(t, result, err)
			assert.Equal(t, head, git(t, ledger, "rev-parse", "HEAD"))
			assert.Equal(t, index, git(t, ledger, "write-tree"))
			assert.Equal(t, pointer, mustReadFile(t, lostPlanHTML(ledger)))
		})
	}
}

func TestIsPlanRenderPath(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"data/plans/p/plan.html":                         true,
		filepath.Join("data", "plans", "p", "plan.html"): true,
		"data/plans/p/plan.md":                           false,
		"data/plans/p/companions/plan.html":              false,
		"data/plans/plan.html":                           false,
		"sessions/s/plan.html":                           false,
		"data/plans/../plan.html":                        false,
	} {
		assert.Equal(t, want, isPlanRenderPath(path), path)
	}
}

// --- C. Reading it without repairing ---

// TestFindMissingPlanRenders_ReportsWithoutChanging: the read-only finder doctor
// uses names each unpushed plan page the store lacks, says whether a local copy
// can restore it, and changes nothing. A push range with no plan page pointer
// never builds a client.
func TestFindMissingPlanRenders_ReportsWithoutChanging(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)

	none, err := findMissingPlanRenders(context.Background(), ledger, func() (*Client, error) {
		t.Fatal("no plan page pointer: the store must not be asked")
		return nil, nil
	})
	require.NoError(t, err)
	assert.Empty(t, none)

	lostOID := strings.Repeat("9", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(lostOID))
	kept := []byte("<!doctype html><title>kept</title>\n")
	keptRef := NewFileRef(kept)
	writeAndCommit(t, ledger, "plan: kept page", map[string]string{"data/plans/kept/plan.html": FormatPointer(keptRef.OID, keptRef.Size)})
	keptCache := RecoveryCachePath(ledger, "data/plans/kept/plan.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(keptCache), 0o700))
	require.NoError(t, os.WriteFile(keptCache, kept, 0o600))
	head := git(t, ledger, "rev-parse", "HEAD")
	client := fakeLFSDownloadServer(t, map[string]int{lostOID: http.StatusNotFound, keptRef.BareOID(): http.StatusNotFound})

	missing, err := findMissingPlanRenders(context.Background(), ledger, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, []MissingPlanRender{
		{Path: "data/plans/" + lostPlan + "/plan.html", OID: "sha256:" + lostOID, Size: 2196945},
		{Path: "data/plans/kept/plan.html", OID: keptRef.OID, Size: keptRef.Size, Recoverable: true},
	}, missing)
	assert.Equal(t, head, git(t, ledger, "rev-parse", "HEAD"))
}

// TestFindMissingPlanRenders_FindsTheCommittedRevisionAndHistory: the report
// covers a plan page whose pointer sits only in an earlier unpushed commit,
// and finds the committed revision's copy after a later save replaced the
// slot. Failure prevented: doctor reporting "every plan page is in the store"
// while the push still refused, and a failed later commit leaving the
// committed pointer with no copy.
func TestFindMissingPlanRenders_FindsTheCommittedRevisionAndHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	rel := "data/plans/" + lostPlan + "/plan.html"

	earlierOID := strings.Repeat("5", 64)
	writeAndCommit(t, ledger, "plan: first revision", lostPlanFiles(earlierOID))
	committed := []byte("<!doctype html><title>committed</title>\n")
	committedRef := NewFileRef(committed)
	writeAndCommit(t, ledger, "plan: second revision", map[string]string{rel: FormatPointer(committedRef.OID, committedRef.Size)})
	slot := RecoveryCachePath(ledger, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(slot), 0o700))
	require.NoError(t, os.WriteFile(slot, []byte("<!doctype html><title>uncommitted third</title>\n"), 0o600))
	require.NoError(t, os.WriteFile(PreviousRecoveryCachePath(ledger, rel), committed, 0o600))
	client := fakeLFSDownloadServer(t, map[string]int{earlierOID: http.StatusNotFound, committedRef.BareOID(): http.StatusNotFound})

	missing, err := findMissingPlanRenders(context.Background(), ledger, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, []MissingPlanRender{
		{Path: rel, OID: committedRef.OID, Size: committedRef.Size, Recoverable: true},
		{Path: rel, OID: "sha256:" + earlierOID, Size: 2196945, HistoryOnly: true},
	}, missing)
}

// TestReconcile_LostPlanPageRepairKeepsAStagedEdit: a coworker's staged edit
// of a lost page's plan.html stops the repair with the edit still staged.
// Failure prevented: the tombstone overwrote the staged edit in the index, and
// a failed commit then reset the path to HEAD, so the edit was gone.
func TestReconcile_LostPlanPageRepairKeepsAStagedEdit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("4", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
	// staged while the working copy is still the committed pointer, which the
	// working-copy check alone does not catch
	pointer := mustReadFile(t, lostPlanHTML(ledger))
	edit := filepath.Join(t.TempDir(), "edit.html")
	require.NoError(t, os.WriteFile(edit, []byte("<!doctype html><title>staged edit</title>\n"), 0o644))
	blob := git(t, ledger, "hash-object", "-w", edit)
	git(t, ledger, "update-index", "--cacheinfo", "100644,"+blob+",data/plans/"+lostPlan+"/plan.html")
	head := git(t, ledger, "rev-parse", "HEAD")
	index := git(t, ledger, "write-tree")
	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.ErrorContains(t, err, "has staged changes")
	assert.Zero(t, result.LostPlanRenders)
	assert.Equal(t, head, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, index, git(t, ledger, "write-tree"), "the staged edit must stay staged")
	assert.Equal(t, pointer, mustReadFile(t, lostPlanHTML(ledger)))
}

// TestTombstoneLostPlanRenders_StagingFailureLeavesTheIndexAsItWas: a
// tombstone that cannot be staged unstages the ones before it. Failure
// prevented: an uncommitted tombstone left in the index, riding along with the
// next commit.
func TestTombstoneLostPlanRenders_StagingFailureLeavesTheIndexAsItWas(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("3", 64)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(oid))
	head := git(t, ledger, "rev-parse", "HEAD")
	index := git(t, ledger, "write-tree")
	ref := FileRef{OID: "sha256:" + oid, Size: 2196945}
	lost := []pointerEntry{
		{relPath: filepath.FromSlash("data/plans/" + lostPlan + "/plan.html"), ref: ref},
		{relPath: filepath.FromSlash(".git/plans/x/plan.html"), ref: ref}, // git refuses to stage under .git
	}

	err := tombstoneLostPlanRenders(context.Background(), ledger, lost, slog.Default())

	require.ErrorContains(t, err, "stage tombstone for .git/plans/x/plan.html")
	assert.Equal(t, head, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, index, git(t, ledger, "write-tree"), "the first tombstone must be unstaged")
	require.NoError(t, unstage(context.Background(), ledger, nil))
}

// TestRewriteWorkingPointer: only the exact committed pointer is replaced by
// the tombstone. Failure prevented: overwriting a coworker's edit made while
// the repair ran.
func TestRewriteWorkingPointer(t *testing.T) {
	ref := FileRef{OID: "sha256:" + strings.Repeat("2", 64), Size: 2196945}
	tombstone := []byte("<!doctype html><title>tombstone</title>\n")
	for _, tc := range []struct {
		name    string
		current *string
		wantErr string
		want    string
	}{
		{name: "absent stays absent"},
		{name: "pointer is replaced", current: ptr(FormatPointer(ref.OID, ref.Size)), want: string(tombstone)},
		{name: "edit is kept", current: ptr("someone's edit\n"), wantErr: "working copy changed", want: "someone's edit\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.html")
			if tc.current != nil {
				require.NoError(t, os.WriteFile(path, []byte(*tc.current), 0o644))
			}

			err := rewriteWorkingPointer(path, ref, tombstone)

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			got, readErr := os.ReadFile(path)
			if tc.current == nil {
				assert.ErrorIs(t, readErr, os.ErrNotExist)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		require.Error(t, rewriteWorkingPointer(t.TempDir(), ref, tombstone), "a directory is not a working copy")
	})
}

func ptr(s string) *string { return &s }

// TestFindMissingPlanRenders_InspectionFailureIsNotNothingMissing: a store
// that cannot be reached is an error, never an empty report.
func TestFindMissingPlanRenders_InspectionFailureIsNotNothingMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories")
	}
	useBundledSessionRules(t)
	ledger, _ := initLedgerWithRemote(t)
	writeAndCommit(t, ledger, "plan: lost page", lostPlanFiles(strings.Repeat("1", 64)))

	missing, err := findMissingPlanRenders(context.Background(), ledger, func() (*Client, error) {
		return nil, errors.New("no git credentials found")
	})

	require.ErrorContains(t, err, "no git credentials found")
	assert.Nil(t, missing)
}
