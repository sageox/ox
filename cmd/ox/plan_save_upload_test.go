package main

// plan_save_upload_test.go covers `ox plan save` when the LFS store cannot take
// a large plan.html. The failure it prevents: the save commits the plain
// multi-megabyte page (or a pointer for a blob the store never kept) and
// reports success, so the lost upload surfaces weeks later as a wedged push.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
)

// planLFSStore is a fake LFS store for plan.html uploads. Batch requests get
// upload actions, and an upload's verify probe finds the object. A PUT waits
// while hold is open (when the store was made holding), then drops the
// connection when fail is set, the way a proxy resetting a large upload does,
// or keeps the body.
type planLFSStore struct {
	client  *lfs.Client
	puts    atomic.Int32
	fail    atomic.Bool
	putting chan struct{} // gets one value per PUT
	hold    chan struct{} // nil, or closed to let PUTs finish
	stored  sync.Map      // oid -> body
}

func newPlanLFSStore(t *testing.T, holding bool) *planLFSStore {
	t.Helper()
	s := &planLFSStore{putting: make(chan struct{}, 16)}
	if holding {
		s.hold = make(chan struct{})
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			s.puts.Add(1)
			select {
			case s.putting <- struct{}{}:
			default:
			}
			if s.hold != nil {
				<-s.hold
			}
			if s.fail.Load() {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			}
			body, _ := io.ReadAll(r.Body)
			s.stored.Store(path.Base(r.URL.Path), body)
			return
		}
		var request struct {
			Operation string `json:"operation"`
			Objects   []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		type object struct {
			OID     string         `json:"oid"`
			Size    int64          `json:"size"`
			Actions map[string]any `json:"actions,omitempty"`
		}
		var objects []object
		for _, o := range request.Objects {
			objects = append(objects, object{OID: o.OID, Size: o.Size, Actions: map[string]any{
				"upload": map[string]any{"href": server.URL + "/objects/" + o.OID},
			}})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": objects})
	}))
	t.Cleanup(server.Close)
	s.client = lfs.NewClient(server.URL, "oauth2", "token")
	return s
}

// useLFSStore points plan saves and retries at client.
func useLFSStore(t *testing.T, client *lfs.Client) {
	t.Helper()
	prev := planLFSClientFn
	planLFSClientFn = func(string) *lfs.Client { return client }
	t.Cleanup(func() { planLFSClientFn = prev })
}

func writeLargeAuthoredPage(t *testing.T, path, title string) {
	t.Helper()
	writeAuthoredPage(t, path, title)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// well past the 1 MiB LFS threshold
	if _, err := f.WriteString("<!-- " + strings.Repeat("large render padding. ", 70000) + " -->\n"); err != nil {
		t.Fatal(err)
	}
}

func ledgerCommitCount(t *testing.T, ledger string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", ledger, "rev-list", "--all", "--count")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("count ledger commits: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// A save whose large plan.html upload fails commits nothing, keeps the plain
// file on disk, and says so. Red-first: restore the "committing plain" branch
// in savePlanArtifacts and the ledger gains a commit carrying plan.html.
func TestPlanSaveFile_FailedHTMLUploadCommitsNothingAndKeepsPlainFile(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, false)
	store.fail.Store(true)
	useLFSStore(t, store.client)

	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Large Upload Failure")

	out, err := runPlanSaveCLI(t, "--file", page)

	if err != nil {
		t.Fatalf("a failed upload must not fail the command (the bytes are safe): %v", err)
	}
	if store.puts.Load() < 2 {
		t.Errorf("PUT attempts = %d, want the upload retried once", store.puts.Load())
	}
	if !strings.Contains(out, "NOT SHARED") || !strings.Contains(out, "plan.html upload failed; nothing was committed; re-run ox plan save to retry") {
		t.Errorf("output must say the plan was not shared and why, got:\n%s", out)
	}
	if count := ledgerCommitCount(t, ledger); count != "0" {
		t.Fatalf("ledger has %s commit(s); a failed upload must commit nothing", count)
	}
	plans, perr := plan.List(findGitRoot())
	if perr != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", perr, len(plans))
	}
	htmlPath, _, isPointer, exists := plan.PlanHTMLPath(plans[0].Dir)
	if !exists || isPointer {
		t.Fatalf("plain plan.html must remain on disk (exists=%v pointer=%v)", exists, isPointer)
	}
	if info, serr := os.Stat(htmlPath); serr != nil || info.Size() < 1<<20 {
		t.Errorf("plan.html on disk is not the full large render: %v", serr)
	}
}

// Saving the same page twice yields byte-identical plan.html and the same OID:
// the stamp carries no clock or counter, so a retry after a failed upload
// uploads the very object the first attempt tried to store.
func TestPlanSaveFile_StampIsDeterministicAcrossSaves(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")

	page := filepath.Join(root, ".context", "plan.html")
	writeAuthoredPage(t, page, "Deterministic Stamp")

	oidOf := func() string {
		if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
			t.Fatalf("save: %v", err)
		}
		plans, err := plan.List(findGitRoot())
		if err != nil || len(plans) != 1 {
			t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
		}
		htmlPath, _, _, _ := plan.PlanHTMLPath(plans[0].Dir)
		content, err := os.ReadFile(htmlPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}

	first, second := oidOf(), oidOf()

	if first != second {
		t.Fatalf("plan.html OID changed between identical saves: %s != %s", first, second)
	}
}

// A retry after a failed large-HTML upload must not snapshot the plain page as
// a "prior revision", and a later commit pass over the plan dir (pending-push
// flush, doctor) must leave it out of the commit. Red-first: drop the
// dehydrate-before-snapshot guard in snapshotPriorRevision and the second save
// adds a ledger commit holding plan.html; drop the pathspec exclusion in
// commitPlanDirLocked and the direct commit tracks plan.html.
func TestPlanSaveFile_RetryAfterFailedUploadNeverCommitsPlainHTML(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, false)
	store.fail.Store(true)
	useLFSStore(t, store.client)

	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Retry Failed Upload")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// the retry is refused, not committed: the prior render is still unuploaded
	if _, err := runPlanSaveCLI(t, "--file", page); err == nil || !strings.Contains(err.Error(), "awaiting upload") {
		t.Fatalf("second save err = %v, want a refusal naming the pending upload", err)
	}
	if count := ledgerCommitCount(t, ledger); count != "0" {
		t.Fatalf("ledger has %s commit(s) after a retried failed upload; want 0", count)
	}

	plans, err := plan.List(findGitRoot())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
	}
	if err := gitutil.WithRepoLock(context.Background(), ledger, func() error {
		return commitPlanDirLocked(context.Background(), ledger, plans[0].Dir, "")
	}); err != nil {
		t.Fatalf("commit plan dir: %v", err)
	}
	tracked := runGitInDir(t, ledger, "ls-files", "--", "data/plans")
	if strings.Contains(tracked, "plan.html") {
		t.Fatalf("plain plan.html was committed:\n%s", tracked)
	}
	if !strings.Contains(tracked, "meta.json") {
		t.Errorf("the rest of the plan dir should still commit, got:\n%s", tracked)
	}
}

// TestSavePlanArtifacts_UploadsOutsideTheLedgerLock: while a save's plan.html
// upload is stalled, an approval of another plan commits and pushes to the same
// Ledger, and the save then shares its page as a pointer. Failure prevented:
// the save holds the Ledger's repo lock through the upload, so on a stalled
// network the daemon's sync skips its cycles and other Ledger writes give up.
func TestSavePlanArtifacts_UploadsOutsideTheLedgerLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, true)
	useLFSStore(t, store.client)
	release := sync.OnceFunc(func() { close(store.hold) })
	t.Cleanup(release)

	var r planSaveReport
	other := savePlanArtifacts(root, plan.Parse(plan.ExtractMarkdown([]byte(shareTestPage))), plan.Result{}, []byte(shareTestPage), plan.PrimaryHTML, withReport(&r))
	if other == "" || !r.Share.Shared {
		t.Fatalf("save the other plan: dir=%q %+v", other, r.Share)
	}

	pagePath := filepath.Join(t.TempDir(), "plan.html")
	writeLargeAuthoredPage(t, pagePath, "Stalled Upload")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	var stalled planSaveReport
	saved := make(chan string, 1)
	go func() {
		saved <- savePlanArtifacts(root, plan.Parse(plan.ExtractMarkdown(page)), plan.Result{}, page, plan.PrimaryHTML, withReport(&stalled))
	}()
	select {
	case <-store.putting:
	// The save scans the page against both secret catalogs before its first byte
	// is uploaded; under -race on a loaded runner that alone takes ~30s. This is
	// a hang detector, not a performance bound.
	case <-time.After(3 * time.Minute):
		t.Fatal("the save never started its upload")
	}

	approved := make(chan error, 1)
	go func() {
		_, st, err := sharePlanEvent(context.Background(), root, other, plan.EventApproved, plan.PlanEventFields{})
		if err == nil && !st.Shared {
			err = fmt.Errorf("approval not shared: %+v", st)
		}
		approved <- err
	}()
	select {
	case err := <-approved:
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the approval waited on the save's stalled upload: the save held the Ledger's repo lock through it")
	}

	release()
	dir := <-saved
	if dir == "" || !stalled.Share.Shared || !lfs.IsPointerFile(filepath.Join(dir, "plan.html")) {
		t.Fatalf("the save must share its page as a pointer once its upload finishes: dir=%q %+v", dir, stalled.Share)
	}
}

// TestPlanSaveFile_FailedUploadIsRetriedByPrime: a save whose plan.html upload
// fails commits nothing but leaves a retry marker for its plan dir; a retry
// whose upload fails too keeps it, and the retry after the store comes back
// shares the save with its page as a pointer. Failure prevented: a save the
// coworker never re-runs stays unshared for good, since session stop commits
// only the files it writes, or one more failed upload drops the retry.
func TestPlanSaveFile_FailedUploadIsRetriedByPrime(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, false)
	store.fail.Store(true)
	useLFSStore(t, store.client)
	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Retried By Prime")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("save: %v", err)
	}
	markers, err := listPlanPushPending(ledger)
	if err != nil || len(markers) != 1 || len(markers[0].Files) != 0 {
		t.Fatalf("a failed upload must leave a retry marker for the whole plan dir, got %+v (err %v)", markers, err)
	}
	if ok, failed, err := flushPendingPlanPushes(context.Background(), ledger); err != nil || ok != 0 || failed != 1 {
		t.Fatalf("a retry whose upload fails: ok=%d failed=%d err=%v", ok, failed, err)
	}
	if left, err := listPlanPushPending(ledger); err != nil || len(left) != 1 || left[0].FailedAttempts != 2 || !strings.Contains(left[0].LastError, "awaiting upload") {
		t.Fatalf("a retry whose upload fails must keep the marker and count it, got %+v (err %v)", left, err)
	}

	store.fail.Store(false)
	if ok, failed, err := flushPendingPlanPushes(context.Background(), ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	rel := markers[0].PlanDir + "/plan.html"
	ref, err := lfs.ReadPointerFile(filepath.Join(ledger, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("the retry must store plan.html as a pointer: %v", err)
	}
	if _, ok := store.stored.Load(ref.BareOID()); !ok {
		t.Fatal("the pointer must name an uploaded blob")
	}
	if got := runGitInDir(t, remote, "show", "main:"+rel); !strings.Contains(got, ref.BareOID()) {
		t.Fatalf("the retry must share the save with its pointer, remote plan.html is %q", got)
	}
}

// TestPlanSaveFile_EditedRetryAfterAFailedUploadKeepsBothRevisions: after a
// failed upload, a re-save of the edited page uploads both pages before taking
// the lock, so the earlier revision is committed as a pointer before the new
// one lands. Failure prevented: every re-save is refused ("prior plan.html is
// awaiting upload") until the author restores the old page.
func TestPlanSaveFile_EditedRetryAfterAFailedUploadKeepsBothRevisions(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	_, remote := newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	store := newPlanLFSStore(t, false)
	store.fail.Store(true)
	useLFSStore(t, store.client)
	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Edited Retry")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("save: %v", err)
	}
	f, err := os.OpenFile(page, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("<!-- edited -->\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	store.fail.Store(false)
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("re-save of the edited page: %v", err)
	}

	plans, err := plan.List(findGitRoot())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan.List: err=%v n=%d", err, len(plans))
	}
	html := "data/plans/" + filepath.Base(plans[0].Dir) + "/plan.html"
	commits := strings.Fields(runGitInDir(t, remote, "log", "--format=%H", "main", "--", html))
	if len(commits) != 2 {
		t.Fatalf("want the earlier and the edited revision on the remote, got %d commit(s)", len(commits))
	}
	for _, c := range commits {
		oid, _, err := lfs.ParsePointer(runGitInDir(t, remote, "show", c+":"+html) + "\n")
		if _, ok := store.stored.Load(strings.TrimPrefix(oid, "sha256:")); err != nil || !ok {
			t.Fatalf("revision %s must store plan.html as a pointer to an uploaded blob (err %v)", c, err)
		}
	}
}

// TestPlanSaveFile_NoStoreLeavesTheLargePageToARetry: with no content store
// reachable, a save leaves its large plan.html out of the commit with a retry
// marker, and once a store is reachable the retry shares the page as a
// pointer. Failure prevented: nothing retries the page, so it is never shared.
func TestPlanSaveFile_NoStoreLeavesTheLargePageToARetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone + push")
	}
	root := newPlanCaptureTestRepo(t)
	ledger, remote := newSharedLedger(t, root, true)
	t.Setenv("SAGEOX_AGENT_ID", "")
	useLFSStore(t, nil)
	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "No Store")
	if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
		t.Fatalf("save: %v", err)
	}
	markers, err := listPlanPushPending(ledger)
	if err != nil || len(markers) != 1 || len(markers[0].Files) != 0 || !strings.Contains(markers[0].LastError, "no content store is reachable") {
		t.Fatalf("the page left out of the commit must stay owed, got %+v (err %v)", markers, err)
	}
	html := markers[0].PlanDir + "/plan.html"

	store := newPlanLFSStore(t, false)
	useLFSStore(t, store.client)
	if ok, failed, err := flushPendingPlanPushes(context.Background(), ledger); err != nil || ok != 1 || failed != 0 {
		t.Fatalf("flush: ok=%d failed=%d err=%v", ok, failed, err)
	}
	ref, err := lfs.ReadPointerFile(filepath.Join(ledger, filepath.FromSlash(html)))
	if err != nil {
		t.Fatalf("the retry must store plan.html as a pointer: %v", err)
	}
	if got := runGitInDir(t, remote, "show", "main:"+html); !strings.Contains(got, ref.BareOID()) {
		t.Fatalf("the retry must share the page's pointer, remote plan.html is %q", got)
	}
}
