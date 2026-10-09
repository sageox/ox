package main

// plan_save_upload_test.go covers `ox plan save` when the LFS store cannot take
// a large plan.html. The failure it prevents: the save commits the plain
// multi-megabyte page (or a pointer for a blob the store never kept) and
// reports success, so the lost upload surfaces weeks later as a wedged push.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
)

// resettingLFSServer answers Batch requests normally but drops the connection on
// every PUT, the way a proxy resetting a large upload does.
func resettingLFSServer(t *testing.T) (*lfs.Client, *atomic.Int32) {
	t.Helper()
	var puts atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
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
	return lfs.NewClient(server.URL, "oauth2", "token"), &puts
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
	client, puts := resettingLFSServer(t)
	prevClient := planLFSClientFn
	planLFSClientFn = func(string) *lfs.Client { return client }
	t.Cleanup(func() { planLFSClientFn = prevClient })

	page := filepath.Join(root, ".context", "plan.html")
	writeLargeAuthoredPage(t, page, "Large Upload Failure")

	out, err := runPlanSaveCLI(t, "--file", page)

	if err != nil {
		t.Fatalf("a failed upload must not fail the command (the bytes are safe): %v", err)
	}
	if puts.Load() < 2 {
		t.Errorf("PUT attempts = %d, want the upload retried once", puts.Load())
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
// commitPlanLocalCtx and the direct commit tracks plan.html.
func TestPlanSaveFile_RetryAfterFailedUploadNeverCommitsPlainHTML(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")
	client, _ := resettingLFSServer(t)
	prevClient := planLFSClientFn
	planLFSClientFn = func(string) *lfs.Client { return client }
	t.Cleanup(func() { planLFSClientFn = prevClient })

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
	if err := commitPlanLocal(ledger, plans[0].Dir, ""); err != nil {
		t.Fatalf("commitPlanLocal: %v", err)
	}
	tracked := runGitInDir(t, ledger, "ls-files", "--", "data/plans")
	if strings.Contains(tracked, "plan.html") {
		t.Fatalf("plain plan.html was committed:\n%s", tracked)
	}
	if !strings.Contains(tracked, "meta.json") {
		t.Errorf("the rest of the plan dir should still commit, got:\n%s", tracked)
	}
}
