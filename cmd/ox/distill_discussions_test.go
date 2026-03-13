package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanPendingDiscussions(t *testing.T) {
	tcPath := t.TempDir()
	discussionsDir := filepath.Join(tcPath, "discussions")

	// create two discussion dirs
	createDiscussionDir(t, discussionsDir, "2026-03-10-1423-ryan", "Architecture Review", "2026-03-10T14:23:00Z")
	createDiscussionDir(t, discussionsDir, "2026-03-11-0900-alice", "Sprint Planning", "2026-03-11T09:00:00Z")

	tests := []struct {
		name      string
		processed map[string]string
		wantCount int
	}{
		{
			name:      "no processed — finds all",
			processed: nil,
			wantCount: 2,
		},
		{
			name:      "one processed — finds remaining",
			processed: map[string]string{"2026-03-10-1423-ryan": discussionContentHash(filepath.Join(discussionsDir, "2026-03-10-1423-ryan"))},
			wantCount: 1,
		},
		{
			name: "all processed — finds none",
			processed: map[string]string{
				"2026-03-10-1423-ryan":  discussionContentHash(filepath.Join(discussionsDir, "2026-03-10-1423-ryan")),
				"2026-03-11-0900-alice": discussionContentHash(filepath.Join(discussionsDir, "2026-03-11-0900-alice")),
			},
			wantCount: 0,
		},
		{
			name:      "stale hash triggers re-scan",
			processed: map[string]string{"2026-03-10-1423-ryan": "stale-hash"},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pending, err := scanPendingDiscussions(tcPath, tt.processed)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(pending) != tt.wantCount {
				t.Errorf("got %d pending, want %d", len(pending), tt.wantCount)
			}
		})
	}
}

func TestScanPendingDiscussionsSorted(t *testing.T) {
	tcPath := t.TempDir()
	discussionsDir := filepath.Join(tcPath, "discussions")

	// create in reverse order
	createDiscussionDir(t, discussionsDir, "2026-03-11-0900-alice", "Later", "2026-03-11T09:00:00Z")
	createDiscussionDir(t, discussionsDir, "2026-03-10-1423-ryan", "Earlier", "2026-03-10T14:23:00Z")

	pending, err := scanPendingDiscussions(tcPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	if pending[0].Title != "Earlier" {
		t.Errorf("expected earliest first, got %q", pending[0].Title)
	}
}

func TestScanPendingDiscussionsMissingMetadata(t *testing.T) {
	tcPath := t.TempDir()
	discussionsDir := filepath.Join(tcPath, "discussions")

	// create a dir without metadata.json
	badDir := filepath.Join(discussionsDir, "2026-03-10-bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// create a valid dir
	createDiscussionDir(t, discussionsDir, "2026-03-10-1423-ryan", "Valid", "2026-03-10T14:23:00Z")

	pending, err := scanPendingDiscussions(tcPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 valid discussion, got %d", len(pending))
	}
}

func TestScanPendingDiscussionsNoDir(t *testing.T) {
	tcPath := t.TempDir() // no discussions/ subdir

	pending, err := scanPendingDiscussions(tcPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 for nonexistent dir, got %d", len(pending))
	}
}

func TestScanPendingDiscussionsParsesContent(t *testing.T) {
	tcPath := t.TempDir()
	discussionsDir := filepath.Join(tcPath, "discussions")

	dirName := "2026-03-10-1423-ryan"
	createDiscussionDir(t, discussionsDir, dirName, "Arch Review", "2026-03-10T14:23:00Z")

	// add summary
	os.WriteFile(filepath.Join(discussionsDir, dirName, "summary.md"), []byte("We discussed architecture"), 0o644)

	// add VTT transcript
	vttContent := `WEBVTT

00:00:00.000 --> 00:00:05.000
<v Speaker 1>Let's review the architecture</v>

00:00:05.000 --> 00:00:10.000
<v Speaker 2>Sounds good</v>
`
	os.WriteFile(filepath.Join(discussionsDir, dirName, "transcript.vtt"), []byte(vttContent), 0o644)

	pending, err := scanPendingDiscussions(tcPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}

	d := pending[0]
	if d.Title != "Arch Review" {
		t.Errorf("title = %q, want %q", d.Title, "Arch Review")
	}
	if d.Summary != "We discussed architecture" {
		t.Errorf("summary = %q, want non-empty", d.Summary)
	}
	if !strings.Contains(d.Transcript, "Speaker 1:") {
		t.Errorf("transcript should contain parsed speaker text, got %q", d.Transcript)
	}
}

func TestReadPendingDiscussionFacts(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write two fact files
	os.WriteFile(filepath.Join(factsDir, "discussion-a.md"), []byte("Fact A"), 0o644)
	os.WriteFile(filepath.Join(factsDir, "discussion-b.md"), []byte("Fact B"), 0o644)

	contents, paths, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 2 {
		t.Errorf("expected 2 contents, got %d", len(contents))
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(paths))
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "memory/.discussion-facts/") {
			t.Errorf("expected relative path, got %q", p)
		}
	}
}

func TestReadPendingDiscussionFactsSince(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write a fact file
	factPath := filepath.Join(factsDir, "discussion-a.md")
	os.WriteFile(factPath, []byte("Fact A"), 0o644)

	// set modtime to the past
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(factPath, past, past)

	// read with since = 24h ago — should skip the old file
	since := time.Now().Add(-24 * time.Hour)
	contents, paths, err := readPendingDiscussionFacts(tcPath, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents (file is older than since), got %d", len(contents))
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths (file is older than since), got %d", len(paths))
	}
}

func TestReadPendingDiscussionFactsEmptyDir(t *testing.T) {
	tcPath := t.TempDir() // no .discussion-facts dir

	contents, paths, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents for nonexistent dir, got %d", len(contents))
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths for nonexistent dir, got %d", len(paths))
	}
}

func TestDiscussionContentHash(t *testing.T) {
	dir := t.TempDir()

	// hash of empty dir
	h1 := discussionContentHash(dir)

	// add summary
	os.WriteFile(filepath.Join(dir, "summary.md"), []byte("summary content"), 0o644)
	h2 := discussionContentHash(dir)

	if h1 == h2 {
		t.Error("hash should change when summary is added")
	}

	// same content = same hash
	h3 := discussionContentHash(dir)
	if h2 != h3 {
		t.Error("hash should be stable for same content")
	}

	// metadata.json change should change hash
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"title":"v1"}`), 0o644)
	h4 := discussionContentHash(dir)
	if h3 == h4 {
		t.Error("hash should change when metadata.json is added")
	}

	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"title":"v2"}`), 0o644)
	h5 := discussionContentHash(dir)
	if h4 == h5 {
		t.Error("hash should change when metadata.json content changes")
	}
}

func TestDistillStateProcessedDiscussionsRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &distillStateV2{
		SchemaVersion: "2",
		TeamID:        "team-xyz",
		ProcessedDiscussions: map[string]string{
			"2026-03-10-1423-ryan":  "abc123",
			"2026-03-11-0900-alice": "def456",
		},
	}

	if err := saveDistillStateV2(tmp, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := loadDistillStateV2(tmp)
	if len(loaded.ProcessedDiscussions) != 2 {
		t.Fatalf("expected 2 processed discussions, got %d", len(loaded.ProcessedDiscussions))
	}
	if loaded.ProcessedDiscussions["2026-03-10-1423-ryan"] != "abc123" {
		t.Error("expected hash abc123 for ryan discussion")
	}
	if loaded.ProcessedDiscussions["2026-03-11-0900-alice"] != "def456" {
		t.Error("expected hash def456 for alice discussion")
	}
}

func TestEnsureMemoryDirsIncludesDiscussionFacts(t *testing.T) {
	tmp := t.TempDir()

	if err := ensureMemoryDirs(tmp); err != nil {
		t.Fatalf("ensureMemoryDirs: %v", err)
	}

	factsDir := filepath.Join(tmp, "memory", ".discussion-facts")
	if _, err := os.Stat(factsDir); os.IsNotExist(err) {
		t.Error("expected .discussion-facts directory to exist")
	}
}

func TestFormatDailyMemoryWithDiscussions(t *testing.T) {
	tests := []struct {
		name       string
		obsCount   int
		discCount  int
		wantSource string
	}{
		{"observations only", 5, 0, "5 observations"},
		{"discussions only", 0, 3, "3 discussions"},
		{"both sources", 5, 3, "5 observations and 3 discussions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := formatDailyMemory("2026-03-11", "content", tt.obsCount, tt.discCount)
			if !strings.Contains(content, tt.wantSource) {
				t.Errorf("expected %q in output, got:\n%s", tt.wantSource, content)
			}
		})
	}
}

// createDiscussionDir creates a minimal discussion directory with metadata.json.
func createDiscussionDir(t *testing.T, discussionsDir, dirName, title, createdAt string) {
	t.Helper()
	dirPath := filepath.Join(discussionsDir, dirName)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := discussionMetadata{
		RecordingID: "rec_" + dirName,
		Title:       title,
		CreatedAt:   createdAt,
		UserID:      "user_test",
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dirPath, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
