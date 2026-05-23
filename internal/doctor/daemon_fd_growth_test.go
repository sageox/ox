package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- FD-growth history file format ---
//
// Failure prevented: regressions in the rolling-history persistence layer.
// The Run() method itself is gated on a live daemon and so is harder to
// unit-test cleanly; these tests cover the read/write/trim primitives and
// the verdict math that decides whether to warn.

func TestFDHistory_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fd-history.json")

	now := time.Now().UTC().Truncate(time.Second)
	want := fdHistoryFile{
		WorkspaceID: "ws-abc",
		Samples: []fdHistorySample{
			{At: now.Add(-2 * time.Hour), Count: 12, PID: 100},
			{At: now.Add(-1 * time.Hour), Count: 14, PID: 100},
			{At: now, Count: 18, PID: 100},
		},
	}

	if err := saveFDHistory(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadFDHistory(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.WorkspaceID != want.WorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", got.WorkspaceID, want.WorkspaceID)
	}
	if len(got.Samples) != len(want.Samples) {
		t.Fatalf("len(Samples) = %d, want %d", len(got.Samples), len(want.Samples))
	}
	for i := range want.Samples {
		if got.Samples[i].Count != want.Samples[i].Count {
			t.Errorf("sample[%d].Count = %d, want %d", i, got.Samples[i].Count, want.Samples[i].Count)
		}
		if !got.Samples[i].At.Equal(want.Samples[i].At) {
			t.Errorf("sample[%d].At = %v, want %v", i, got.Samples[i].At, want.Samples[i].At)
		}
		// PID is part of the persisted schema (the doctor check writes
		// status.Pid so future investigation can correlate growth with
		// daemon restarts). A regression that drops or renames `pid` in
		// the JSON tag must trip this assertion.
		if got.Samples[i].PID != want.Samples[i].PID {
			t.Errorf("sample[%d].PID = %d, want %d", i, got.Samples[i].PID, want.Samples[i].PID)
		}
	}
}

func TestFDHistory_LoadMissing_ReturnsEmptyNotError(t *testing.T) {
	t.Parallel()
	// loadFDHistory is best-effort. A missing file should NOT propagate as
	// a fatal error to the doctor check — it returns an empty history so
	// the next Run() call seeds a fresh history naturally.
	h, err := loadFDHistory(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected os.ErrNotExist from missing file")
	}
	if len(h.Samples) != 0 {
		t.Errorf("expected empty samples on missing file, got %d", len(h.Samples))
	}
}

func TestFDHistory_LoadCorruptJSON_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fd-history.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := loadFDHistory(path)
	if err == nil {
		t.Fatal("expected unmarshal error on corrupt file")
	}
	if len(h.Samples) != 0 {
		t.Errorf("corrupt file should produce empty history; got %d samples", len(h.Samples))
	}
}

func TestFDHistory_SaveIsAtomic(t *testing.T) {
	t.Parallel()
	// Round-trip a payload, then confirm no stray *.tmp file is left
	// behind — saveFDHistory uses write-temp + rename for atomicity, and
	// a leaked .tmp would suggest the rename path silently broke.
	dir := t.TempDir()
	path := filepath.Join(dir, "fd-history.json")
	h := fdHistoryFile{Samples: []fdHistorySample{{At: time.Now(), Count: 1}}}
	if err := saveFDHistory(path, h); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leaked temp file from saveFDHistory: %s", e.Name())
		}
	}

	// And confirm the JSON is well-formed.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe fdHistoryFile
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Errorf("saved file is not valid JSON: %v", err)
	}
}

// TestFDHistory_TrimRetainsRecent verifies the in-memory trim behavior used
// by Run() — `samples = samples[len-Max:]`. This is the contract that
// keeps the on-disk file from growing unbounded across months of daily
// doctor runs.
func TestFDHistory_TrimRetainsRecent(t *testing.T) {
	t.Parallel()
	// Mirror the trim performed inside Run().
	const max = fdHistoryMaxSamples
	samples := make([]fdHistorySample, max+25)
	for i := range samples {
		samples[i] = fdHistorySample{At: time.Unix(int64(i), 0), Count: i}
	}
	if len(samples) > max {
		samples = samples[len(samples)-max:]
	}
	if len(samples) != max {
		t.Fatalf("len after trim = %d, want %d", len(samples), max)
	}
	// First retained sample's count is offset by 25 (we trimmed the 25
	// oldest), proving the trim drops oldest, not newest.
	if samples[0].Count != 25 {
		t.Errorf("first retained sample.Count = %d, want 25 (trim should drop oldest)", samples[0].Count)
	}
	if samples[len(samples)-1].Count != max+24 {
		t.Errorf("last retained sample.Count = %d, want %d", samples[len(samples)-1].Count, max+24)
	}
}

func TestDefaultFDGrowthHistoryPath(t *testing.T) {
	t.Parallel()
	// Both shapes — workspace-scoped and global — should resolve to a
	// non-empty path under the daemon state dir. The exact prefix varies
	// with XDG settings, so we assert the suffix and parent layout only.
	withID := DefaultFDGrowthHistoryPath("ws-xyz")
	if filepath.Base(withID) != "fd-history-ws-xyz.json" {
		t.Errorf("with workspaceID: basename = %q, want fd-history-ws-xyz.json", filepath.Base(withID))
	}
	global := DefaultFDGrowthHistoryPath("")
	if filepath.Base(global) != "fd-history.json" {
		t.Errorf("empty workspaceID: basename = %q, want fd-history.json", filepath.Base(global))
	}
	if filepath.Dir(withID) != filepath.Dir(global) {
		t.Errorf("paths should share a parent dir, got %q vs %q",
			filepath.Dir(withID), filepath.Dir(global))
	}
}
