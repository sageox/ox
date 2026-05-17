package uicatalog_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/sageox/ox/internal/uicatalog"

	// import the entries package so init() side effects register every component
	_ "github.com/sageox/ox/internal/uicatalog/entries"
)

// TestEntries_HaveDocsAndAreNonEmpty asserts that every registered
// catalog Entry has a matching docs/design/components/<name>.md spec
// file, and that no critical Entry field is empty. This is the drift
// gate from .claude/rules/design.md rule #5 — "new components are not
// done until they're in the catalog." Without this test, an entry can
// silently lack a spec page (or a spec page can outlive its entry).
func TestEntries_HaveDocsAndAreNonEmpty(t *testing.T) {
	repoRoot := findRepoRoot(t)
	docsDir := filepath.Join(repoRoot, "docs", "design", "components")

	entries := uicatalog.All()
	if len(entries) == 0 {
		t.Fatal("no catalog entries registered — does internal/uicatalog/entries/* import correctly?")
	}

	registered := map[string]bool{}
	for _, e := range entries {
		registered[e.Name] = true

		if e.Family == "" {
			t.Errorf("entry %q: empty Family", e.Name)
		}
		if e.Package == "" {
			t.Errorf("entry %q: empty Package", e.Name)
		}
		if len(e.Exports) == 0 {
			t.Errorf("entry %q: empty Exports — list the public Go symbols", e.Name)
		}
		if e.Since == "" {
			t.Errorf("entry %q: empty Since", e.Name)
		}
		if e.WhenToUse == "" || e.WhenNotTo == "" {
			t.Errorf("entry %q: WhenToUse and WhenNotTo must both be set", e.Name)
		}
		if e.Render == nil {
			t.Errorf("entry %q: nil Render func", e.Name)
			continue
		}
		out := e.Render()
		if out == "" {
			t.Errorf("entry %q: Render() returned empty string", e.Name)
		}

		docPath := filepath.Join(docsDir, e.Name+".md")
		if _, err := os.Stat(docPath); err != nil {
			t.Errorf("entry %q: missing spec at %s", e.Name, docPath)
		}
	}

	// Catch orphan docs (file exists with no registered entry).
	dirEntries, err := os.ReadDir(docsDir)
	if err != nil {
		t.Fatalf("read components dir: %v", err)
	}
	var orphans []string
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if filepath.Ext(name) != ".md" {
			continue
		}
		base := name[:len(name)-len(".md")]
		if !registered[base] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	for _, o := range orphans {
		t.Errorf("orphan doc: docs/design/components/%s has no registered entry", o)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/uicatalog/registry_test.go → repo root is two levels up
	return filepath.Join(filepath.Dir(file), "..", "..")
}
