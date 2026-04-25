package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// TestOpenSessionContent_PrefersCacheOverPointer is the load-bearing test
// for the cache-only design. When the in-place file is an LFS pointer AND
// the cache has hydrated content, openSessionContent (via the underlying
// resolver) must return the cache path — never trigger an in-place write
// and never return the pointer path.
//
// Without this guarantee, callers like regenerate, lint, and token-optimize
// would either fail (pointer can't be parsed as JSONL) or — worse —
// hydrate in-place, breaking LFS linkage and giving the daemon's anti-
// entropy an opening to clobber summaries (see bd ox-4ncz post-mortem).
//
// Note: this test exercises the in-process resolver path only. The full
// hydrate-via-Batch-API path requires a live LFS server; that's covered
// by the manual end-to-end verification documented in PR #561 and a
// future ox-test-harness integration test.
func TestOpenSessionContent_PrefersCacheWhenInPlaceIsPointer(t *testing.T) {
	ledger := t.TempDir()
	sessionName := "2026-04-25T12-00-test-Ox123Z"
	sessionDir := filepath.Join(ledger, "sessions", sessionName)
	cacheDir := filepath.Join(ledger, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	// in-place: LFS pointer (this is the state for any session synced from
	// the ledger, ever).
	if err := lfs.WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"), lfs.FileRef{
		OID:  "deadbeef" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Size: 4096,
	}); err != nil {
		t.Fatal(err)
	}

	// cache: real hydrated content.
	cachedPath := filepath.Join(cacheDir, "raw.jsonl")
	cachedBody := []byte(`{"type":"user","content":"hello"}` + "\n")
	if err := os.WriteFile(cachedPath, cachedBody, 0644); err != nil {
		t.Fatal(err)
	}

	// Direct call to the underlying resolver (openSessionContent itself
	// also takes projectRoot for hydration; we exercise the no-hydrate
	// path here since the cache is already populated).
	got := lfs.ResolveContentPath(sessionDir, cacheDir, "raw.jsonl")
	if got != cachedPath {
		t.Fatalf("expected cache path %q (cache-only invariant); got %q", cachedPath, got)
	}

	// And the in-place file must STILL be a pointer — never overwritten.
	if !lfs.IsPointerFile(filepath.Join(sessionDir, "raw.jsonl")) {
		t.Errorf("in-place raw.jsonl must remain an LFS pointer after content resolution; cache-only contract violated")
	}
}

// TestOpenSessionContent_ReturnsErrorWhenNeitherHasContent ensures we don't
// silently fall through when a session is fully dehydrated and ledger
// access fails. Caller decides what to do (retry, surface to user, etc.).
func TestOpenSessionContent_ReturnsErrorWhenFullyDehydrated(t *testing.T) {
	ledger := t.TempDir()
	sessionName := "2026-04-25T12-00-test-Ox123Z"
	sessionDir := filepath.Join(ledger, "sessions", sessionName)
	cacheDir := filepath.Join(ledger, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// in-place: pointer; cache: empty.
	if err := lfs.WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"), lfs.FileRef{
		OID:  "feedface" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Size: 1024,
	}); err != nil {
		t.Fatal(err)
	}

	got := lfs.ResolveContentPath(sessionDir, cacheDir, "raw.jsonl")
	if got != "" {
		t.Fatalf("expected empty (caller must hydrate); got %q", got)
	}
}
