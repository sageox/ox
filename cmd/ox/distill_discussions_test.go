package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/discussion"
)

func TestScanPendingDiscussions(t *testing.T) {
	// helper: set up tcPath with two discussions and optional fact files with source_hash
	setup := func(t *testing.T, withFactFiles bool) (string, string) {
		t.Helper()
		tcPath := t.TempDir()
		discussionsDir := filepath.Join(tcPath, "discussions")
		createDiscussionDir(t, discussionsDir, "2026-03-10-1423-ryan", "Architecture Review", "2026-03-10T14:23:00Z")
		createDiscussionDir(t, discussionsDir, "2026-03-11-0900-alice", "Sprint Planning", "2026-03-11T09:00:00Z")
		if withFactFiles {
			factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
			os.MkdirAll(factsDir, 0o755)
			// write JSONL fact files with source_hash matching current content
			ryanHash := discussionContentHash(filepath.Join(discussionsDir, "2026-03-10-1423-ryan"))
			aliceHash := discussionContentHash(filepath.Join(discussionsDir, "2026-03-11-0900-alice"))
			writeFactFileWithHash(t, factsDir, "2026-03-10-1423-ryan.jsonl", ryanHash)
			writeFactFileWithHash(t, factsDir, "2026-03-11-0900-alice.jsonl", aliceHash)
		}
		return tcPath, discussionsDir
	}

	t.Run("no fact files — finds all", func(t *testing.T) {
		tcPath, _ := setup(t, false)
		pending, err := scanPendingDiscussions(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 2 {
			t.Errorf("got %d pending, want 2", len(pending))
		}
	})

	t.Run("fact files with matching source_hash — finds none", func(t *testing.T) {
		tcPath, _ := setup(t, true)
		pending, err := scanPendingDiscussions(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0 (both have matching source_hash)", len(pending))
		}
	})

	t.Run("stale source_hash — re-processes changed discussion", func(t *testing.T) {
		tcPath, _ := setup(t, true)
		// overwrite ryan's fact file with stale hash
		factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
		writeFactFileWithHash(t, factsDir, "2026-03-10-1423-ryan.jsonl", "stale-hash")
		pending, err := scanPendingDiscussions(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ryan has stale hash → re-process; alice has matching hash → skip
		if len(pending) != 1 {
			t.Errorf("got %d pending, want 1", len(pending))
		}
		if len(pending) > 0 && pending[0].DirName != "2026-03-10-1423-ryan" {
			t.Errorf("expected ryan to be pending, got %s", pending[0].DirName)
		}
	})

	t.Run("legacy fact file without source_hash — re-processes", func(t *testing.T) {
		tcPath, _ := setup(t, false)
		factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
		os.MkdirAll(factsDir, 0o755)
		// write JSONL without source_hash
		os.WriteFile(filepath.Join(factsDir, "2026-03-10-1423-ryan.jsonl"),
			[]byte(`{"_meta":{"schema_version":"2","source_type":"discussion"}}`), 0o644)
		pending, err := scanPendingDiscussions(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ryan has no source_hash → re-process; alice has no fact file → process
		if len(pending) != 2 {
			t.Errorf("got %d pending, want 2", len(pending))
		}
	})

	t.Run("legacy .md fact file — re-processes for upgrade", func(t *testing.T) {
		tcPath, _ := setup(t, false)
		factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
		os.MkdirAll(factsDir, 0o755)
		os.WriteFile(filepath.Join(factsDir, "2026-03-10-1423-ryan.md"),
			[]byte("# Facts\nold markdown"), 0o644)
		pending, err := scanPendingDiscussions(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ryan has legacy .md → re-process; alice has no fact file → process
		if len(pending) != 2 {
			t.Errorf("got %d pending, want 2", len(pending))
		}
	})

	t.Run("since filters out older discussions", func(t *testing.T) {
		tcPath, _ := setup(t, false)
		// since = March 11 00:00 UTC → ryan (March 10) excluded, alice (March 11) included
		since := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
		pending, err := scanPendingDiscussions(tcPath, since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("expected 1 pending (alice only), got %d", len(pending))
		}
		if pending[0].Title != "Sprint Planning" {
			t.Errorf("expected Sprint Planning, got %s", pending[0].Title)
		}
	})

	t.Run("since excludes all when all are older", func(t *testing.T) {
		tcPath, _ := setup(t, false)
		since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		pending, err := scanPendingDiscussions(tcPath, since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("expected 0 pending, got %d", len(pending))
		}
	})
}

// writeFactFileWithHash writes a minimal JSONL fact file with the given source_hash.
func writeFactFileWithHash(t *testing.T, factsDir, filename, sourceHash string) {
	t.Helper()
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := `{"_meta":{"schema_version":"2","source_type":"discussion","source_hash":"` + sourceHash + `"}}`
	if err := os.WriteFile(filepath.Join(factsDir, filename), []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanPendingDiscussionsSorted(t *testing.T) {
	tcPath := t.TempDir()
	discussionsDir := filepath.Join(tcPath, "discussions")

	// create in reverse order
	createDiscussionDir(t, discussionsDir, "2026-03-11-0900-alice", "Later", "2026-03-11T09:00:00Z")
	createDiscussionDir(t, discussionsDir, "2026-03-10-1423-ryan", "Earlier", "2026-03-10T14:23:00Z")

	pending, err := scanPendingDiscussions(tcPath, time.Time{})
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

	pending, err := scanPendingDiscussions(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 valid discussion, got %d", len(pending))
	}
}

func TestScanPendingDiscussionsNoDir(t *testing.T) {
	tcPath := t.TempDir() // no discussions/ subdir

	pending, err := scanPendingDiscussions(tcPath, time.Time{})
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

	pending, err := scanPendingDiscussions(tcPath, time.Time{})
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

func TestReadFactFileSourceHash(t *testing.T) {
	t.Run("reads source_hash from JSONL header", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")
		os.WriteFile(path, []byte(`{"_meta":{"schema_version":"2","source_hash":"abc123"}}`+"\n"), 0o644)
		if got := readFactFileSourceHash(path); got != "abc123" {
			t.Errorf("got %q, want abc123", got)
		}
	})

	t.Run("returns empty for missing file", func(t *testing.T) {
		if got := readFactFileSourceHash("/nonexistent/path.jsonl"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("returns empty for JSONL without source_hash", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")
		os.WriteFile(path, []byte(`{"_meta":{"schema_version":"2"}}`+"\n"), 0o644)
		if got := readFactFileSourceHash(path); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("returns empty for non-JSON content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")
		os.WriteFile(path, []byte("# Markdown header\nnot json"), 0o644)
		if got := readFactFileSourceHash(path); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestReadPendingDiscussionFacts(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write two fact files with footer dates
	os.WriteFile(filepath.Join(factsDir, "2026-03-10-1423-ryan.md"),
		[]byte("Fact A\n\n---\n*Extracted from discussion: 2026-03-10-1423-ryan (created 2026-03-10)*\n"), 0o644)
	os.WriteFile(filepath.Join(factsDir, "2026-03-11-0900-alice.md"),
		[]byte("Fact B\n\n---\n*Extracted from discussion: 2026-03-11-0900-alice (created 2026-03-11)*\n"), 0o644)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	totalFacts := 0
	for _, facts := range factsByDay {
		totalFacts += len(facts)
	}
	if totalFacts != 2 {
		t.Errorf("expected 2 total facts, got %d", totalFacts)
	}
	for _, facts := range factsByDay {
		for _, f := range facts {
			if !strings.HasPrefix(f.RelPath, "memory/.discussion-facts/") {
				t.Errorf("expected relative path, got %q", f.RelPath)
			}
		}
	}
}

func TestReadDiscussionFacts_ParsesDateFromFooter(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	// Write fact file with footer date — set mtime to 2020 to prove it's ignored
	factPath := filepath.Join(factsDir, "some-discussion.md")
	os.WriteFile(factPath, []byte("Facts here\n\n---\n*Extracted from discussion: test (created 2026-03-10)*\n"), 0o644)
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(factPath, oldTime, oldTime)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := factsByDay["2026-03-10"]; !ok {
		t.Errorf("expected facts grouped under 2026-03-10, got keys: %v", factsByDay)
	}
}

func TestReadDiscussionFacts_FallbackToFilename(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	// No footer date — should fall back to filename prefix
	os.WriteFile(filepath.Join(factsDir, "2026-03-11-1423-ryan.md"),
		[]byte("Facts without footer date"), 0o644)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := factsByDay["2026-03-11"]; !ok {
		t.Errorf("expected facts grouped under 2026-03-11, got keys: %v", factsByDay)
	}
}

func TestReadDiscussionFacts_GroupsByDate(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	os.WriteFile(filepath.Join(factsDir, "2026-03-10-ryan.md"),
		[]byte("Day 1 facts\n\n---\n*(created 2026-03-10)*\n"), 0o644)
	os.WriteFile(filepath.Join(factsDir, "2026-03-11-alice.md"),
		[]byte("Day 2 facts\n\n---\n*(created 2026-03-11)*\n"), 0o644)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(factsByDay) != 2 {
		t.Errorf("expected 2 date groups, got %d", len(factsByDay))
	}
}

func TestReadDiscussionFacts_SinceFilter(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	os.WriteFile(filepath.Join(factsDir, "2026-03-08-old.md"),
		[]byte("Old fact\n\n---\n*(created 2026-03-08)*\n"), 0o644)
	os.WriteFile(filepath.Join(factsDir, "2026-03-11-new.md"),
		[]byte("New fact\n\n---\n*(created 2026-03-11)*\n"), 0o644)

	since := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	factsByDay, err := readPendingDiscussionFacts(tcPath, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := factsByDay["2026-03-08"]; ok {
		t.Error("expected old facts to be filtered out by since")
	}
	if _, ok := factsByDay["2026-03-11"]; !ok {
		t.Error("expected new facts to be included")
	}
}

func TestReadPendingDiscussionFactsEmptyDir(t *testing.T) {
	tcPath := t.TempDir() // no .discussion-facts dir

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(factsByDay) != 0 {
		t.Errorf("expected 0 groups for nonexistent dir, got %d", len(factsByDay))
	}
}

func TestReadDiscussionFacts_JSONL(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	// Write a JSONL fact file with _meta header
	jsonlContent := `{"_meta":{"schema_version":"2","source_type":"discussion","recorded_at":"2026-03-10T14:23:00Z"}}
{"headline":"Chose PostgreSQL","category":"decision","timestamp":"2026-03-10T14:23:00Z"}
`
	os.WriteFile(filepath.Join(factsDir, "2026-03-10-1423-ryan.jsonl"), []byte(jsonlContent), 0o644)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := factsByDay["2026-03-10"]; !ok {
		t.Errorf("expected facts grouped under 2026-03-10, got keys: %v", factsByDay)
	}
	facts := factsByDay["2026-03-10"]
	if len(facts) != 1 {
		t.Errorf("expected 1 fact entry, got %d", len(facts))
	}
	if !strings.HasSuffix(facts[0].RelPath, ".jsonl") {
		t.Errorf("expected .jsonl relpath, got %q", facts[0].RelPath)
	}
}

func TestReadDiscussionFacts_MixedMDAndJSONL(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	os.MkdirAll(factsDir, 0o755)

	// Write a legacy .md fact file
	os.WriteFile(filepath.Join(factsDir, "2026-03-10-1423-ryan.md"),
		[]byte("Fact A\n\n---\n*Extracted from discussion: 2026-03-10-1423-ryan (created 2026-03-10)*\n"), 0o644)

	// Write a new .jsonl fact file
	jsonlContent := `{"_meta":{"schema_version":"2","source_type":"discussion","recorded_at":"2026-03-11T09:00:00Z"}}
{"headline":"Sprint velocity increased","category":"learning","timestamp":"2026-03-11T09:00:00Z"}
`
	os.WriteFile(filepath.Join(factsDir, "2026-03-11-0900-alice.jsonl"), []byte(jsonlContent), 0o644)

	factsByDay, err := readPendingDiscussionFacts(tcPath, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	totalFacts := 0
	for _, facts := range factsByDay {
		totalFacts += len(facts)
	}
	if totalFacts != 2 {
		t.Errorf("expected 2 total facts (1 md + 1 jsonl), got %d", totalFacts)
	}
	if _, ok := factsByDay["2026-03-10"]; !ok {
		t.Error("expected facts for 2026-03-10 (from .md)")
	}
	if _, ok := factsByDay["2026-03-11"]; !ok {
		t.Error("expected facts for 2026-03-11 (from .jsonl)")
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

func TestDistillStateRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &distillStateV2{
		SchemaVersion: "2",
		TeamID:        "team-xyz",
		LastWeekly:    "2026-03-29T23:59:59Z",
		LastMonthly:   "2026-03-31T23:59:59Z",
	}

	if err := saveDistillStateV2(tmp, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := loadDistillStateV2(tmp, tmp)
	if loaded.LastWeekly != "2026-03-29T23:59:59Z" {
		t.Errorf("expected LastWeekly preserved, got %q", loaded.LastWeekly)
	}
	if loaded.LastMonthly != "2026-03-31T23:59:59Z" {
		t.Errorf("expected LastMonthly preserved, got %q", loaded.LastMonthly)
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
		{"facts only", 0, 3, "3 facts"},
		{"both sources", 5, 3, "5 observations and 3 facts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := formatDailyMemory("2026-03-11", "content", tt.obsCount, tt.discCount, nil, nil)
			if !strings.Contains(content, tt.wantSource) {
				t.Errorf("expected %q in output, got:\n%s", tt.wantSource, content)
			}
		})
	}
}

func TestCategorizeAnnotations(t *testing.T) {
	tests := []struct {
		name          string
		annotations   []annotationJSON
		wantDecisions int
		wantLearnings int
		wantActions   int
		wantOpenQs    int
	}{
		{
			name:        "nil input",
			annotations: nil,
		},
		{
			name: "all types",
			annotations: []annotationJSON{
				{Type: "decision", Content: "use postgres"},
				{Type: "action-item", Content: "migrate by friday"},
				{Type: "disagreement", Content: "team split on caching"},
				{Type: "insight", Content: "latency is the bottleneck"},
				{Type: "learning", Content: "redis works well here"},
				{Type: "question", Content: "what about failover?"},
				{Type: "consensus", Content: "agreed on postgres"},
			},
			wantDecisions: 2, // decision + consensus
			wantLearnings: 2, // insight + learning
			wantActions:   1,
			wantOpenQs:    2, // disagreement + question
		},
		{
			name: "unknown types ignored",
			annotations: []annotationJSON{
				{Type: "decision", Content: "a"},
				{Type: "unknown-type", Content: "b"},
			},
			wantDecisions: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var af *discussion.AnnotationsFile
			if tt.annotations != nil {
				af = &discussion.AnnotationsFile{}
				for _, a := range tt.annotations {
					af.Annotations = append(af.Annotations, discussion.Annotation{
						Type: a.Type, Content: a.Content,
					})
				}
			}
			d, l, a, o := categorizeAnnotations(af)
			if len(d) != tt.wantDecisions {
				t.Errorf("decisions: got %d, want %d", len(d), tt.wantDecisions)
			}
			if len(l) != tt.wantLearnings {
				t.Errorf("learnings: got %d, want %d", len(l), tt.wantLearnings)
			}
			if len(a) != tt.wantActions {
				t.Errorf("actions: got %d, want %d", len(a), tt.wantActions)
			}
			if len(o) != tt.wantOpenQs {
				t.Errorf("open questions: got %d, want %d", len(o), tt.wantOpenQs)
			}
		})
	}
}

// annotationJSON is a test helper for building annotation fixtures.
type annotationJSON struct {
	Type    string
	Content string
}

func TestExtractFactsFromSummaryJSON(t *testing.T) {
	// helper to build a valid v2 summary base
	v2Base := func() map[string]any {
		return map[string]any{
			"schema_version": 2,
			"recording_id":   "rec-test",
			"title":          "Test",
			"human_summary":  "summary",
		}
	}

	t.Run("categorized facts present — outputs JSONL with source_hash", func(t *testing.T) {
		dir := t.TempDir()
		s := v2Base()
		s["chapters"] = []map[string]any{
			{"id": "ch-1", "title": "Auth design", "summary": "Token rotation", "importance": 0.9, "time_range": []float64{0, 60}, "cue_range": []int{0, 10}},
		}
		s["decisions"] = []map[string]any{{"description": "use rotating tokens"}}
		s["learnings"] = []map[string]any{{"description": "redis handles TTL well"}}
		s["action_items"] = []map[string]any{{"description": "migrate by friday"}}
		s["open_questions"] = []map[string]any{{"question": "failover strategy?"}}
		s["requirements"] = []map[string]any{{"description": "must support SAML SSO"}}
		s["constraints"] = []string{"compliance requirement"}
		s["technical_context"] = map[string]any{
			"technologies": []string{"Redis 7.x"},
			"architecture": []string{"token service is stateless"},
			"integrations": []string{"Okta SAML provider"},
			"notes":        []string{"latency budget is 50ms"},
		}
		writeSummaryJSON(t, dir, s)

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Auth Review",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash-123", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// verify JSONL header contains source_hash
		firstLine, _, _ := strings.Cut(output, "\n")
		assertContains(t, firstLine, `"source_hash":"test-hash-123"`)
		assertContains(t, firstLine, `"schema_version":"2"`)

		// verify fact content
		assertContains(t, output, "use rotating tokens")
		assertContains(t, output, "redis handles TTL well")
		assertContains(t, output, "migrate by friday")
		assertContains(t, output, "failover strategy?")
		assertContains(t, output, "must support SAML SSO")
		assertContains(t, output, "compliance requirement")
		assertContains(t, output, "Redis 7.x")
		assertContains(t, output, "token service is stateless")
		assertContains(t, output, "Okta SAML provider")
		assertContains(t, output, "latency budget is 50ms")

		// gh #476 regression: every fact line MUST carry source_url and
		// source_title so the citation pipeline can render clickable links
		// in the final daily summary. The recording_id comes from summary.json
		// (set by v2Base() helper as "rec-test").
		wantURL := "https://sageox.ai/team/team_test/media/recordings/rec-test"
		assertContains(t, output, `"source_url":"`+wantURL+`"`)
		// title comes from summary.json title field (set by v2Base() as "Test")
		assertContains(t, output, `"source_title":"Test"`)
	})

	// TestExtractFactsFromSummaryJSON_NoRecordingID covers the audio-only /
	// legacy fallback: when summary.json has no recording_id, source_url is
	// empty (graceful degradation — citation pipeline renders label-only).
	// Failure prevented: panic / broken URL when discussions lack a recording.
	t.Run("missing recording_id leaves source_url empty", func(t *testing.T) {
		dir := t.TempDir()
		writeSummaryJSON(t, dir, map[string]any{
			"schema_version": 2,
			"recording_id":   "", // legacy / audio-only
			"title":          "Audio-only sync",
			"human_summary":  "summary",
			"decisions":      []map[string]any{{"description": "ship the demo"}},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Audio-only sync",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, output, "ship the demo")
		// source_url should be omitted (empty) — JSON omitempty drops the field entirely
		if strings.Contains(output, `"source_url":`) {
			t.Errorf("expected source_url omitted when recording_id is empty, got:\n%s", output)
		}
		// source_title should still be present
		assertContains(t, output, `"source_title":"Audio-only sync"`)
	})

	t.Run("v1 summary with categorized facts — succeeds without chapters", func(t *testing.T) {
		dir := t.TempDir()
		writeSummaryJSON(t, dir, map[string]any{
			"schema_version": 1,
			"recording_id":   "rec-test",
			"title":          "Old",
			"human_summary":  "summary",
			"decisions":      []map[string]any{{"description": "use postgres"}},
			"learnings":      []map[string]any{{"description": "caching helps"}},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "V1 Schema",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "v1-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, output, "use postgres")
		assertContains(t, output, "caching helps")
	})

	t.Run("no categorized facts — returns error for LLM fallback", func(t *testing.T) {
		dir := t.TempDir()
		writeSummaryJSON(t, dir, map[string]any{
			"schema_version": 2,
			"recording_id":   "rec-test",
			"title":          "Empty",
			"human_summary":  "summary",
			"chapters":       []map[string]any{},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "No Facts",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		_, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err == nil {
			t.Fatal("expected error for empty facts")
		}
		assertContains(t, err.Error(), "no categorized facts")
	})

	t.Run("missing summary.json — returns error", func(t *testing.T) {
		dir := t.TempDir()

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Missing",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		_, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err == nil {
			t.Fatal("expected error for missing summary.json")
		}
	})

	t.Run("v1 summary excludes chapters from key context", func(t *testing.T) {
		dir := t.TempDir()
		writeSummaryJSON(t, dir, map[string]any{
			"schema_version":    1,
			"recording_id":     "rec-test",
			"title":            "V1 With Chapters",
			"human_summary":    "summary",
			"decisions":        []map[string]any{{"description": "keep monolith"}},
			"constraints":      []string{"budget limited"},
			"technical_context": map[string]any{"notes": []string{"uses AWS"}},
			// chapters might be present in v1 JSON but should not be used
			"chapters": []map[string]any{
				{"id": "ch-1", "title": "Planning", "summary": "Should be excluded", "importance": 0.9, "time_range": []float64{0, 60}, "cue_range": []int{0, 10}},
			},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "V1 Chapters Test",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContains(t, output, "keep monolith")
		assertContains(t, output, "budget limited")
		assertContains(t, output, "uses AWS")
		if strings.Contains(output, "Should be excluded") {
			t.Error("v1 summary should not include chapters in key context")
		}
	})

	t.Run("key context from constraints + non-goals + chapter summaries", func(t *testing.T) {
		dir := t.TempDir()
		s := v2Base()
		s["chapters"] = []map[string]any{
			{"id": "ch-1", "title": "Design", "summary": "Important context from chapter", "importance": 0.9, "time_range": []float64{0, 60}, "cue_range": []int{0, 10}},
			{"id": "ch-2", "title": "Trivial", "summary": "Low importance", "importance": 0.3, "time_range": []float64{60, 120}, "cue_range": []int{10, 20}},
		}
		s["decisions"] = []map[string]any{{"description": "use postgres"}}
		s["constraints"] = []string{"must be HIPAA compliant"}
		s["non_goals"] = []string{"mobile support"}
		s["technical_context"] = map[string]any{"notes": []string{"team prefers Go"}}
		writeSummaryJSON(t, dir, s)

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Context Test",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		assertContains(t, output, "team prefers Go")
		assertContains(t, output, "must be HIPAA compliant")
		assertContains(t, output, "mobile support")
		assertContains(t, output, "Important context from chapter")

		// low-importance chapter should be excluded
		if strings.Contains(output, "Low importance") {
			t.Error("low-importance chapter should be excluded from key context")
		}
	})

	t.Run("categorized facts do NOT re-append annotations", func(t *testing.T) {
		dir := t.TempDir()
		s := v2Base()
		s["chapters"] = []map[string]any{
			{"id": "ch-1", "title": "Design", "importance": 0.5, "summary": "", "time_range": []float64{0, 60}, "cue_range": []int{0, 10}},
		}
		s["decisions"] = []map[string]any{{"description": "use postgres"}}
		writeSummaryJSON(t, dir, s)
		writeAnnotationsJSON(t, dir, []map[string]any{
			{"type": "decision", "content": "use postgres"},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Dedup Test",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// "use postgres" should appear exactly once (from categorized facts only)
		if strings.Count(output, "use postgres") != 1 {
			t.Errorf("expected 'use postgres' exactly once, got %d occurrences", strings.Count(output, "use postgres"))
		}
	})

	t.Run("low-importance chapters excluded from key context", func(t *testing.T) {
		dir := t.TempDir()
		s := v2Base()
		s["chapters"] = []map[string]any{
			{"id": "ch-1", "title": "Important", "summary": "Key insight", "importance": 0.8, "time_range": []float64{0, 60}, "cue_range": []int{0, 10}},
			{"id": "ch-2", "title": "Trivial", "summary": "Smalltalk", "importance": 0.3, "time_range": []float64{60, 120}, "cue_range": []int{10, 20}},
		}
		s["decisions"] = []map[string]any{{"description": "a decision"}}
		writeSummaryJSON(t, dir, s)

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Importance Test",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		assertContains(t, output, "Key insight")
		if strings.Contains(output, "Smalltalk") {
			t.Error("low-importance chapter should be excluded")
		}
	})

	// CodeRabbit #3: when summary.json omits recording_id / title, fall back
	// to the metadata.json values that scanPendingDiscussions already loaded
	// into discussionInput. Older or partial server summaries should still
	// produce clickable citations.
	//
	// Failure prevented: a discussion with metadata.json but a stripped-down
	// summary.json silently loses its recording link in distilled summaries.
	t.Run("metadata.json fallback when summary.json omits recording_id and title", func(t *testing.T) {
		dir := t.TempDir()
		writeSummaryJSON(t, dir, map[string]any{
			"schema_version": 2,
			// recording_id and title intentionally absent / empty
			"recording_id":  "",
			"title":         "",
			"human_summary": "summary",
			"decisions":     []map[string]any{{"description": "ship the demo"}},
		})

		d := discussionInput{
			DirName:        "2026-03-20-1423-person",
			Title:          "Title from metadata.json",
			RecordingID:    "rec_from_metadata",
			CreatedAt:      time.Date(2026, 3, 20, 14, 23, 0, 0, time.UTC),
			SummaryJSONDir: dir,
		}

		output, err := extractFactsFromSummaryJSON(d, "test-hash", "team_test", "https://sageox.ai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// source_url should be built from the metadata.json fallback recording_id
		assertContains(t, output, `"source_url":"https://sageox.ai/team/team_test/media/recordings/rec_from_metadata"`)
		// source_title should fall back to the metadata.json title
		assertContains(t, output, `"source_title":"Title from metadata.json"`)
	})
}

// test helpers

func writeSummaryJSON(t *testing.T, dir string, data map[string]any) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAnnotationsJSON(t *testing.T, dir string, annotations []map[string]any) {
	t.Helper()
	data := map[string]any{"annotations": annotations}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "annotations.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q, got:\n%s", substr, s)
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
