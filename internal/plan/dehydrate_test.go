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
	_, dir := ledgerPlanDir(t)
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

// ledgerPlanDir makes the layout every saved plan has, <ledger>/data/plans/<plan>,
// and returns the Ledger root and the plan dir.
func ledgerPlanDir(t *testing.T) (ledger, dir string) {
	t.Helper()
	ledger = t.TempDir()
	dir = filepath.Join(ledger, "data", "plans", "2026-10-24-launch-mockup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make plan dir: %v", err)
	}
	return ledger, dir
}

func recoveryCopy(ledger, dir string) string {
	return lfs.RecoveryCachePath(ledger, filepath.Join("data", "plans", filepath.Base(dir), planHTMLFile))
}

// TestPointerizeHTML_KeepsThePageWhereThePushRepairFindsIt: once plan.html is a
// pointer, its exact bytes sit in the Ledger recovery cache slot the push repair
// re-uploads from. Failure prevented: the store drops the uploaded blob before
// the commit is pushed (GitLab housekeeping deletes unreferenced LFS objects),
// the page exists nowhere on this machine, and the pointer refuses every later
// push for days (the 2026-10-24 Ledger wedge).
func TestPointerizeHTML_KeepsThePageWhereThePushRepairFindsIt(t *testing.T) {
	client, _ := fakeLFSUploadServer(t)
	page := largeHTMLWithHead()
	ref, ok, err := UploadHTML(client, page)
	if err != nil || !ok {
		t.Fatalf("UploadHTML: ok=%v err=%v", ok, err)
	}
	ledger, dir := ledgerPlanDir(t)
	writePlanHTMLPlain(t, dir, page)

	if swapped, err := PointerizeHTML(dir, ref); err != nil || !swapped {
		t.Fatalf("PointerizeHTML: swapped=%v err=%v, want a pointer", swapped, err)
	}

	kept, err := os.ReadFile(recoveryCopy(ledger, dir))
	if err != nil {
		t.Fatalf("no recovery copy of the page: %v", err)
	}
	pointer, err := lfs.ReadPointerFile(filepath.Join(dir, planHTMLFile))
	if err != nil {
		t.Fatalf("plan.html is not a pointer: %v", err)
	}
	if !bytes.Equal(kept, page) || lfs.ComputeOID(kept) != pointer.BareOID() {
		t.Fatal("the recovery copy must be exactly the bytes the pointer names")
	}
}

// TestPointerizeHTML_NoRecoveryCopyMeansNoPointer: when the page cannot be kept
// locally, plan.html stays plain and the error tells the caller to treat it as
// awaiting upload. Failure prevented: a pointer committed with no local copy,
// which a store that later drops the blob turns into a permanent push wedge.
func TestPointerizeHTML_NoRecoveryCopyMeansNoPointer(t *testing.T) {
	client, _ := fakeLFSUploadServer(t)
	page := largeHTMLWithHead()
	ref, ok, err := UploadHTML(client, page)
	if err != nil || !ok {
		t.Fatalf("UploadHTML: ok=%v err=%v", ok, err)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"plan dir outside a Ledger", func(t *testing.T) string { return t.TempDir() }},
		{"recovery cache blocked by a file", func(t *testing.T) string {
			ledger, dir := ledgerPlanDir(t)
			// a regular file where the cache directory belongs fails MkdirAll on every platform
			if err := os.MkdirAll(filepath.Join(ledger, ".sageox"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ledger, ".sageox", "cache"), []byte("not a dir"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.setup(t)
			writePlanHTMLPlain(t, dir, page)

			swapped, err := PointerizeHTML(dir, ref)

			if err == nil || swapped {
				t.Fatalf("swapped=%v err=%v, want an error and no pointer", swapped, err)
			}
			assertPlainHTML(t, dir, page)
			if !HasLargePlainHTML(dir) {
				t.Fatal("the plain page must read as awaiting upload so the save is retried")
			}
		})
	}
}

// TestPointerizeHTML_RevisionReplacesTheOlderCopy: a revision of a large page
// replaces the earlier revision's recovery copy. Failure prevented: the copy
// refuses to change, so every revision after the first stays plain, awaiting
// an upload that already succeeded.
func TestPointerizeHTML_RevisionReplacesTheOlderCopy(t *testing.T) {
	client, _ := fakeLFSUploadServer(t)
	ledger, dir := ledgerPlanDir(t)
	first := largeHTMLWithHead()
	second := append(bytes.Clone(first), "<!-- revision 2 -->"...)
	for _, page := range [][]byte{first, second} {
		ref, ok, err := UploadHTML(client, page)
		if err != nil || !ok {
			t.Fatalf("UploadHTML: ok=%v err=%v", ok, err)
		}
		writePlanHTMLPlain(t, dir, page)
		if swapped, err := PointerizeHTML(dir, ref); err != nil || !swapped {
			t.Fatalf("PointerizeHTML: swapped=%v err=%v", swapped, err)
		}
	}
	kept, err := os.ReadFile(recoveryCopy(ledger, dir))
	if err != nil || !bytes.Equal(kept, second) {
		t.Fatalf("recovery copy is not the latest revision (err=%v)", err)
	}
	// the save swaps in the pointer before its commit; until the revision is
	// committed, the committed pointer still names the first page
	rel := filepath.Join("data", "plans", filepath.Base(dir), planHTMLFile)
	previous, err := os.ReadFile(lfs.PreviousRecoveryCachePath(ledger, rel))
	if err != nil || !bytes.Equal(previous, first) {
		t.Fatalf("the replaced revision's copy is gone (err=%v): a failed commit leaves its pointer unrecoverable", err)
	}
}

// TestPointerizeHTML_RecoveryCopyNeverFollowsALink: a symlink planted in the
// recovery cache never redirects the copy outside the Ledger. Failure
// prevented: a linked cache directory or slot made ox write the page wherever
// the link pointed.
func TestPointerizeHTML_RecoveryCopyNeverFollowsALink(t *testing.T) {
	client, _ := fakeLFSUploadServer(t)
	page := largeHTMLWithHead()
	ref, ok, err := UploadHTML(client, page)
	if err != nil || !ok {
		t.Fatalf("UploadHTML: ok=%v err=%v", ok, err)
	}
	for _, tc := range []struct {
		name    string
		link    func(ledger, dir string) string
		wantErr bool
	}{
		{"linked cache directory", func(ledger, _ string) string {
			return filepath.Join(ledger, ".sageox", "cache", "data")
		}, true},
		{"linked slot", func(ledger, dir string) string { return recoveryCopy(ledger, dir) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, dir := ledgerPlanDir(t)
			outside := t.TempDir()
			target := filepath.Join(outside, "target")
			if tc.wantErr {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := tc.link(ledger, dir)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			writePlanHTMLPlain(t, dir, page)

			swapped, err := PointerizeHTML(dir, ref)

			if tc.wantErr {
				if err == nil || swapped {
					t.Fatalf("swapped=%v err=%v, want an error and no pointer", swapped, err)
				}
				assertPlainHTML(t, dir, page)
				entries, _ := os.ReadDir(target)
				if len(entries) != 0 {
					t.Fatalf("the copy escaped the Ledger into %s: %v", target, entries)
				}
				return
			}
			if err != nil || !swapped {
				t.Fatalf("PointerizeHTML: swapped=%v err=%v", swapped, err)
			}
			if got, _ := os.ReadFile(target); string(got) != "outside\n" {
				t.Fatal("the copy was written through the link")
			}
			info, err := os.Lstat(link)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("the slot is not a regular file (err=%v)", err)
			}
		})
	}
}
