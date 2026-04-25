package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// TestNeedsHydration_LFSStubBug is the regression for the LFS-stub bug
// that blocked Phase 2 of the 71-session richness regen. Before the fix,
// the regen path gated download on `os.Stat(rawPath) != nil`, which
// passed for LFS pointer files because they exist on disk — they just
// contain ~140 bytes of "version https://..." text instead of session
// JSONL. Result: download skipped, ReadSessionFromPath failed parsing
// pointer bytes as JSONL, every stub-form session erroring out.
//
// The fix routes through needsHydration(), which combines the existence
// check with lfs.IsPointerFile(). This test asserts both branches.
func TestNeedsHydration_LFSStubBug(t *testing.T) {
	t.Run("missing file needs hydration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.jsonl")
		if !needsHydration(path) {
			t.Errorf("missing file should need hydration")
		}
	})

	t.Run("LFS pointer stub needs hydration (the actual bug)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		ref := lfs.FileRef{
			OID:  "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			Size: 4096,
		}
		if err := lfs.WritePointerFile(path, ref); err != nil {
			t.Fatalf("WritePointerFile: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stub file should exist on disk; this is the whole point of the bug: %v", err)
		}
		if !needsHydration(path) {
			t.Errorf("LFS pointer stub MUST report needsHydration=true; this is the ox session regenerate Phase-2 bug")
		}
	})

	t.Run("real session bytes do not need hydration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.jsonl")
		body := []byte(`{"type":"user","content":"hello","seq":1}` + "\n" +
			`{"type":"assistant","content":"hi","seq":2}` + "\n")
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatalf("write raw: %v", err)
		}
		if needsHydration(path) {
			t.Errorf("hydrated session bytes should NOT need hydration")
		}
	})
}
