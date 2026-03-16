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

	t.Run("explicit daily", func(t *testing.T) {
		plan := determineLayers(&distillStateV2{}, "daily", now)
		if !plan.Daily {
			t.Error("expected Daily=true")
		}
		if len(plan.Weeks) != 0 || len(plan.Months) != 0 {
			t.Error("expected no weeks or months for explicit daily")
		}
	})

	t.Run("fresh state triggers all layers", func(t *testing.T) {
		plan := determineLayers(&distillStateV2{}, "", now)
		if !plan.Daily {
			t.Error("expected Daily=true")
		}
		if len(plan.Weeks) == 0 {
			t.Error("expected at least one week")
		}
		if len(plan.Months) == 0 {
			t.Error("expected at least one month")
		}
	})

	t.Run("recent weekly skips weekly", func(t *testing.T) {
		state := &distillStateV2{
			LastWeekly:  now.Add(-24 * time.Hour).Format(time.RFC3339),
			LastMonthly: now.Add(-24 * time.Hour).Format(time.RFC3339),
		}
		plan := determineLayers(state, "", now)
		if !plan.Daily {
			t.Error("expected Daily=true")
		}
		if len(plan.Weeks) != 0 {
			t.Errorf("expected no weeks, got %d", len(plan.Weeks))
		}
		if len(plan.Months) != 0 {
			t.Errorf("expected no months, got %d", len(plan.Months))
		}
	})

	t.Run("8 days since weekly triggers weekly", func(t *testing.T) {
		state := &distillStateV2{
			LastWeekly:  now.Add(-8 * 24 * time.Hour).Format(time.RFC3339),
			LastMonthly: now.Add(-24 * time.Hour).Format(time.RFC3339),
		}
		plan := determineLayers(state, "", now)
		if !plan.Daily {
			t.Error("expected Daily=true")
		}
		if len(plan.Weeks) == 0 {
			t.Error("expected at least one week")
		}
	})
}

func TestDetermineLayers_MultipleWeeks(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	// 3 weeks ago
	state := &distillStateV2{
		LastWeekly:  now.Add(-21 * 24 * time.Hour).Format(time.RFC3339),
		LastMonthly: now.Add(-24 * time.Hour).Format(time.RFC3339),
	}
	plan := determineLayers(state, "", now)
	if len(plan.Weeks) < 2 {
		t.Errorf("expected at least 2 weeks for 3-week gap, got %d", len(plan.Weeks))
	}
}

func TestDetermineLayers_MultipleMonths(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	// 2+ months ago
	state := &distillStateV2{
		LastWeekly:  now.Add(-24 * time.Hour).Format(time.RFC3339),
		LastMonthly: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	plan := determineLayers(state, "", now)
	if len(plan.Months) < 2 {
		t.Errorf("expected at least 2 months, got %d: %v", len(plan.Months), plan.Months)
	}
}

func TestDetermineLayers_ExplicitLayer(t *testing.T) {
	now := time.Now().UTC()
	plan := determineLayers(&distillStateV2{}, "weekly", now)
	if plan.Daily {
		t.Error("expected Daily=false for explicit weekly")
	}
	if len(plan.Weeks) == 0 {
		t.Error("expected weeks for explicit weekly")
	}
	if len(plan.Months) != 0 {
		t.Error("expected no months for explicit weekly")
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

	// load as v2 (with empty tcPath — no memory files to infer from)
	state := loadDistillStateV2(tmp, tmp)
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

	loaded := loadDistillStateV2(tmp, tmp)
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

// --- New tests for distill fix (#211) ---

func TestGroupObservationsByDay(t *testing.T) {
	obs := []distillObservation{
		{Content: "obs1", RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)},
		{Content: "obs2", RecordedAt: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)},
		{Content: "obs3", RecordedAt: time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)},
		{Content: "obs4", RecordedAt: time.Date(2026, 3, 12, 8, 0, 0, 0, time.UTC)},
	}

	groups := groupObservationsByDay(obs)
	if len(groups) != 3 {
		t.Errorf("expected 3 day groups, got %d", len(groups))
	}
	if len(groups["2026-03-10"]) != 2 {
		t.Errorf("expected 2 obs on 2026-03-10, got %d", len(groups["2026-03-10"]))
	}
	if len(groups["2026-03-11"]) != 1 {
		t.Errorf("expected 1 obs on 2026-03-11, got %d", len(groups["2026-03-11"]))
	}
	if len(groups["2026-03-12"]) != 1 {
		t.Errorf("expected 1 obs on 2026-03-12, got %d", len(groups["2026-03-12"]))
	}
}

func TestGroupObservationsByDay_SingleDay(t *testing.T) {
	obs := []distillObservation{
		{Content: "obs1", RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)},
		{Content: "obs2", RecordedAt: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)},
	}

	groups := groupObservationsByDay(obs)
	if len(groups) != 1 {
		t.Errorf("expected 1 day group, got %d", len(groups))
	}
	if len(groups["2026-03-10"]) != 2 {
		t.Errorf("expected 2 obs, got %d", len(groups["2026-03-10"]))
	}
}

func TestInferDailyHighWater_OldNaming(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	os.WriteFile(filepath.Join(dailyDir, "2026-03-08.md"), []byte("day 1"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10.md"), []byte("day 2"), 0o644)

	got := inferDailyHighWater(tmp)
	want := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInferDailyHighWater_NewNaming(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	os.WriteFile(filepath.Join(dailyDir, "2026-03-10-019526a0-7e8b-7abc-8def-0123456789ab.md"), []byte("day"), 0o644)

	got := inferDailyHighWater(tmp)
	want := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInferDailyHighWater_Mixed(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	os.WriteFile(filepath.Join(dailyDir, "2026-03-08.md"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-11-019526a0-7e8b-7abc-8def-0123456789ab.md"), []byte("new"), 0o644)

	got := inferDailyHighWater(tmp)
	want := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInferDailyHighWater_Empty(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	got := inferDailyHighWater(tmp)
	if !got.IsZero() {
		t.Errorf("expected zero time for empty dir, got %v", got)
	}
}

func TestInferDailyHighWater_NoDir(t *testing.T) {
	tmp := t.TempDir()
	got := inferDailyHighWater(tmp)
	if !got.IsZero() {
		t.Errorf("expected zero time for missing dir, got %v", got)
	}
}

func TestInferWeeklyHighWater(t *testing.T) {
	tmp := t.TempDir()
	weeklyDir := filepath.Join(tmp, "memory", "weekly")
	os.MkdirAll(weeklyDir, 0o755)

	os.WriteFile(filepath.Join(weeklyDir, "2026-W10.md"), []byte("week 10"), 0o644)
	os.WriteFile(filepath.Join(weeklyDir, "2026-W08.md"), []byte("week 8"), 0o644)

	got := inferWeeklyHighWater(tmp)
	_, end := isoWeekRange(2026, 10)
	if !got.Equal(end) {
		t.Errorf("got %v, want %v", got, end)
	}
}

func TestInferMonthlyHighWater(t *testing.T) {
	tmp := t.TempDir()
	monthlyDir := filepath.Join(tmp, "memory", "monthly")
	os.MkdirAll(monthlyDir, 0o755)

	os.WriteFile(filepath.Join(monthlyDir, "2026-02.md"), []byte("feb"), 0o644)
	os.WriteFile(filepath.Join(monthlyDir, "2026-01.md"), []byte("jan"), 0o644)

	got := inferMonthlyHighWater(tmp)
	want := endOfMonth(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestISOWeekRange(t *testing.T) {
	tests := []struct {
		year, week int
		wantStart  time.Time
		wantEnd    time.Time
	}{
		{
			year:      2026,
			week:      10,
			wantStart: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),  // Monday
			wantEnd:   time.Date(2026, 3, 8, 23, 59, 59, 0, time.UTC), // Sunday
		},
		{
			year:      2026,
			week:      1,
			wantStart: time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC), // Monday of ISO week 1 2026
			wantEnd:   time.Date(2026, 1, 4, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		start, end := isoWeekRange(tt.year, tt.week)
		if !start.Equal(tt.wantStart) {
			t.Errorf("isoWeekRange(%d, %d) start = %v, want %v", tt.year, tt.week, start, tt.wantStart)
		}
		if !end.Equal(tt.wantEnd) {
			t.Errorf("isoWeekRange(%d, %d) end = %v, want %v", tt.year, tt.week, end, tt.wantEnd)
		}
		// verify the start is Monday and end is Sunday
		if start.Weekday() != time.Monday {
			t.Errorf("start should be Monday, got %v", start.Weekday())
		}
		if end.Weekday() != time.Sunday {
			t.Errorf("end should be Sunday, got %v", end.Weekday())
		}
	}
}

func TestReadDailyFilesForDateRange(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	os.WriteFile(filepath.Join(dailyDir, "2026-03-08.md"), []byte("day 8"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-09.md"), []byte("day 9"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10.md"), []byte("day 10"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-11.md"), []byte("day 11"), 0o644)

	contents, names, err := readDailyFilesForDateRange(dailyDir, "2026-03-09", "2026-03-10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 2 {
		t.Errorf("expected 2 files, got %d", len(contents))
	}
	if len(names) != 2 || names[0] != "2026-03-09.md" || names[1] != "2026-03-10.md" {
		t.Errorf("expected [2026-03-09.md, 2026-03-10.md], got %v", names)
	}
}

func TestReadDailyFilesForDateRange_MultiplePerDay(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)

	// 3 UUID7 files for same day
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10-aaaa.md"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10-bbbb.md"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10-cccc.md"), []byte("c"), 0o644)

	contents, names, err := readDailyFilesForDateRange(dailyDir, "2026-03-10", "2026-03-10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 3 {
		t.Errorf("expected 3 files, got %d", len(contents))
	}
	if len(names) != 3 {
		t.Errorf("expected 3 names, got %d", len(names))
	}
}

func TestReadWeeklyFilesForMonth(t *testing.T) {
	tmp := t.TempDir()
	weeklyDir := filepath.Join(tmp, "memory", "weekly")
	os.MkdirAll(weeklyDir, 0o755)

	// Week 10: March 2-8, 2026. Overlaps March.
	os.WriteFile(filepath.Join(weeklyDir, "2026-W10.md"), []byte("week 10"), 0o644)
	// Week 9: Feb 23 - Mar 1, 2026. Overlaps both Feb and March.
	os.WriteFile(filepath.Join(weeklyDir, "2026-W09.md"), []byte("week 9"), 0o644)
	// Week 5: Jan 26 - Feb 1. Does not overlap March.
	os.WriteFile(filepath.Join(weeklyDir, "2026-W05.md"), []byte("week 5"), 0o644)

	contents, names, err := readWeeklyFilesForMonth(weeklyDir, 2026, 3) // March
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 2 {
		t.Errorf("expected 2 weekly files for March (W09 and W10), got %d: %v", len(contents), names)
	}
}

func TestLoadState_FallbackToHighWater(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	os.MkdirAll(sageoxDir, 0o755)

	tcPath := t.TempDir()
	// create existing daily files
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	os.MkdirAll(dailyDir, 0o755)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-09.md"), []byte("day 9"), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10.md"), []byte("day 10"), 0o644)

	// create weekly file
	weeklyDir := filepath.Join(tcPath, "memory", "weekly")
	os.MkdirAll(weeklyDir, 0o755)
	os.WriteFile(filepath.Join(weeklyDir, "2026-W10.md"), []byte("week 10"), 0o644)

	// create monthly file
	monthlyDir := filepath.Join(tcPath, "memory", "monthly")
	os.MkdirAll(monthlyDir, 0o755)
	os.WriteFile(filepath.Join(monthlyDir, "2026-02.md"), []byte("feb"), 0o644)

	// no state file — should infer from existing files
	state := loadDistillStateV2(tmp, tcPath)

	daily := state.lastDailyTime()
	if daily.IsZero() {
		t.Error("expected non-zero lastDailyTime from high-water inference")
	}
	wantDaily := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !daily.Equal(wantDaily) {
		t.Errorf("lastDailyTime = %v, want %v", daily, wantDaily)
	}

	weekly := state.lastWeeklyTime()
	if weekly.IsZero() {
		t.Error("expected non-zero lastWeeklyTime from high-water inference")
	}

	monthly := state.lastMonthlyTime()
	if monthly.IsZero() {
		t.Error("expected non-zero lastMonthlyTime from high-water inference")
	}
}

func TestLoadState_MigratesV1(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	os.MkdirAll(sageoxDir, 0o755)

	v1State := &distillState{
		SchemaVersion:    "1",
		LastDistilled:    "2026-03-10T12:00:00Z",
		ObservationCount: 10,
		TeamID:           "team-abc",
	}
	saveDistillState(tmp, v1State)

	// v1 migration should NOT also infer from files (v1 data takes precedence)
	state := loadDistillStateV2(tmp, tmp)
	if state.TeamID != "team-abc" {
		t.Errorf("expected team-abc, got %s", state.TeamID)
	}
	daily := state.lastDailyTime()
	if daily.IsZero() {
		t.Error("v1 LastDistilled should be used as daily fallback")
	}
}

func TestParseFactDate(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		filename string
		want     string
	}{
		{
			name:     "footer date",
			content:  "Facts\n\n---\n*Extracted from discussion: test (created 2026-03-10)*\n",
			filename: "other-name.md",
			want:     "2026-03-10",
		},
		{
			name:     "filename fallback",
			content:  "Facts without footer",
			filename: "2026-03-11-1423-ryan.md",
			want:     "2026-03-11",
		},
		{
			name:     "no date",
			content:  "No date anywhere",
			filename: "random-name.md",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFactDate(tt.content, tt.filename)
			if got != tt.want {
				t.Errorf("parseFactDate() = %q, want %q", got, tt.want)
			}
		})
	}
}
