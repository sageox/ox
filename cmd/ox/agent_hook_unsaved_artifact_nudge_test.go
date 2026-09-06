package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// authoredPage writes a self-contained page big enough to clear the size gate.
func authoredPage(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<!doctype html><title>Review</title><style>body{color:#fff}</style>" +
		strings.Repeat("<p>device capture</p>", 3000)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The exact miss: a review sheet authored under .context/ that no plan claims.
func TestFindUnsavedArtifacts_FindsAuthoredPage(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, ".context", "arc-lab", "device-review.html")
	authoredPage(t, page)

	got := findUnsavedArtifacts(root, time.Now())
	if len(got) != 1 || filepath.Base(got[0]) != "device-review.html" {
		t.Fatalf("authored page not found: %v", got)
	}
}

func TestFindUnsavedArtifacts_Ignores(t *testing.T) {
	root := t.TempDir()

	// generated output in a dependency tree
	authoredPage(t, filepath.Join(root, "node_modules", "pkg", "index.html"))
	// too small to be an authored page
	small := filepath.Join(root, "frag.html")
	if err := os.WriteFile(small, []byte("<html><p>hi</p></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// not HTML at all
	if err := os.WriteFile(filepath.Join(root, "notes.md"), bytes.Repeat([]byte("x"), 40000), 0o644); err != nil {
		t.Fatal(err)
	}
	// old enough to be someone else's work
	stale := filepath.Join(root, "stale.html")
	authoredPage(t, stale)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if got := findUnsavedArtifacts(root, time.Now()); len(got) != 0 {
		t.Fatalf("nudged on something it should ignore: %v", got)
	}
}

func TestLooksAuthoredPage(t *testing.T) {
	yes := [][]byte{
		[]byte("<!doctype html><style>a{}</style>"),
		[]byte("<html><img src=\"data:image/png;base64,AAA\">"),
		[]byte("<!DOCTYPE HTML><div data-ox-section=\"Verdict\">"),
	}
	for _, b := range yes {
		if !looksAuthoredPage(b) {
			t.Errorf("looksAuthoredPage(%.40q) = false, want true", b)
		}
	}
	no := [][]byte{
		[]byte("<html><body>plain</body></html>"), // no styling, no inline media
		[]byte("{\"json\": true}"),
		[]byte("# markdown"),
	}
	for _, b := range no {
		if looksAuthoredPage(b) {
			t.Errorf("looksAuthoredPage(%.40q) = true, want false", b)
		}
	}
}

// A nudge must be said once, not on every prompt.
func TestEmitUnsavedArtifactNudge_OncePerArtifact(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SAGEOX_AGENT_ID", "TestAgent")
	authoredPage(t, filepath.Join(root, "review.html"))

	var first, second bytes.Buffer
	emitUnsavedArtifactNudge(&first, root, "TestAgent")
	emitUnsavedArtifactNudge(&second, root, "TestAgent")

	if !strings.Contains(first.String(), "not in the ledger") {
		t.Fatalf("first prompt said nothing: %q", first.String())
	}
	if !strings.Contains(first.String(), "--kind") {
		t.Errorf("nudge does not name the artifact kinds: %q", first.String())
	}
	if second.Len() != 0 {
		t.Fatalf("nudge repeated on the next prompt: %q", second.String())
	}
}

// A saved plan claims ONE artifact: the source path it was saved from. Two
// pages that merely share a basename are two artifacts. This is the regression
// that mattered most in practice — the authoring contract tells everyone to
// name the file plan.html, so basename matching meant the first saved plan
// silenced every later page in the project.
func TestFindUnsavedArtifacts_SameBasenameElsewhereStillNudges(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	savedSource := filepath.Join(root, "docs", "review.html")
	authoredPage(t, savedSource)
	unsaved := filepath.Join(root, "scratch", "review.html")
	authoredPage(t, unsaved)

	if _, _, err := plan.Save(root, plan.Input{Raw: "# Review\n"}, plan.Result{}, nil,
		plan.Meta{Topic: "Review", SourcePlanPath: savedSource}); err != nil {
		t.Fatalf("seed saved plan: %v", err)
	}

	got := findUnsavedArtifacts(root, time.Now())
	if !slices.Contains(got, unsaved) {
		t.Fatalf("a distinct artifact sharing a basename with a saved plan was silenced: got %v, want %s", got, unsaved)
	}
	if slices.Contains(got, savedSource) {
		t.Errorf("the saved plan's own source nudged: %v", got)
	}
}

// A RELATIVE SourcePlanPath (legacy plans only — Save records absolute) claims
// nothing, and the artifact nudges. The directory such a path was relative to
// was never persisted, so resolving it against the current one is a guess, and
// the wrong guess is the unrecoverable direction: a resolution that lands on an
// unrelated file silences that file permanently. Nudging once about an artifact
// that turns out to be saved is the failure we prefer.
//
// The absolute case is the real path and is covered by the tests above; this
// pins that we do not quietly extend it to paths we cannot prove.
func TestFindUnsavedArtifacts_RelativeSavedSourceIsUnprovable(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	page := filepath.Join(root, "docs", "review.html")
	authoredPage(t, page)

	if _, _, err := plan.Save(root, plan.Input{Raw: "# Review\n"}, plan.Result{}, nil,
		plan.Meta{Topic: "Review", SourcePlanPath: filepath.Join("docs", "review.html")}); err != nil {
		t.Fatalf("seed saved plan: %v", err)
	}

	if got := findUnsavedArtifacts(root, time.Now()); !slices.Contains(got, page) {
		t.Fatalf("a relative stored path was treated as proof the artifact is saved: got %v, want %s", got, page)
	}
}

// Marker identity must survive paths that differ only where a flattening
// scheme collapses them: /repo/a/b.html and /repo/a_b.html both flatten to
// "_repo_a_b.html", so the second artifact was suppressed by the first's
// marker and never nudged at all.
func TestArtifactNudgedPath_DistinctPathsDistinctMarkers(t *testing.T) {
	root := t.TempDir()
	const agentID = "Ox0042"

	nested := artifactNudgedPath(root, agentID, "/repo/a/b.html")
	flat := artifactNudgedPath(root, agentID, "/repo/a_b.html")
	if nested == "" || flat == "" {
		t.Fatalf("no marker path: nested=%q flat=%q", nested, flat)
	}
	if nested == flat {
		t.Fatalf("distinct artifacts share one marker: %s", nested)
	}

	// The artifact path is attacker-influenced, so the marker must stay inside
	// the cache dir no matter what the path holds.
	cacheDir := filepath.Join(root, ".sageox", "cache", artifactNudgeCacheSubdir)
	for _, art := range []string{"/repo/a/b.html", "../../etc/passwd", `C:\x\y.html`} {
		marker := artifactNudgedPath(root, agentID, art)
		if name := filepath.Base(marker); strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
			t.Errorf("marker filename for %q is not escape-proof: %q", art, name)
		}
		if dir := filepath.Dir(marker); dir != cacheDir {
			t.Errorf("marker for %q escaped the cache dir: %s", art, dir)
		}
	}
}
