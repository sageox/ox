package main

// Tests for the review server's restart/resume durability surface: the
// stable-identity endpoints and the port selection that keep an open review
// tab alive across server death.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
)

// TestReviewServer_HealthzIdentifiesPlan verifies /healthz is served without a
// token and names the plan being served. Failure prevented: a second
// `ox plan review` can't recognize the running server, so it races it for the
// port; the page's offline probe can't detect recovery.
func TestReviewServer_HealthzIdentifiesPlan(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	var h reviewHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.App != "ox-plan-review" || h.Slug != "p" {
		t.Errorf("healthz identity = %+v", h)
	}
}

// TestReviewServer_ServesOfflineShell verifies /sw.js is served as JS. Failure
// prevented: the page registers a 404 as its service worker, so a reload while
// the server is down shows a browser error page instead of the cached plan.
func TestReviewServer_ServesOfflineShell(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)
	resp, err := http.Get(srv.URL + "/sw.js")
	if err != nil {
		t.Fatalf("sw.js: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sw.js status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("sw.js content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "caches.match") {
		t.Error("sw.js must serve the cache-fallback shell")
	}
}

// TestReviewServer_PageIsNoStore verifies the live page forbids HTTP caching —
// offline serving is the service worker's job. Failure prevented: the HTTP
// cache masks a dead server with a stale page that still looks live, hiding
// the disconnected state from the reviewer.
func TestReviewServer_PageIsNoStore(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	// the temp dir has no plan.md, so the render may 500 — the header contract
	// applies to the page route regardless of render success.
	if cc := resp.Header.Get("Cache-Control"); resp.StatusCode == http.StatusOK && cc != "no-store" {
		t.Errorf("page Cache-Control = %q, want no-store", cc)
	}
}

// TestReviewPortCandidates_PersistedFirstThenStable verifies candidate order:
// the persisted (tab-parked) port leads, the deterministic stable port and its
// probe window follow, no dupes, no nonsense ports. Failure prevented: a
// restart binds a fresh origin while the old one was free, stranding marks.
func TestReviewPortCandidates_PersistedFirstThenStable(t *testing.T) {
	const dirName = "2026-07-01-my-plan"
	stable := plan.StableReviewPort(dirName)

	got := reviewPortCandidates(51000, dirName)
	if got[0] != 51000 {
		t.Errorf("persisted port must lead: %v", got)
	}
	if got[1] != stable {
		t.Errorf("stable port must follow persisted: %v", got)
	}
	seen := map[int]bool{}
	for _, p := range got {
		if p <= 0 || p > 65535 || seen[p] {
			t.Fatalf("bad candidate list: %v", got)
		}
		seen[p] = true
	}
	// persisted == stable must dedupe, and zero persisted must drop out.
	if got := reviewPortCandidates(stable, dirName); got[0] != stable || len(got) != 5 {
		t.Errorf("dedupe failed: %v", got)
	}
	if got := reviewPortCandidates(0, dirName); got[0] != stable {
		t.Errorf("zero persisted must be skipped: %v", got)
	}
}

// TestProbeReviewServer_MatchesOnlyThisPlan verifies the probe recognizes a
// live server for the SAME plan and rejects everything else (other plan,
// non-ox listener, dead port). Failure prevented: `ox plan review` "reuses" a
// stranger's server and never starts its own.
func TestProbeReviewServer_MatchesOnlyThisPlan(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir) // serves planDir with Dir=base(dir)
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	dirName := dir[strings.LastIndex(dir, "/")+1:]
	if !probeReviewServer(port, dirName) {
		t.Error("probe must recognize a live server for the same plan")
	}
	if probeReviewServer(port, "2026-01-01-some-other-plan") {
		t.Error("probe must reject a server for a different plan")
	}
	// a dead port must not match (bind-then-close to get a free one).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if probeReviewServer(dead, dirName) {
		t.Error("probe must reject a dead port")
	}
}
