package plan

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// writePlanHTMLPlain simulates what Save writes: a plain plan.html on disk.
func writePlanHTMLPlain(t *testing.T, dir string, html []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, planHTMLFile), html, 0o644); err != nil {
		t.Fatalf("seed plain plan.html: %v", err)
	}
}

func assertPlainHTML(t *testing.T, dir string, want []byte) {
	t.Helper()
	p := filepath.Join(dir, planHTMLFile)
	if lfs.IsPointerFile(p) {
		t.Fatalf("plan.html is an LFS pointer, want plain")
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read plan.html: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("plan.html bytes changed: got %d, want %d", len(got), len(want))
	}
}

// fakeLFSUploadServer stands up a minimal Git LFS Batch API server that hands out
// upload actions and stores PUT'd blobs in memory. Returns a client pointed at it
// and the OID->bytes store so a test can prove the blob really landed.
func fakeLFSUploadServer(t *testing.T) (*lfs.Client, *sync.Map) {
	t.Helper()
	stored := &sync.Map{}
	var srv *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string `json:"operation"`
			Objects   []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		type action struct {
			Href string `json:"href"`
		}
		type actions struct {
			Upload *action `json:"upload,omitempty"`
		}
		type obj struct {
			OID     string   `json:"oid"`
			Size    int64    `json:"size"`
			Actions *actions `json:"actions,omitempty"`
		}
		resp := struct {
			Transfer string `json:"transfer"`
			Objects  []obj  `json:"objects"`
		}{Transfer: "basic"}
		for _, o := range req.Objects {
			resp.Objects = append(resp.Objects, obj{
				OID:     o.OID,
				Size:    o.Size,
				Actions: &actions{Upload: &action{Href: srv.URL + "/store/" + o.OID}},
			})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/store/", func(w http.ResponseWriter, r *http.Request) {
		oid := strings.TrimPrefix(r.URL.Path, "/store/")
		body, _ := io.ReadAll(r.Body)
		stored.Store(oid, body)
		w.WriteHeader(http.StatusOK)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return lfs.NewClient(srv.URL, "oauth2", "token"), stored
}

// largeHTMLWithHead builds a >threshold render that carries a <head>, so that
// Save's StampHTMLMeta actually mutates it (the condition that exposed the
// stamped-vs-unstamped bug).
func largeHTMLWithHead() []byte {
	body := strings.Repeat("PRESERVE-ME ", htmlLFSThreshold/12+4)
	return []byte("<html><head></head><body>" + body + "</body></html>")
}

// TestSaveThenDehydrate_RealHeadHTML is the regression test for the stamped-vs-
// unstamped bug: Save writes STAMPED bytes to plan.html, and DehydrateHTML must
// upload/pointerize THOSE bytes — not a caller's pre-stamp copy.
//
// Failure prevented: DehydrateHTML uploads the unstamped bytes, so their OID
// disagrees with the on-disk (stamped) file; guardPointerOverwrite refuses the
// swap, dehydration silently never happens for any real render, and every save
// leaks an orphaned blob. This drives the true path (Save → DehydrateHTML reading
// on disk) with a <head>-bearing render above the threshold.
func TestSaveThenDehydrate_RealHeadHTML(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	html := largeHTMLWithHead()
	meta := Meta{Topic: "Real head render", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir, _, err := Save("/g", Input{Raw: "# H\n"}, sampleResult(), html, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// sanity: Save stamped the on-disk copy, so it differs from the raw html the
	// CLI still holds — this is exactly the divergence the bug tripped over.
	onDisk, err := os.ReadFile(filepath.Join(dir, planHTMLFile))
	if err != nil {
		t.Fatalf("read stamped plan.html: %v", err)
	}
	if bytes.Equal(onDisk, html) {
		t.Fatalf("precondition: expected StampHTMLMeta to modify the <head> render")
	}

	client, stored := fakeLFSUploadServer(t)
	pointerized, err := DehydrateHTML(dir, client)
	if err != nil {
		t.Fatalf("DehydrateHTML errored on a real render (the stamped/unstamped bug): %v", err)
	}
	if !pointerized {
		t.Fatalf("real render was not dehydrated — the on-disk stamped bytes were not the ones uploaded")
	}

	htmlPath := filepath.Join(dir, planHTMLFile)
	ref, err := lfs.ReadPointerFile(htmlPath)
	if err != nil {
		t.Fatalf("plan.html is not a valid pointer after dehydration: %v", err)
	}
	blob, ok := stored.Load(ref.BareOID())
	if !ok {
		t.Fatalf("blob %s not in the store — pointer would be poisoned", ref.BareOID())
	}
	// the uploaded blob is the STAMPED on-disk content, not the caller's raw html.
	if !bytes.Equal(blob.([]byte), onDisk) {
		t.Fatalf("uploaded blob != the stamped file Save wrote")
	}
	if bytes.Equal(blob.([]byte), html) {
		t.Fatalf("uploaded blob is the UNSTAMPED html — the #810 stamped-bytes bug")
	}
	if lfs.ComputeOID(blob.([]byte)) != ref.BareOID() {
		t.Fatalf("pointer OID does not match the stored blob")
	}
}

// TestDehydrateHTML_SmallStaysPlain: a sub-threshold render is never pointerized.
func TestDehydrateHTML_SmallStaysPlain(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("x"), 1024)
	writePlanHTMLPlain(t, dir, html)
	client, _ := fakeLFSUploadServer(t)

	pointerized, err := DehydrateHTML(dir, client)
	if err != nil {
		t.Fatalf("DehydrateHTML: %v", err)
	}
	if pointerized {
		t.Fatalf("small render pointerized, want plain")
	}
	assertPlainHTML(t, dir, html)
}

// TestDehydrateHTML_NilClientStaysPlain: offline / no ledger remote leaves a large
// render plain — bigger in git, but retrievable and pushable, never lost.
func TestDehydrateHTML_NilClientStaysPlain(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("PRESERVE "), htmlLFSThreshold/8+2)
	writePlanHTMLPlain(t, dir, html)

	pointerized, err := DehydrateHTML(dir, nil)
	if err != nil {
		t.Fatalf("DehydrateHTML: %v", err)
	}
	if pointerized {
		t.Fatalf("nil client pointerized, want plain")
	}
	assertPlainHTML(t, dir, html)
}

// TestDehydrateHTML_UploadFailureLeavesPlain is the load-bearing failure-mode
// test: when the upload fails, DehydrateHTML must NOT write a pointer. The plain
// plan.html on disk stays intact (content safe) and no poisoned pointer can reach
// the remote. Failure prevented: reintroducing GH #810 via a failed upload.
func TestDehydrateHTML_UploadFailureLeavesPlain(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("PRESERVE "), htmlLFSThreshold/8+2)
	writePlanHTMLPlain(t, dir, html)

	// server that rejects the batch request → BatchUpload errors.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client := lfs.NewClient(srv.URL, "oauth2", "token")

	pointerized, err := DehydrateHTML(dir, client)
	if err == nil {
		t.Fatalf("DehydrateHTML: want error on upload failure, got nil")
	}
	if pointerized {
		t.Fatalf("pointerized despite upload failure — content would be lost")
	}
	assertPlainHTML(t, dir, html) // plain render intact, no dangling pointer
}

// TestDehydrateHTML_LargeUploadsThenPointerizes proves the plain seed → pointer
// path: the blob is uploaded AND the on-disk plan.html becomes a pointer to it.
func TestDehydrateHTML_LargeUploadsThenPointerizes(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("PRESERVE-ME "), htmlLFSThreshold/12+2)
	writePlanHTMLPlain(t, dir, html)
	client, stored := fakeLFSUploadServer(t)

	pointerized, err := DehydrateHTML(dir, client)
	if err != nil {
		t.Fatalf("DehydrateHTML: %v", err)
	}
	if !pointerized {
		t.Fatalf("large render not pointerized")
	}

	htmlPath := filepath.Join(dir, planHTMLFile)
	if !lfs.IsPointerFile(htmlPath) {
		t.Fatalf("plan.html is not a pointer after dehydration")
	}
	ref, err := lfs.ReadPointerFile(htmlPath)
	if err != nil {
		t.Fatalf("ReadPointerFile: %v", err)
	}
	wantOID := "sha256:" + lfs.ComputeOID(html)
	if ref.OID != wantOID {
		t.Fatalf("pointer OID = %q, want %q", ref.OID, wantOID)
	}
	blob, ok := stored.Load(lfs.ComputeOID(html))
	if !ok {
		t.Fatalf("blob for %s not uploaded — pointer would be poisoned", wantOID)
	}
	if !bytes.Equal(blob.([]byte), html) {
		t.Fatalf("uploaded blob differs from the render")
	}
}

// TestDehydrateHTML_AlreadyPointerIsNoop: a plan.html that is already a pointer
// (a re-save, or a synced dehydrated clone) must not be re-processed.
func TestDehydrateHTML_AlreadyPointerIsNoop(t *testing.T) {
	dir := t.TempDir()
	ref := lfs.AssertUploaded(lfs.FileRef{Storage: "lfs", OID: "sha256:" + lfs.ComputeOID([]byte("blob")), Size: 4})
	require := func(cond bool, msg string) {
		if !cond {
			t.Fatal(msg)
		}
	}
	if err := lfs.WritePointerFile(filepath.Join(dir, planHTMLFile), ref); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}
	client, _ := fakeLFSUploadServer(t)
	pointerized, err := DehydrateHTML(dir, client)
	require(err == nil, "DehydrateHTML on an existing pointer should not error")
	require(!pointerized, "an existing pointer must not be re-pointerized")
}

// TestDehydrateHTML_BoundaryComparator pins the `>` (not `>=`) gate: a render of
// exactly htmlLFSThreshold bytes stays plain.
func TestDehydrateHTML_BoundaryComparator(t *testing.T) {
	cases := []struct {
		name string
		size int
		want bool
	}{
		{"exactly threshold stays plain", htmlLFSThreshold, false},
		{"one over crosses", htmlLFSThreshold + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePlanHTMLPlain(t, dir, bytes.Repeat([]byte("z"), tc.size))
			client, _ := fakeLFSUploadServer(t)
			got, err := DehydrateHTML(dir, client)
			if err != nil {
				t.Fatalf("DehydrateHTML: %v", err)
			}
			if got != tc.want {
				t.Fatalf("pointerized = %v, want %v (size=%d)", got, tc.want, tc.size)
			}
		})
	}
}
