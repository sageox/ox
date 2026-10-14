package adapters

import (
	"path/filepath"
	"testing"
)

func TestCursorKnownSessionRootsAreLimitedToNativeProjectsRoot(t *testing.T) {
	home := t.TempDir()
	roots := KnownSessionRoots("cursor", home)
	if len(roots) != 1 || roots[0] != filepath.Join(home, ".cursor", "projects") {
		t.Fatalf("KnownSessionRoots(cursor) = %q, want only native projects root", roots)
	}
	if !IsSessionFileAllowed("cursor", filepath.Join(roots[0], "key", "agent-transcripts", "x", "x.jsonl"), home) {
		t.Fatal("native Cursor path rejected by root allowlist")
	}
	if IsSessionFileAllowed("cursor", filepath.Join(home, ".cursor", "agent-transcripts", "x.jsonl"), home) {
		t.Fatal("unsupported flat Cursor layout accepted")
	}
}
