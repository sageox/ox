package summaryeval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeGolden(t *testing.T, corpusDir, name string, gs GoldenSession) {
	t.Helper()
	sessionDir := filepath.Join(corpusDir, name)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(gs, "", "  ")
	if err := os.WriteFile(filepath.Join(sessionDir, "reference.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGoldenSession(t *testing.T) {
	dir := t.TempDir()
	writeGolden(t, dir, "session-a", GoldenSession{
		Name: "session-a",
		Reference: Summary{
			Title:   "Test session",
			Outcome: "success",
		},
	})

	gs, err := LoadGoldenSession(dir, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if gs == nil {
		t.Fatal("expected non-nil GoldenSession")
	}
	if gs.Reference.Title != "Test session" {
		t.Errorf("title mismatch: %q", gs.Reference.Title)
	}
}

func TestLoadGoldenSession_NotExistReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	gs, err := LoadGoldenSession(dir, "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gs != nil {
		t.Errorf("expected nil for missing session")
	}
}

func TestLoadCorpus_OrderedByName(t *testing.T) {
	dir := t.TempDir()
	writeGolden(t, dir, "z-last", GoldenSession{Name: "z-last", Reference: Summary{Title: "z"}})
	writeGolden(t, dir, "a-first", GoldenSession{Name: "a-first", Reference: Summary{Title: "a"}})
	writeGolden(t, dir, "m-middle", GoldenSession{Name: "m-middle", Reference: Summary{Title: "m"}})

	corpus, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus) != 3 {
		t.Fatalf("expected 3, got %d", len(corpus))
	}
	if corpus[0].Name != "a-first" || corpus[2].Name != "z-last" {
		t.Errorf("not ordered lexicographically: %v", []string{corpus[0].Name, corpus[1].Name, corpus[2].Name})
	}
}

func TestLoadCorpus_SkipsDirsWithoutReference(t *testing.T) {
	dir := t.TempDir()
	// Valid session
	writeGolden(t, dir, "good", GoldenSession{Name: "good", Reference: Summary{Title: "g"}})
	// Dir without reference.json — should be skipped
	if err := os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	corpus, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus) != 1 {
		t.Errorf("expected 1 (skipping empty-dir), got %d", len(corpus))
	}
}
