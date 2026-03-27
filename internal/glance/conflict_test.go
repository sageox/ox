package glance

import (
	"testing"
	"time"
)

func TestDetectConflicts_NoOverlap(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"a.go", "b.go"}},
		{Name: "s2", User: "bob", Files: []string{"c.go", "d.go"}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 0 {
		t.Errorf("expected 0 overlaps, got %d", len(report.Overlaps))
	}
	if report.TotalFiles != 4 {
		t.Errorf("expected 4 total files, got %d", report.TotalFiles)
	}
}

func TestDetectConflicts_SingleOverlap(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"shared.go", "a.go"}},
		{Name: "s2", User: "bob", Files: []string{"shared.go", "b.go"}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 1 {
		t.Fatalf("expected 1 overlap, got %d", len(report.Overlaps))
	}
	if report.Overlaps[0].FilePath != "shared.go" {
		t.Errorf("expected shared.go, got %s", report.Overlaps[0].FilePath)
	}
	if len(report.Overlaps[0].Authors) != 2 {
		t.Errorf("expected 2 authors, got %d", len(report.Overlaps[0].Authors))
	}
}

func TestDetectConflicts_MultipleAuthors(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"hot.go"}},
		{Name: "s2", User: "bob", Files: []string{"hot.go"}},
		{Name: "s3", User: "charlie", Files: []string{"hot.go"}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 1 {
		t.Fatalf("expected 1 overlap, got %d", len(report.Overlaps))
	}
	if len(report.Overlaps[0].Authors) != 3 {
		t.Errorf("expected 3 authors, got %d", len(report.Overlaps[0].Authors))
	}
	// Should have 3 author pairs: alice|bob, alice|charlie, bob|charlie
	pairs := report.OverlapPairs()
	if len(pairs) != 3 {
		t.Errorf("expected 3 pairs, got %d", len(pairs))
	}
}

func TestDetectConflicts_SameAuthorNoConflict(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"a.go"}},
		{Name: "s2", User: "alice", Files: []string{"a.go"}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 0 {
		t.Errorf("same author touching same file should not be a conflict, got %d overlaps", len(report.Overlaps))
	}
}

func TestDetectConflicts_SortedByHotness(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"cool.go", "hot.go"}},
		{Name: "s2", User: "bob", Files: []string{"cool.go", "hot.go"}},
		{Name: "s3", User: "charlie", Files: []string{"hot.go"}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 2 {
		t.Fatalf("expected 2 overlaps, got %d", len(report.Overlaps))
	}
	// hot.go has 3 authors, cool.go has 2 — hot.go should be first
	if report.Overlaps[0].FilePath != "hot.go" {
		t.Errorf("expected hot.go first (3 authors), got %s", report.Overlaps[0].FilePath)
	}
}

func TestDetectConflicts_EmptyFiles(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: nil},
		{Name: "s2", User: "bob", Files: []string{}},
	}
	report := DetectConflicts(sessions)
	if len(report.Overlaps) != 0 {
		t.Errorf("expected 0 overlaps, got %d", len(report.Overlaps))
	}
}

func TestOverlapPairs_Sorted(t *testing.T) {
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Files: []string{"a.go", "b.go", "c.go"}},
		{Name: "s2", User: "bob", Files: []string{"a.go"}},
		{Name: "s3", User: "charlie", Files: []string{"a.go", "b.go", "c.go"}},
	}
	report := DetectConflicts(sessions)
	pairs := report.OverlapPairs()
	// alice|charlie should have most shared files (3), then alice|bob (1), bob|charlie (1)
	if len(pairs) < 1 {
		t.Fatal("expected at least 1 pair")
	}
	if pairs[0].SharedFiles < pairs[len(pairs)-1].SharedFiles {
		t.Error("pairs should be sorted by shared_files descending")
	}
}

func TestGroupByAuthor(t *testing.T) {
	now := time.Now()
	sessions := []SessionRecord{
		{Name: "s1", User: "alice", Time: now, Files: []string{"a.go"}},
		{Name: "s2", User: "bob", Time: now.Add(-time.Hour), Files: []string{"b.go", "c.go"}},
		{Name: "s3", User: "alice", Time: now.Add(-2 * time.Hour), Files: []string{"d.go"}},
	}
	authors := GroupByAuthor(sessions)
	if len(authors) != 2 {
		t.Fatalf("expected 2 authors, got %d", len(authors))
	}
	// Alice should be first (most recent session)
	if authors[0].Name != "alice" {
		t.Errorf("expected alice first, got %s", authors[0].Name)
	}
	if authors[0].SessionCount != 2 {
		t.Errorf("expected 2 sessions for alice, got %d", authors[0].SessionCount)
	}
	if authors[0].FilesTouched != 2 {
		t.Errorf("expected 2 files touched for alice, got %d", authors[0].FilesTouched)
	}
	if authors[1].FilesTouched != 2 {
		t.Errorf("expected 2 files touched for bob, got %d", authors[1].FilesTouched)
	}
}
