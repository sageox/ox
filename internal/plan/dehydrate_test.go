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

// TestDehydrateHTML_SmallStaysPlain: a sub-threshold render is never pointerized,
// so a dehydrated clone reads it directly.
func TestDehydrateHTML_SmallStaysPlain(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("x"), 1024)
	writePlanHTMLPlain(t, dir, html)
	client, _ := fakeLFSUploadServer(t)

	pointerized, err := DehydrateHTML(dir, html, client)
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

	pointerized, err := DehydrateHTML(dir, html, nil)
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
// plan.html Save wrote stays on disk (content safe) and no poisoned pointer can
// reach the remote. Failure prevented: reintroducing GH #810 via a failed upload.
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

	pointerized, err := DehydrateHTML(dir, html, client)
	if err == nil {
		t.Fatalf("DehydrateHTML: want error on upload failure, got nil")
	}
	if pointerized {
		t.Fatalf("pointerized despite upload failure — content would be lost")
	}
	assertPlainHTML(t, dir, html) // plain render intact, no dangling pointer
}

// TestDehydrateHTML_LargeUploadsThenPointerizes proves the whole fix end to end:
// the blob is actually uploaded to the store AND the on-disk plan.html becomes a
// pointer to it. Order matters — the pointer's OID must be a blob the server now
// holds.
func TestDehydrateHTML_LargeUploadsThenPointerizes(t *testing.T) {
	dir := t.TempDir()
	html := bytes.Repeat([]byte("PRESERVE-ME "), htmlLFSThreshold/12+2)
	writePlanHTMLPlain(t, dir, html)
	client, stored := fakeLFSUploadServer(t)

	pointerized, err := DehydrateHTML(dir, html, client)
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
	// the blob the pointer names must actually be in the store.
	blob, ok := stored.Load(lfs.ComputeOID(html))
	if !ok {
		t.Fatalf("blob for %s not uploaded — pointer would be poisoned", wantOID)
	}
	if !bytes.Equal(blob.([]byte), html) {
		t.Fatalf("uploaded blob differs from the render")
	}
}
