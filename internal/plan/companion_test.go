package plan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectCompanionLinks(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "deep-dive.html")
	if err := os.WriteFile(present, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "viz")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(sub, "timeline.html")
	if err := os.WriteFile(nested, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw := `# Plan

See [the interactive companion](deep-dive.html) and [timeline](viz/timeline.html).
Also [the same again](deep-dive.html), [a URL](https://example.com/x.html),
[an absolute path](/etc/x.html), and [a dangling link](missing.html).
`
	got := DetectCompanionLinks(raw, dir)
	want := []string{present, nested}
	if len(got) != len(want) {
		t.Fatalf("DetectCompanionLinks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	files := DetectCompanionFiles(raw, dir)
	wantRel := []string{"deep-dive.html", "viz/timeline.html"}
	if len(files) != len(wantRel) {
		t.Fatalf("DetectCompanionFiles = %+v, want %d files", files, len(wantRel))
	}
	for i := range wantRel {
		if files[i].RelPath != wantRel[i] {
			t.Errorf("file[%d].RelPath = %q, want %q", i, files[i].RelPath, wantRel[i])
		}
	}

	if got := DetectCompanionLinks(raw, ""); got != nil {
		t.Errorf("empty baseDir (stdin plan) must detect nothing, got %v", got)
	}
}

func TestCopyCompanions(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	a := filepath.Join(src, "a.html")
	if err := os.WriteFile(a, []byte("<html>A</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := CopyCompanions([]CompanionFile{{Name: "a.html", SrcPath: a, RelPath: "docs/a.html"}}, dst)
	if err != nil {
		t.Fatalf("CopyCompanions: %v", err)
	}
	if len(names) != 1 || names[0] != "a.html" {
		t.Fatalf("names = %v", names)
	}
	b, err := os.ReadFile(filepath.Join(dst, CompanionsDir, "a.html"))
	if err != nil {
		t.Fatalf("copied companion unreadable: %v", err)
	}
	if string(b) != "<html>A</html>" {
		t.Errorf("companion content rewritten: %q", b)
	}
	inline, err := os.ReadFile(filepath.Join(dst, "docs", "a.html"))
	if err != nil {
		t.Fatalf("inline companion link copy unreadable: %v", err)
	}
	if string(inline) != "<html>A</html>" {
		t.Errorf("inline companion content rewritten: %q", inline)
	}
}

func TestCompanionRefs(t *testing.T) {
	refs := CompanionRefs([]string{"a.html", "", "../evil.html", "b.html"})
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2 sane entries", refs)
	}
	if refs[0].Href != "companions/a.html" || refs[1].Href != "companions/b.html" {
		t.Errorf("hrefs = %q, %q", refs[0].Href, refs[1].Href)
	}
}

// TestSavePreservesCompanionsOnResave pins the read-merge rule: a re-save that
// carries no companions (the hook draft, a later `ox plan` run) must not wipe
// the list RecordCompanions already stored.
func TestSavePreservesCompanionsOnResave(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	created := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	in := Input{Raw: "# Companion plan\n\nBody."}
	meta := Meta{Topic: "Companion plan", CreatedAt: created}

	dir, _, err := Save("/fake/git/root", in, Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := RecordCompanions(dir, []string{"deep-dive.html"}); err != nil {
		t.Fatalf("RecordCompanions: %v", err)
	}
	// duplicate record must not double up
	if err := RecordCompanions(dir, []string{"deep-dive.html", "extra.html"}); err != nil {
		t.Fatalf("RecordCompanions (2nd): %v", err)
	}

	if _, _, err := Save("/fake/git/root", in, Result{}, nil, meta); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	got, err := LoadMeta(dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	want := []string{"deep-dive.html", "extra.html"}
	if len(got.Companions) != len(want) {
		t.Fatalf("Companions after re-save = %v, want %v", got.Companions, want)
	}
	for i := range want {
		if got.Companions[i] != want[i] {
			t.Errorf("Companions[%d] = %q, want %q", i, got.Companions[i], want[i])
		}
	}
}
