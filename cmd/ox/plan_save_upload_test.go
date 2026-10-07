package main

// plan_save_upload_test.go covers `ox plan save` when the LFS store cannot take
// a large plan.html. The failure it prevents: the save commits the plain
// multi-megabyte page (or a pointer for a blob the store never kept) and
// reports success, so the lost upload surfaces weeks later as a wedged push.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
)

// resettingLFSServer answers Batch requests normally but drops the connection on
// every PUT, the way a proxy resetting a large upload does.
func resettingLFSServer(t *testing.T) (*lfs.Client, *atomic.Int32) {
	client, puts, _, _ := recoverableLFSServer(t)
	return client, puts
}

func recoverableLFSServer(t *testing.T) (*lfs.Client, *atomic.Int32, *atomic.Bool, *sync.Map) {
	t.Helper()
	var puts atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	stored := &sync.Map{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			if failing.Load() {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			}
			content, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read upload", http.StatusBadRequest)
				return
			}
			stored.Store(strings.TrimPrefix(r.URL.Path, "/objects/"), content)
			w.WriteHeader(http.StatusOK)
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
			Error   map[string]any `json:"error,omitempty"`
		}
		var objects []object
		for _, o := range request.Objects {
			action := "upload"
			if request.Operation == "download" {
				if _, ok := stored.Load(o.OID); !ok {
					objects = append(objects, object{OID: o.OID, Size: o.Size, Error: map[string]any{"code": 404, "message": "not stored"}})
					continue
				}
				action = "download"
			}
			objects = append(objects, object{OID: o.OID, Size: o.Size, Actions: map[string]any{
				action: map[string]any{"href": server.URL + "/objects/" + o.OID},
			}})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": objects})
	}))
	t.Cleanup(server.Close)
	return lfs.NewClient(server.URL, "oauth2", "token"), &puts, &failing, stored
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

func TestPlanSaveFile_FailedHTMLUploadRetryPreservesPriorRevision(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprintf("offline_retry_%v", offline), func(t *testing.T) {
			root := newPlanCaptureTestRepo(t)
			ledger := initPlanTestLedger(t, root)
			t.Setenv("SAGEOX_AGENT_ID", "")
			client, puts, failing, stored := recoverableLFSServer(t)
			previous := planLFSClientFn
			planLFSClientFn = func(string) *lfs.Client { return client }
			t.Cleanup(func() { planLFSClientFn = previous })
			page := filepath.Join(root, ".context", "plan.html")
			writeLargeAuthoredPage(t, page, "Pending Large Plan")
			if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
				t.Fatal(err)
			}
			plans, err := plan.List(root)
			if err != nil || len(plans) != 1 {
				t.Fatalf("saved plans=%v error=%v", plans, err)
			}
			dir := plans[0].Dir
			prior := make(map[string][]byte)
			for _, name := range []string{"plan.html", "plan.md", "meta.json", "events.jsonl"} {
				prior[name], err = os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
			}
			attempts := puts.Load()
			if offline {
				planLFSClientFn = func(string) *lfs.Client { return nil }
			}
			writeLargeAuthoredPage(t, page, "Changed Pending Large Plan")
			out, err := runPlanSaveCLI(t, "--file", page)
			if err == nil || !strings.Contains(err.Error(), "NOT overwritten") {
				t.Fatalf("retry must refuse to overwrite pending revision: error=%v output=%s", err, out)
			}
			if !offline && puts.Load() <= attempts {
				t.Fatal("retry did not attempt to upload the prior revision")
			}
			if count := ledgerCommitCount(t, ledger); count != "0" {
				t.Fatalf("failed retry committed prior plain HTML: commits=%s", count)
			}
			for name, want := range prior {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Errorf("failed retry changed prior %s: error=%v", name, err)
				}
			}

			// recovery uploads the previous bytes before snapshotting them,
			// then saves the changed page as another pointer-backed revision.
			failing.Store(false)
			planLFSClientFn = func(string) *lfs.Client { return client }
			if _, err := runPlanSaveCLI(t, "--file", page); err != nil {
				t.Fatalf("recovered retry: %v", err)
			}
			if count := ledgerCommitCount(t, ledger); count != "2" {
				t.Fatalf("recovery should snapshot prior then commit revision: commits=%s", count)
			}
			rel, err := filepath.Rel(ledger, filepath.Join(dir, "plan.html"))
			if err != nil {
				t.Fatal(err)
			}
			pointer, err := exec.Command("git", "-C", ledger, "show", "HEAD~1:"+filepath.ToSlash(rel)).Output()
			if err != nil {
				t.Fatal(err)
			}
			oid, _, err := lfs.ParsePointer(string(pointer))
			if err != nil {
				t.Fatalf("prior revision in history is plain HTML rather than a pointer: %v", err)
			}
			blob, ok := stored.Load(strings.TrimPrefix(oid, "sha256:"))
			if !ok || !bytes.Equal(blob.([]byte), prior["plan.html"]) {
				t.Fatal("prior revision's pointer does not resolve to its original uploaded bytes")
			}
			if !lfs.IsPointerFile(filepath.Join(dir, "plan.html")) {
				t.Fatal("recovered revision retained plain large HTML")
			}
		})
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
