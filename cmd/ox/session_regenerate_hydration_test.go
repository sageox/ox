package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// TestResolveContentPath_CacheOnlyDesign asserts the cache-only invariant
// that's load-bearing for Phase 2's failure modes:
//
//   - In-place full content → resolver returns in-place (a coworker's own
//     freshly-recorded session before LFS upload).
//   - In-place LFS pointer + cache content → resolver returns cache (NEVER
//     hydrates the in-place file). This is the case that breaks if a
//     reader uses the in-place path directly.
//   - In-place pointer + no cache → resolver returns "" (caller hydrates).
//
// Without this contract, regenerate hydrates raw.jsonl at the in-place
// path, then commitAndPushLedger globs *.jsonl and stages the full content
// as a regular git blob — breaking LFS linkage and triggering a daemon
// finalize race that overwrites good summaries with stub content. Both
// failure modes were observed on 2026-04-25 (bd ox-4ncz).
func TestResolveContentPath_CacheOnlyDesign(t *testing.T) {
	t.Run("in-place real content returns in-place", func(t *testing.T) {
		sess := t.TempDir()
		cache := t.TempDir()
		path := filepath.Join(sess, "raw.jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","content":"hi"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got := lfs.ResolveContentPath(sess, cache, "raw.jsonl")
		if got != path {
			t.Errorf("expected in-place path %q, got %q", path, got)
		}
	})

	t.Run("in-place pointer + cache content returns cache (the load-bearing case)", func(t *testing.T) {
		sess := t.TempDir()
		cache := t.TempDir()
		// in-place: LFS pointer
		if err := lfs.WritePointerFile(filepath.Join(sess, "raw.jsonl"), lfs.FileRef{
			OID: "deadbeef" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			Size: 1024,
		}); err != nil {
			t.Fatal(err)
		}
		// cache: real content
		cachedPath := filepath.Join(cache, "raw.jsonl")
		if err := os.WriteFile(cachedPath, []byte(`{"type":"assistant","content":"yo"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got := lfs.ResolveContentPath(sess, cache, "raw.jsonl")
		if got != cachedPath {
			t.Errorf("MUST return cache path when in-place is a pointer (resolver guarantees in-place stays untouched); got %q", got)
		}
	})

	t.Run("in-place pointer + no cache returns empty (caller must hydrate)", func(t *testing.T) {
		sess := t.TempDir()
		cache := t.TempDir()
		if err := lfs.WritePointerFile(filepath.Join(sess, "raw.jsonl"), lfs.FileRef{
			OID:  "feedface" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			Size: 2048,
		}); err != nil {
			t.Fatal(err)
		}
		got := lfs.ResolveContentPath(sess, cache, "raw.jsonl")
		if got != "" {
			t.Errorf("expected empty (force hydration), got %q", got)
		}
	})

	t.Run("missing file returns empty", func(t *testing.T) {
		got := lfs.ResolveContentPath(t.TempDir(), t.TempDir(), "raw.jsonl")
		if got != "" {
			t.Errorf("expected empty for missing file, got %q", got)
		}
	})
}

// TestHasSubstantiveTurns covers the empty-session preflight (bd ox-o45g).
// Without this, regen of a session that's just `{"type":"system","content":"Loaded coworker"}`
// would invoke the LLM, get refusal/narration back, fail validation, and
// persist a failure-marker stub. Cleaner to fail fast.
func TestHasSubstantiveTurns(t *testing.T) {
	cases := []struct {
		name    string
		entries []map[string]any
		want    bool
	}{
		{"empty entries", []map[string]any{}, false},
		{"only system", []map[string]any{{"type": "system", "content": "Loaded coworker"}}, false},
		{"only header", []map[string]any{{"type": "header", "content": ""}}, false},
		{"header + system + tool (no real turns)", []map[string]any{
			{"type": "header"}, {"type": "system"}, {"type": "tool"},
		}, false},
		{"one user turn", []map[string]any{{"type": "user", "content": "fix this"}}, true},
		{"one assistant turn", []map[string]any{{"type": "assistant", "content": "ok"}}, true},
		{"realistic short session", []map[string]any{
			{"type": "header"},
			{"type": "user", "content": "x"},
			{"type": "assistant", "content": "y"},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasSubstantiveTurns(tc.entries); got != tc.want {
				t.Errorf("hasSubstantiveTurns(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
