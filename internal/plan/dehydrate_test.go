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

// TestSavedHTML_IsWhatSaveWrites: the page the CLI uploads before taking the
// Ledger's repo lock is byte for byte the plan.html Save then writes, for a
// plan's first save and for a revision, so the upload's pointer can replace it.
// Failure prevented: the upload describes other bytes (the caller's unstamped
// copy, or another plan id), PointerizeHTML finds no match, and every save of
// a large page fails as an upload failure (or, before the OID check, the
// pointer named a blob the store never received: the #810 wedge).
func TestSavedHTML_IsWhatSaveWrites(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	html := largeHTMLWithHead()
	meta := Meta{Topic: "Real head render", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	for _, save := range []string{"first save", "revision"} {
		page, m, err := SavedHTML("/g", meta, html)
		if err != nil {
			t.Fatalf("%s: SavedHTML: %v", save, err)
		}
		if bytes.Equal(page, html) {
			t.Fatalf("%s: precondition: the page must carry Save's stamp", save)
		}
		dir, _, err := Save("/g", Input{Raw: "# H\n"}, sampleResult(), html, m)
		if err != nil {
			t.Fatalf("%s: Save: %v", save, err)
		}
		onDisk, err := os.ReadFile(filepath.Join(dir, planHTMLFile))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(onDisk, page) {
			t.Fatalf("%s: Save wrote other bytes than SavedHTML returned", save)
		}
	}
}

// TestUploadHTML_UploadsOnlyALargePageToAReachableStore pins when a page is
// uploaded: above the threshold (`>`, not `>=`), with a client, and an upload
// error is returned, never read as "stays plain".
func TestUploadHTML_UploadsOnlyALargePageToAReachableStore(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	store, _ := fakeLFSUploadServer(t)
	for _, tc := range []struct {
		name    string
		client  *lfs.Client
		size    int
		ok, err bool
	}{
		{"exactly threshold stays plain", store, htmlLFSThreshold, false, false},
		{"one over is uploaded", store, htmlLFSThreshold + 1, true, false},
		{"no store reachable stays plain", nil, htmlLFSThreshold + 1, false, false},
		{"a failed upload is an error", lfs.NewClient(failing.URL, "oauth2", "token"), htmlLFSThreshold + 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := UploadHTML(tc.client, bytes.Repeat([]byte("z"), tc.size))
			if ok != tc.ok || (err != nil) != tc.err {
				t.Fatalf("ok=%v err=%v, want ok=%v err=%v", ok, err, tc.ok, tc.err)
			}
		})
	}
}

// TestPointerizeHTML_SwapsOnlyTheUploadedBytes: plan.html becomes a pointer to
// the uploaded blob only while it holds exactly the uploaded bytes. Failure
// prevented: a page rewritten after its upload (another save of the plan)
// is replaced by a pointer to a blob that is not its content, losing it.
func TestPointerizeHTML_SwapsOnlyTheUploadedBytes(t *testing.T) {
	client, stored := fakeLFSUploadServer(t)
	page := bytes.Repeat([]byte("PRESERVE-ME "), htmlLFSThreshold/12+2)
	ref, ok, err := UploadHTML(client, page)
	if err != nil || !ok {
		t.Fatalf("UploadHTML: ok=%v err=%v", ok, err)
	}

	changed := append(bytes.Clone(page), "<!-- edited -->"...)
	dir := t.TempDir()
	writePlanHTMLPlain(t, dir, changed)
	if swapped, err := PointerizeHTML(dir, ref); err != nil || swapped {
		t.Fatalf("other bytes: swapped=%v err=%v, want left alone", swapped, err)
	}
	assertPlainHTML(t, dir, changed)

	writePlanHTMLPlain(t, dir, page)
	if swapped, err := PointerizeHTML(dir, ref); err != nil || !swapped {
		t.Fatalf("uploaded bytes: swapped=%v err=%v, want a pointer", swapped, err)
	}
	pointer, err := lfs.ReadPointerFile(filepath.Join(dir, planHTMLFile))
	if err != nil {
		t.Fatalf("plan.html is not a pointer: %v", err)
	}
	if blob, ok := stored.Load(pointer.BareOID()); !ok || !bytes.Equal(blob.([]byte), page) {
		t.Fatal("the pointer must name the uploaded blob")
	}
}
