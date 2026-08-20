package format

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIndex(t *testing.T) {
	entries, invalid, err := LoadIndex(discussionsRoot)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(entries), entries)
	}

	full := entries[0]
	if full.Folder != "2026-08-11-22-32-full" {
		t.Errorf("entries[0].Folder = %q", full.Folder)
	}
	if full.Title != "" {
		t.Errorf("empty title must survive, got %q", full.Title)
	}
	if full.RecordedAt != "" || full.HasDistillation != nil {
		t.Errorf("real-shape entry must have no recorded_at/has_distillation, got %q / %v", full.RecordedAt, full.HasDistillation)
	}

	// Phantom entries (folder deleted) are returned as-is: dropping them is
	// read-layer policy, not format policy.
	phantom := entries[2]
	if phantom.Folder != "2026-01-01-00-00-phantom" {
		t.Errorf("phantom entry missing, entries[2] = %+v", phantom)
	}

	// The modern optional fields decode when present.
	fin := entries[3]
	if fin.RecordedAt == "" || fin.HasDistillation == nil || !*fin.HasDistillation {
		t.Errorf("optional fields lost: %+v", fin)
	}

	if len(invalid) != 2 {
		t.Fatalf("invalid = %+v, want 2 surfaced entries", invalid)
	}
	if invalid[0].Line != 5 || invalid[1].Line != 6 {
		t.Errorf("invalid line numbers = %d, %d, want 5, 6", invalid[0].Line, invalid[1].Line)
	}
}

func TestLoadIndexEdgeCases(t *testing.T) {
	t.Run("missing index is nil nil", func(t *testing.T) {
		entries, invalid, err := LoadIndex(t.TempDir())
		if err != nil || entries != nil || invalid != nil {
			t.Fatalf("got %v, %v, %v; want nil, nil, nil", entries, invalid, err)
		}
	})
	t.Run("non-array index is a hard error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`{"not":"an array"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadIndex(dir); err == nil {
			t.Fatal("want error for non-array index")
		}
	})
}
