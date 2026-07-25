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

// TestRenderFresh_BundlesCompanionNextToOutput verifies Leg A end-to-end at the
// command layer: a plan whose markdown links a relative .html companion renders
// with the companion card AND the companion file is copied into companions/
// next to the -o output, so both the card link and the plan's own inline
// relative link resolve. Failure prevented: the hand-crafted interactive page
// orphaned as a dead href in a temp-file render.
func TestRenderFresh_BundlesCompanionNextToOutput(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test") // headless: never open a browser
	t.Chdir(t.TempDir())               // outside any git repo: findGitRoot()=="" so no ledger save

	srcDir := t.TempDir()
	companion := filepath.Join(srcDir, "deep-dive.html")
	if err := os.WriteFile(companion, []byte("<html>rich</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(srcDir, "plan.md")
	planMD := "# Companion Plan\n\nSee [the deep-dive](deep-dive.html).\n\n## One\n\na\n\n## Two\n\nb\n"
	if err := os.WriteFile(planPath, []byte(planMD), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "render.html")

	cmd := planRenderCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Flags().Set("file", planPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set("file", "") })
	if err := runPlanRenderFresh(cmd, planPath, outPath, false, false); err != nil {
		t.Fatalf("runPlanRenderFresh: %v", err)
	}

	html, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(html), `href="companions/deep-dive.html"`) {
		t.Error("render missing the companion card link")
	}
	copied, err := os.ReadFile(filepath.Join(outDir, "companions", "deep-dive.html"))
	if err != nil {
		t.Fatalf("companion not copied next to output: %v", err)
	}
	if string(copied) != "<html>rich</html>" {
		t.Errorf("companion content rewritten: %q", copied)
	}
}

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

	emitRenderedHTML(cmd, htmlBytes, savedDir, "", true, "large-plan", nil)

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
