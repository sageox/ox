package lfs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHydrateBubble_AtomicReplace verifies that a successful hydration
// produces real content at the in-place path (replacing the pointer) and
// removes any temp sidecar so the working tree is clean.
//
// Failure prevented: a bug that left .lfs-tmp.<nanos> files behind would
// confuse `git status` and look like the user has uncommitted work.
func TestHydrateBubble_AtomicReplace(t *testing.T) {
	body := []byte("atomic replace body — one two three")
	oid := ComputeOID(body)

	dir := t.TempDir()
	rel := "data/payload.bin"
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	ptr := FormatPointer("sha256:"+oid, int64(len(body)))
	if err := os.WriteFile(full, []byte(ptr), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newHydrateTestServer(t, map[string][]byte{oid: body})
	client := NewClient(srv.URL, "oauth2", "tok")

	res, err := HydrateBubble(context.Background(), dir, "", client, nil)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(res.Hydrated) != 1 || res.Hydrated[0] != "data/payload.bin" {
		t.Errorf("hydrated: %v", res.Hydrated)
	}
	if len(res.Failed) != 0 {
		t.Errorf("unexpected failures: %v", res.Failed)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("content mismatch: got %q want %q", got, body)
	}
	// no temp left behind
	entries, _ := os.ReadDir(filepath.Dir(full))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".lfs-tmp.") {
			t.Errorf("stray temp file: %s", e.Name())
		}
	}
}

// TestHydrateBubble_SkipsGitDir verifies the walker never descends into
// .git/, so pointer-shaped objects in git's internal storage can't trip
// false positives.
//
// Failure prevented: hydrating .git/objects/... would corrupt the repo.
func TestHydrateBubble_SkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	gitInternal := filepath.Join(dir, ".git", "objects", "00")
	if err := os.MkdirAll(gitInternal, 0o755); err != nil {
		t.Fatal(err)
	}
	// plant something that looks like an LFS pointer inside .git/
	body := []byte("inside git body content one one")
	oid := ComputeOID(body)
	ptr := FormatPointer("sha256:"+oid, int64(len(body)))
	planted := filepath.Join(gitInternal, "deadbeef")
	if err := os.WriteFile(planted, []byte(ptr), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newHydrateTestServer(t, map[string][]byte{oid: body})
	client := NewClient(srv.URL, "oauth2", "tok")

	res, err := HydrateBubble(context.Background(), dir, "", client, nil)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(res.Hydrated) != 0 {
		t.Errorf("walker descended into .git/: %v", res.Hydrated)
	}
	got, err := os.ReadFile(planted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(ptr)) {
		t.Errorf(".git internal modified")
	}
}

// newHydrateTestServer builds an httptest LFS server suitable for hydrate tests.
func newHydrateTestServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string        `json:"operation"`
			Objects   []BatchObject `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp := BatchResponse{Transfer: "basic"}
		for _, obj := range req.Objects {
			ro := BatchResponseObject{OID: obj.OID, Size: obj.Size}
			if _, ok := files[obj.OID]; ok {
				ro.Actions = &Actions{Download: &Action{Href: srv.URL + "/object/" + obj.OID}}
			} else {
				ro.Error = &ObjectError{Code: 404, Message: "not found"}
			}
			resp.Objects = append(resp.Objects, ro)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/object/", func(w http.ResponseWriter, r *http.Request) {
		oid := strings.TrimPrefix(r.URL.Path, "/object/")
		body, ok := files[oid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
