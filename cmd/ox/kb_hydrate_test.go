package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

// kb_hydrate_test.go covers the on-demand hydration command end-to-end. We
// run the production code path (runKBHydrateWithDeps) against a fake LFS
// server fronted by httptest, so the test exercises:
//   - the bubble-walk -> pointer detection -> Batch API -> download -> rename pipeline
//   - the auth-missing precondition
//   - per-file failures vs. all-failures exit-code policy
//   - JSON output shape
//
// The tests deliberately use the real lfs.HydrateBubble — we don't stub it.
// The seam between production wiring and tests is the kbHydrateDeps struct.

// fakeLFSServer is an httptest server simulating a Git LFS Batch API.
// Stores OID -> bytes; the /batch endpoint returns download actions whose
// hrefs point back to /object/<oid>. Counters expose call counts so tests
// can assert idempotency.
type fakeLFSServer struct {
	srv          *httptest.Server
	objects      map[string][]byte // bareOID -> content
	missingOIDs  map[string]bool   // OIDs that should report a server-side 404
	batchCount   int32             // total /batch POSTs
	objectCount  int32             // total /object/<oid> GETs
	requireAuth  bool              // when true, requests without Authorization are 401'd
	expectAuthOK bool              // sentinel set when at least one authenticated request landed
}

func newFakeLFSServer(t *testing.T, files map[string][]byte) *fakeLFSServer {
	t.Helper()
	s := &fakeLFSServer{
		objects:     files,
		missingOIDs: map[string]bool{},
	}
	mux := http.NewServeMux()

	// /info/lfs/objects/batch — the Batch API endpoint.
	mux.HandleFunc("/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.batchCount, 1)
		if s.requireAuth && r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.expectAuthOK = true
		var req struct {
			Operation string            `json:"operation"`
			Objects   []lfs.BatchObject `json:"objects"`
			Transfers []string          `json:"transfers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := lfs.BatchResponse{Transfer: "basic"}
		for _, obj := range req.Objects {
			ro := lfs.BatchResponseObject{OID: obj.OID, Size: obj.Size}
			if s.missingOIDs[obj.OID] {
				ro.Error = &lfs.ObjectError{Code: 404, Message: "object not found"}
			} else if _, ok := s.objects[obj.OID]; ok {
				ro.Actions = &lfs.Actions{Download: &lfs.Action{Href: s.srv.URL + "/object/" + obj.OID}}
			} else {
				ro.Error = &lfs.ObjectError{Code: 404, Message: "object not found"}
			}
			resp.Objects = append(resp.Objects, ro)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /object/<oid> — direct blob serving.
	mux.HandleFunc("/object/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.objectCount, 1)
		oid := strings.TrimPrefix(r.URL.Path, "/object/")
		data, ok := s.objects[oid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})

	// also expose a /info/refs path so the URL "looks like" a git remote;
	// nothing actually calls it but it makes the test traces clearer.
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// makeBubble creates a synthetic kb checkout containing the given pointer
// files. Returns the absolute bubble dir and a map of relative path -> OID
// suitable for tests to assert the post-hydration content against.
func makeBubble(t *testing.T, contents map[string][]byte) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	oidByPath := make(map[string]string, len(contents))
	for rel, body := range contents {
		oid := lfs.ComputeOID(body)
		oidByPath[rel] = oid
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		ptr := lfs.FormatPointer("sha256:"+oid, int64(len(body)))
		if err := os.WriteFile(full, []byte(ptr), 0o644); err != nil {
			t.Fatalf("write pointer: %v", err)
		}
	}
	return dir, oidByPath
}

// fakeKBResolver is a minimal kbResolver returning a fixed bubble list.
// Reused across tests; bubble lookups by slug or kb_id both work.
type fakeKBResolver struct {
	bubbles []api.KB
	err     error
}

func (f *fakeKBResolver) ListBubbles(_ context.Context) ([]api.KB, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.bubbles, nil
}
func (f *fakeKBResolver) GetBubble(_ context.Context, kbID string) (*api.KB, error) {
	for i := range f.bubbles {
		if f.bubbles[i].KBID == kbID || f.bubbles[i].Slug == kbID {
			return &f.bubbles[i], nil
		}
	}
	return nil, fmt.Errorf("not found")
}

// newHydrateTestCmd returns a fresh cobra command wired with capture buffers,
// matching the production flag layout (--json).
func newHydrateTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{Use: "hydrate"}
	cmd.Flags().Bool("json", false, "")
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

// buildDeps builds a kbHydrateDeps wired to a fake LFS server and a
// pre-populated bubble checkout. Tests pass in a fakeLFSServer plus the
// bubble dir and kb_id to fixture-up the rest.
func buildDeps(server *fakeLFSServer, bubbleDir, kbID, slug string, withCreds bool) kbHydrateDeps {
	deps := kbHydrateDeps{
		resolver: &fakeKBResolver{bubbles: []api.KB{
			{KBID: kbID, KBType: api.KBTypeTeam, Slug: slug, RepoURL: server.srv.URL + "/repo.git"},
		}},
		pathFor:       func(_ string) string { return bubbleDir },
		endpointForKB: func(_ string) string { return "https://example.test" },
		bubbleRemote:  func(_ string) (string, error) { return server.srv.URL, nil },
		newLFSClient:  lfs.NewClient,
		hydrateBubble: lfs.HydrateBubble,
		resolveBubble: defaultResolveBubble,
	}
	if withCreds {
		deps.loadCreds = func(_ string) (*gitserver.GitCredentials, error) {
			return &gitserver.GitCredentials{Username: "oauth2", Token: "tkn"}, nil
		}
	} else {
		deps.loadCreds = func(_ string) (*gitserver.GitCredentials, error) {
			return nil, nil
		}
	}
	return deps
}

// TestKBHydrate_SingleFile verifies that hydrating one specific path swaps
// just that pointer for real content while leaving sibling pointers alone.
//
// Failure prevented: a regression that hydrated the entire bubble when a
// path arg was supplied would silently rack up bandwidth on huge bubbles
// and clobber files the user didn't ask for.
func TestKBHydrate_SingleFile(t *testing.T) {
	body := []byte("hello-world contents 1")
	other := []byte("other unrelated body 2")

	bubbleDir, oids := makeBubble(t, map[string][]byte{
		"docs/big.bin":   body,
		"docs/other.bin": other,
	})
	server := newFakeLFSServer(t, map[string][]byte{
		oids["docs/big.bin"]:   body,
		oids["docs/other.bin"]: other,
	})

	cmd, _, _ := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_team1", "team1", true)

	if err := runKBHydrateWithDeps(cmd, "team1", "docs/big.bin", false, "", deps); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(bubbleDir, "docs/big.bin"))
	if err != nil {
		t.Fatalf("read hydrated: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("hydrated content mismatch: got %q want %q", got, body)
	}
	// the sibling must still be a pointer
	if !lfs.IsPointerFile(filepath.Join(bubbleDir, "docs/other.bin")) {
		t.Errorf("docs/other.bin should still be a pointer")
	}
}

// TestKBHydrate_AllFiles verifies the all-pointers path: omitting the path
// arg hydrates every pointer, and non-pointer files are untouched.
//
// Failure prevented: if the walker accidentally rewrote regular files (e.g.
// a small README) it could corrupt the working tree.
func TestKBHydrate_AllFiles(t *testing.T) {
	bodies := map[string][]byte{
		"a.bin":     []byte("alpha pointer body"),
		"b.bin":     []byte("beta pointer body 2"),
		"sub/c.bin": []byte("gamma pointer body 3"),
	}
	bubbleDir, oids := makeBubble(t, bodies)

	// add a non-pointer regular file that must NOT be touched
	if err := os.WriteFile(filepath.Join(bubbleDir, "README.md"), []byte("regular"), 0o644); err != nil {
		t.Fatal(err)
	}

	srvFiles := make(map[string][]byte, len(oids))
	for rel, oid := range oids {
		srvFiles[oid] = bodies[rel]
	}
	server := newFakeLFSServer(t, srvFiles)

	cmd, _, _ := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_a", "all", true)

	if err := runKBHydrateWithDeps(cmd, "all", "", false, "", deps); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	for rel, want := range bodies {
		got, err := os.ReadFile(filepath.Join(bubbleDir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: got %q want %q", rel, got, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(bubbleDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "regular" {
		t.Errorf("README.md mutated: got %q", got)
	}
}

// TestKBHydrate_Idempotent verifies that running hydrate a second time is a
// no-op: nothing to do, no batch calls, no object downloads.
//
// Failure prevented: a regression that re-downloads everything every run
// would be silently expensive (bandwidth, server load) and slow the loop
// for users who script this.
func TestKBHydrate_Idempotent(t *testing.T) {
	bodies := map[string][]byte{
		"x.bin": []byte("idempotent body 1"),
	}
	bubbleDir, oids := makeBubble(t, bodies)
	srvFiles := map[string][]byte{oids["x.bin"]: bodies["x.bin"]}
	server := newFakeLFSServer(t, srvFiles)

	cmd, _, _ := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_x", "idem", true)

	if err := runKBHydrateWithDeps(cmd, "idem", "", false, "", deps); err != nil {
		t.Fatalf("first hydrate: %v", err)
	}
	beforeBatch := atomic.LoadInt32(&server.batchCount)
	beforeObj := atomic.LoadInt32(&server.objectCount)

	if err := runKBHydrateWithDeps(cmd, "idem", "", false, "", deps); err != nil {
		t.Fatalf("second hydrate: %v", err)
	}
	if atomic.LoadInt32(&server.batchCount) != beforeBatch {
		t.Errorf("batch API called on second run: before=%d after=%d", beforeBatch, server.batchCount)
	}
	if atomic.LoadInt32(&server.objectCount) != beforeObj {
		t.Errorf("object download called on second run: before=%d after=%d", beforeObj, server.objectCount)
	}
}

// TestKBHydrate_PerFileFailureContinues verifies that one OID returning 404
// from the Batch API doesn't abort the run: surviving files still hydrate,
// the failed file is reported, and exit code is 0 when at least one file
// succeeded.
//
// Failure prevented: an all-or-nothing implementation would force users to
// hand-prune the failing file before they could hydrate the rest.
func TestKBHydrate_PerFileFailureContinues(t *testing.T) {
	bodies := map[string][]byte{
		"good.bin": []byte("good body content alpha"),
		"bad.bin":  []byte("bad body content beta!"),
	}
	bubbleDir, oids := makeBubble(t, bodies)
	srvFiles := map[string][]byte{oids["good.bin"]: bodies["good.bin"]}
	// note: oids["bad.bin"] is omitted -> server reports 404
	server := newFakeLFSServer(t, srvFiles)
	server.missingOIDs[oids["bad.bin"]] = true

	cmd, _, stderr := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_p", "partial", true)

	if err := runKBHydrateWithDeps(cmd, "partial", "", false, "", deps); err != nil {
		t.Fatalf("hydrate (expected exit 0 on partial success): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(bubbleDir, "good.bin"))
	if err != nil {
		t.Fatalf("read good: %v", err)
	}
	if !bytes.Equal(got, bodies["good.bin"]) {
		t.Errorf("good.bin not hydrated")
	}
	if !lfs.IsPointerFile(filepath.Join(bubbleDir, "bad.bin")) {
		t.Errorf("bad.bin should remain a pointer when its OID is missing on server")
	}
	if !strings.Contains(stderr.String(), "bad.bin") {
		t.Errorf("expected bad.bin in stderr report, got %q", stderr.String())
	}
}

// TestKBHydrate_AuthMissing verifies that no credentials produces a clear
// error pointing at `ox login` and a non-zero exit (cli.ErrSilent).
//
// Failure prevented: a silent auth failure that returns 0 would hide the
// problem from users running `ox kb hydrate` in CI scripts.
func TestKBHydrate_AuthMissing(t *testing.T) {
	bubbleDir, _ := makeBubble(t, map[string][]byte{"a.bin": []byte("content")})
	server := newFakeLFSServer(t, nil)

	cmd, _, stderr := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_x", "noauth", false)

	err := runKBHydrateWithDeps(cmd, "noauth", "", false, "", deps)
	if err == nil {
		t.Fatal("expected error on missing credentials")
	}
	if !strings.Contains(stderr.String(), "ox login") {
		t.Errorf("expected `ox login` hint in stderr, got %q", stderr.String())
	}
}

// TestKBHydrate_ForwardCompatInvalidPointer verifies that a small file that
// looks pointer-shaped (starts with "version https://git-lfs...") but is
// otherwise garbage doesn't corrupt the file or abort the run; other
// pointers still hydrate.
//
// Failure prevented: a strict pointer parser that errored out instead of
// reporting and continuing would block hydration whenever a single file
// got truncated or carried a future spec extension.
func TestKBHydrate_ForwardCompatInvalidPointer(t *testing.T) {
	body := []byte("real content for good.bin x")
	bubbleDir, oids := makeBubble(t, map[string][]byte{"good.bin": body})

	// plant a malformed pointer at bad.bin: starts with version line but no
	// oid/size — will fail ParsePointer, must be reported and ignored.
	bad := []byte("version https://git-lfs.github.com/spec/v1\nthis-is-not-valid\n")
	if err := os.WriteFile(filepath.Join(bubbleDir, "bad.bin"), bad, 0o644); err != nil {
		t.Fatal(err)
	}

	server := newFakeLFSServer(t, map[string][]byte{oids["good.bin"]: body})

	cmd, _, _ := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_z", "compat", true)

	if err := runKBHydrateWithDeps(cmd, "compat", "", false, "", deps); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(bubbleDir, "good.bin"))
	if err != nil {
		t.Fatalf("read good: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("good.bin not hydrated")
	}
	// bad.bin must be untouched (no corruption)
	stillBad, err := os.ReadFile(filepath.Join(bubbleDir, "bad.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stillBad, bad) {
		t.Errorf("bad.bin was modified despite parse failure")
	}
}

// TestKBHydrate_JSONOutputShape pins the JSON envelope so accidental shape
// changes (renamed fields, added top-level keys) trip a test.
//
// Failure prevented: scripts piping `ox kb hydrate ... --json` to jq would
// silently break if a field were renamed.
func TestKBHydrate_JSONOutputShape(t *testing.T) {
	body := []byte("json-body content one one")
	bubbleDir, oids := makeBubble(t, map[string][]byte{"a.bin": body})
	server := newFakeLFSServer(t, map[string][]byte{oids["a.bin"]: body})

	cmd, stdout, _ := newHydrateTestCmd(t)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	deps := buildDeps(server, bubbleDir, "kb_j", "jsontest", true)

	if err := runKBHydrateWithDeps(cmd, "jsontest", "", true, "", deps); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, stdout.String())
	}

	// canonical keys per the spec: slug, kb_id, hydrated. failed is omitempty.
	for _, key := range []string{"slug", "kb_id", "hydrated"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON missing required key %q in %v", key, parsed)
		}
	}
	if got, want := parsed["slug"], "jsontest"; got != want {
		t.Errorf("slug: got %v want %q", got, want)
	}
	if got, want := parsed["kb_id"], "kb_j"; got != want {
		t.Errorf("kb_id: got %v want %q", got, want)
	}
	hyd, ok := parsed["hydrated"].([]any)
	if !ok {
		t.Fatalf("hydrated should be array, got %T", parsed["hydrated"])
	}
	if len(hyd) != 1 || hyd[0] != "a.bin" {
		t.Errorf("hydrated mismatch: %v", hyd)
	}
}

// TestKBHydrate_KBIDDirect verifies that a kb_id-prefixed query does not
// require the API resolver to succeed — the offline path is preserved.
//
// Failure prevented: a network outage that broke `ox kb list` would also
// break hydration if we routed kb_id queries through the resolver.
func TestKBHydrate_KBIDDirect(t *testing.T) {
	body := []byte("kbid-direct content one one")
	bubbleDir, oids := makeBubble(t, map[string][]byte{"a.bin": body})
	server := newFakeLFSServer(t, map[string][]byte{oids["a.bin"]: body})

	cmd, _, _ := newHydrateTestCmd(t)
	deps := buildDeps(server, bubbleDir, "kb_direct1", "ignored", true)
	// stub the resolver to fail loudly — proves we never call it
	deps.resolver = &fakeKBResolver{err: errors.New("resolver should not be called")}

	if err := runKBHydrateWithDeps(cmd, "kb_direct1", "", false, "", deps); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
}
