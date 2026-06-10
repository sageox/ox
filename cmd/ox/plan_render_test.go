package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

// TestEmitRenderedHTML_FallsBackFromPointerToFreshBytes verifies `ox plan render
// --open` still opens real HTML when the saved ledger copy is an LFS pointer.
// Failure prevented: image-heavy renders save as pointers, then `--open` only
// surfaces the pointer-backed ledger path instead of the fresh render bytes this
// process already has in hand.
func TestEmitRenderedHTML_FallsBackFromPointerToFreshBytes(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")

	savedDir := t.TempDir()
	htmlPath := filepath.Join(savedDir, "plan.html")
	ref := lfs.NewFileRef([]byte(strings.Repeat("x", 400_000)))
	if err := lfs.WritePointerFile(htmlPath, ref); err != nil {
		t.Fatalf("write pointer: %v", err)
	}

	htmlBytes := []byte("<html><body>fresh render</body></html>")
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	emitRenderedHTML(cmd, htmlBytes, savedDir, "", true, "large-plan")

	got := strings.TrimSpace(out.String())
	const prefix = "Rendered HTML: "
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("expected rendered HTML path, got %q", got)
	}
	target := strings.TrimPrefix(got, prefix)
	if target == htmlPath {
		t.Fatalf("expected fallback path instead of pointer file %q", htmlPath)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read fallback render: %v", err)
	}
	if string(b) != string(htmlBytes) {
		t.Fatalf("fallback render mismatch: got %q want %q", string(b), string(htmlBytes))
	}
}
