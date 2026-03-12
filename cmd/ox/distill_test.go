package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetermineLayers(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name     string
		state    *distillStateV2
		explicit string
		want     []string
	}{
		{
			name:     "explicit daily",
			state:    &distillStateV2{},
			explicit: "daily",
			want:     []string{"daily"},
		},
		{
			name:  "fresh state triggers all layers",
			state: &distillStateV2{},
			want:  []string{"daily", "weekly", "monthly"},
		},
		{
			name: "recent weekly skips weekly",
			state: &distillStateV2{
				LastWeekly:  now.Add(-24 * time.Hour).Format(time.RFC3339),
				LastMonthly: now.Add(-24 * time.Hour).Format(time.RFC3339),
			},
			want: []string{"daily"},
		},
		{
			name: "8 days since weekly triggers weekly",
			state: &distillStateV2{
				LastWeekly:  now.Add(-8 * 24 * time.Hour).Format(time.RFC3339),
				LastMonthly: now.Add(-24 * time.Hour).Format(time.RFC3339),
			},
			want: []string{"daily", "weekly"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := determineLayers(tt.state, tt.explicit, now)
			if len(got) != len(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d]=%s, want %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEnsureMemoryDirs(t *testing.T) {
	tmp := t.TempDir()

	if err := ensureMemoryDirs(tmp); err != nil {
		t.Fatalf("ensureMemoryDirs: %v", err)
	}

	for _, sub := range []string{"memory/daily", "memory/weekly", "memory/monthly"} {
		path := filepath.Join(tmp, sub)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", sub)
		}
	}
}

func TestSeedMemoryMD(t *testing.T) {
	tmp := t.TempDir()

	// init git repo so commitMemoryFile doesn't fail
	initGitRepo(t, tmp)

	// first call creates the file
	if err := seedMemoryMD(tmp); err != nil {
		t.Fatalf("seedMemoryMD: %v", err)
	}

	memPath := filepath.Join(tmp, "MEMORY.md")
	data, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if len(data) == 0 {
		t.Error("MEMORY.md should not be empty")
	}

	// second call should not overwrite
	if err := os.WriteFile(memPath, []byte("custom content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedMemoryMD(tmp); err != nil {
		t.Fatalf("seedMemoryMD (second call): %v", err)
	}
	data, err = os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom content" {
		t.Error("seedMemoryMD should not overwrite existing MEMORY.md")
	}
}

func TestWriteMemoryFile(t *testing.T) {
	tmp := t.TempDir()

	content := "# Test\n\ntest content\n"
	relPath := filepath.Join("memory", "daily", "2026-03-11.md")

	if err := writeMemoryFile(tmp, relPath, content); err != nil {
		t.Fatalf("writeMemoryFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, relPath))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}
}

func TestFormatDailyMemory(t *testing.T) {
	content := formatDailyMemory("2026-03-11", "Some distilled content", 5, 0)
	if content == "" {
		t.Error("expected non-empty content")
	}
	if !strings.Contains(content, "Daily Memory") {
		t.Error("should contain 'Daily Memory'")
	}
	if !strings.Contains(content, "5 observations") {
		t.Error("should contain observation count")
	}
}

func TestReadRecentMemoryFiles(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// create some daily files
	files := []struct {
		name    string
		content string
	}{
		{"2026-03-09.md", "day 1 content"},
		{"2026-03-10.md", "day 2 content"},
		{"2026-03-11.md", "day 3 content"},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dailyDir, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	contents, names, err := readRecentMemoryFiles(dailyDir, 2)
	if err != nil {
		t.Fatalf("readRecentMemoryFiles: %v", err)
	}
	if len(contents) != 2 {
		t.Errorf("expected 2 files, got %d", len(contents))
	}
	// most recent first
	if len(names) > 0 && names[0] != "2026-03-11.md" {
		t.Errorf("expected most recent first, got %s", names[0])
	}
}

func TestReadRecentMemoryFilesEmpty(t *testing.T) {
	contents, names, err := readRecentMemoryFiles("/nonexistent/path", 5)
	if err != nil {
		t.Errorf("expected nil error for nonexistent dir, got %v", err)
	}
	if contents != nil || names != nil {
		t.Error("expected nil results for nonexistent dir")
	}
}

func TestDistillStateV2Migration(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write v1 state
	v1State := &distillState{
		SchemaVersion:    "1",
		LastDistilled:    "2026-03-10T12:00:00Z",
		ObservationCount: 10,
		TeamID:           "team-abc",
	}
	if err := saveDistillState(tmp, v1State); err != nil {
		t.Fatal(err)
	}

	// load as v2
	state := loadDistillStateV2(tmp)
	if state.TeamID != "team-abc" {
		t.Errorf("expected team-abc, got %s", state.TeamID)
	}

	// v1's LastDistilled should be used as fallback for lastDailyTime
	daily := state.lastDailyTime()
	if daily.IsZero() {
		t.Error("lastDailyTime should fall back to v1 LastDistilled")
	}
}

func TestDistillStateV2SaveLoad(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &distillStateV2{
		SchemaVersion: "2",
		TeamID:        "team-xyz",
		LastDaily:     "2026-03-11T10:00:00Z",
		LastWeekly:    "2026-03-10T10:00:00Z",
		DailyCount:    15,
	}

	if err := saveDistillStateV2(tmp, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := loadDistillStateV2(tmp)
	if loaded.TeamID != "team-xyz" {
		t.Errorf("expected team-xyz, got %s", loaded.TeamID)
	}
	if loaded.LastDaily != "2026-03-11T10:00:00Z" {
		t.Errorf("expected LastDaily 2026-03-11T10:00:00Z, got %s", loaded.LastDaily)
	}
	if loaded.DailyCount != 15 {
		t.Errorf("expected DailyCount 15, got %d", loaded.DailyCount)
	}
}

func TestDistillStateV2LastTimes(t *testing.T) {
	t.Run("zero state returns zero times", func(t *testing.T) {
		state := &distillStateV2{}
		if !state.lastDailyTime().IsZero() {
			t.Error("expected zero lastDailyTime for empty state")
		}
		if !state.lastWeeklyTime().IsZero() {
			t.Error("expected zero lastWeeklyTime for empty state")
		}
		if !state.lastMonthlyTime().IsZero() {
			t.Error("expected zero lastMonthlyTime for empty state")
		}
	})

	t.Run("lastDaily prefers LastDaily over LastDistilled", func(t *testing.T) {
		state := &distillStateV2{
			LastDaily:     "2026-03-11T10:00:00Z",
			LastDistilled: "2026-03-01T10:00:00Z",
		}
		daily := state.lastDailyTime()
		expected, _ := time.Parse(time.RFC3339, "2026-03-11T10:00:00Z")
		if !daily.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, daily)
		}
	})

	t.Run("lastDaily falls back to LastDistilled", func(t *testing.T) {
		state := &distillStateV2{
			LastDistilled: "2026-03-01T10:00:00Z",
		}
		daily := state.lastDailyTime()
		if daily.IsZero() {
			t.Error("expected non-zero lastDailyTime from LastDistilled fallback")
		}
	})
}

func TestLoadDistillGuidelines(t *testing.T) {
	tmp := t.TempDir()

	// no file — returns empty
	got := loadDistillGuidelines(tmp)
	if got != "" {
		t.Errorf("expected empty string for missing DISTILL.md, got %q", got)
	}

	// with file — returns content
	content := "Always track security decisions.\nIgnore dependency bumps."
	if err := os.WriteFile(filepath.Join(tmp, "DISTILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got = loadDistillGuidelines(tmp)
	if got != content {
		t.Errorf("expected %q, got %q", content, got)
	}
}

func TestContentHash(t *testing.T) {
	h1 := contentHash("a", "b")
	h2 := contentHash("a", "b")
	h3 := contentHash("a", "c")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 16 {
		t.Errorf("expected 16-char hash, got %d", len(h1))
	}
}

func TestCommitMemoryFile(t *testing.T) {
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	// write a file and commit it
	relPath := filepath.Join("memory", "daily", "2026-03-11.md")
	fullPath := filepath.Join(tmp, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := commitMemoryFile(tmp, relPath, "test commit"); err != nil {
		t.Fatalf("commitMemoryFile: %v", err)
	}

	// committing again with no changes should not error
	if err := commitMemoryFile(tmp, relPath, "no-op commit"); err != nil {
		t.Fatalf("commitMemoryFile (no changes): %v", err)
	}
}
