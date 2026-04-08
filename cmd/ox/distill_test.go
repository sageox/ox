package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetermineLayers(t *testing.T) {
	now := time.Now().UTC()

	t.Run("explicit daily", func(t *testing.T) {
		plan := determineLayers(&distillStateV2{}, "daily", now, nil)
		if !plan.Daily {
			t.Error("expected Daily=true")
		}
		if len(plan.Weeks) != 0 || len(plan.Months) != 0 {
			t.Error("expected no weeks or months for explicit daily")
		}
	})

	t.Run("fresh state triggers all layers", func(t *testing.T) {
		plan := determineLayers(&distillStateV2{}, "", now, nil)
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
		plan := determineLayers(state, "", now, nil)
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
		plan := determineLayers(state, "", now, nil)
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
	plan := determineLayers(state, "", now, nil)
	require.GreaterOrEqual(t, len(plan.Weeks), 2, "expected at least 2 weeks for 3-week gap")
	// verify actual ISO week values: Feb 19 is W08, now Mar 12 is W11
	// determineLayers returns W08, W09, W10 (all completed weeks in the gap)
	weekNums := make([]int, len(plan.Weeks))
	for i, w := range plan.Weeks {
		weekNums[i] = w.Week
	}
	assert.Contains(t, weekNums, 9, "should contain W09")
	assert.Contains(t, weekNums, 10, "should contain W10")
}

func TestDetermineLayers_MultipleMonths(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	// 2+ months ago
	state := &distillStateV2{
		LastWeekly:  now.Add(-24 * time.Hour).Format(time.RFC3339),
		LastMonthly: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	plan := determineLayers(state, "", now, nil)
	require.ElementsMatch(t, []string{"2026-01", "2026-02"}, plan.Months, "expected exactly Jan and Feb 2026")
}

func TestDetermineLayers_ExplicitLayer(t *testing.T) {
	now := time.Now().UTC()
	plan := determineLayers(&distillStateV2{}, "weekly", now, nil)
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

	for _, sub := range []string{"memory/daily", "memory/weekly", "memory/monthly", "memory/guidance"} {
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
	content := formatDailyMemory("2026-03-11", "Some distilled content", 5, 0, nil)
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
	// most recent first — guard against empty slice hiding a real bug
	require.Len(t, names, 2, "expected 2 names after reading 2 files")
	if names[0] != "2026-03-11.md" {
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

	// v1 state is no longer migrated — just verify we get a fresh v2 state
	state := loadDistillStateV2(tmp, tmp)
	if state.SchemaVersion != "2" {
		t.Errorf("expected schema version 2, got %s", state.SchemaVersion)
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
		LastWeekly:    "2026-03-10T10:00:00Z",
		LastMonthly:   "2026-02-28T23:59:59Z",
	}

	if err := saveDistillStateV2(tmp, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := loadDistillStateV2(tmp, tmp)
	if loaded.TeamID != "team-xyz" {
		t.Errorf("expected team-xyz, got %s", loaded.TeamID)
	}
	if loaded.LastWeekly != "2026-03-10T10:00:00Z" {
		t.Errorf("expected LastWeekly 2026-03-10T10:00:00Z, got %s", loaded.LastWeekly)
	}
}

func TestDistillStateV2LastTimes(t *testing.T) {
	t.Run("zero state returns zero times", func(t *testing.T) {
		state := &distillStateV2{}
		if !state.lastWeeklyTime().IsZero() {
			t.Error("expected zero lastWeeklyTime for empty state")
		}
		if !state.lastMonthlyTime().IsZero() {
			t.Error("expected zero lastMonthlyTime for empty state")
		}
	})
}

func TestLoadGuidance(t *testing.T) {
	tmp := t.TempDir()
	guidanceDir := filepath.Join(tmp, "memory", "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("missing file returns empty", func(t *testing.T) {
		got := loadGuidance(tmp, "EXTRACT.md")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("reads EXTRACT.md", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte("extract guidance"), 0o644))
		got := loadGuidance(tmp, "EXTRACT.md")
		if got != "extract guidance" {
			t.Errorf("expected 'extract guidance', got %q", got)
		}
	})

	t.Run("reads DISTILL.md", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "DISTILL.md"), []byte("distill guidance"), 0o644))
		got := loadGuidance(tmp, "DISTILL.md")
		if got != "distill guidance" {
			t.Errorf("expected 'distill guidance', got %q", got)
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte("  padded  \n"), 0o644))
		got := loadGuidance(tmp, "EXTRACT.md")
		if got != "padded" {
			t.Errorf("expected 'padded', got %q", got)
		}
	})
}

func TestSeedGuidanceFiles(t *testing.T) {
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	if err := seedGuidanceFiles(tmp); err != nil {
		t.Fatalf("seedGuidanceFiles: %v", err)
	}

	guidanceDir := filepath.Join(tmp, "memory", "guidance")

	// EXTRACT.md should exist
	data, err := os.ReadFile(filepath.Join(guidanceDir, "EXTRACT.md"))
	if err != nil {
		t.Fatalf("EXTRACT.md not created: %v", err)
	}
	if !strings.Contains(string(data), "Extraction Guidance") {
		t.Error("EXTRACT.md should contain default content")
	}

	// DISTILL.md should exist
	data, err = os.ReadFile(filepath.Join(guidanceDir, "DISTILL.md"))
	if err != nil {
		t.Fatalf("DISTILL.md not created: %v", err)
	}
	if !strings.Contains(string(data), "Distillation Guidance") {
		t.Error("DISTILL.md should contain default content")
	}

	// second call should not overwrite
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte("custom"), 0o644))
	if err := seedGuidanceFiles(tmp); err != nil {
		t.Fatalf("seedGuidanceFiles (second call): %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(guidanceDir, "EXTRACT.md"))
	if string(data) != "custom" {
		t.Error("seedGuidanceFiles should not overwrite existing files")
	}
}

func TestMigrateDistillGuidelines(t *testing.T) {
	t.Run("migrates old file", func(t *testing.T) {
		tmp := t.TempDir()
		initGitRepo(t, tmp)

		oldContent := "# Custom Guidelines\nTrack security."
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "DISTILL.md"), []byte(oldContent), 0o644))

		// commit the old file so git tracks it (mirrors real-world state)
		if err := commitMemoryFile(tmp, "DISTILL.md", "seed DISTILL.md"); err != nil {
			t.Fatalf("commit old DISTILL.md: %v", err)
		}

		migrated, err := migrateDistillGuidelines(tmp)
		if err != nil {
			t.Fatalf("migrateDistillGuidelines: %v", err)
		}
		if !migrated {
			t.Error("expected migration to occur")
		}

		// old file should be gone
		if _, err := os.Stat(filepath.Join(tmp, "DISTILL.md")); err == nil {
			t.Error("old DISTILL.md should be removed")
		}

		// new file should exist with same content
		data, err := os.ReadFile(filepath.Join(tmp, "memory", "guidance", "DISTILL.md"))
		if err != nil {
			t.Fatalf("new DISTILL.md not found: %v", err)
		}
		if string(data) != oldContent {
			t.Errorf("expected %q, got %q", oldContent, string(data))
		}
	})

	t.Run("skips if old file missing", func(t *testing.T) {
		tmp := t.TempDir()
		migrated, err := migrateDistillGuidelines(tmp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if migrated {
			t.Error("should not migrate when no old file")
		}
	})

	t.Run("skips if new file exists", func(t *testing.T) {
		tmp := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "DISTILL.md"), []byte("old"), 0o644))
		guidanceDir := filepath.Join(tmp, "memory", "guidance")
		require.NoError(t, os.MkdirAll(guidanceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "DISTILL.md"), []byte("new"), 0o644))

		migrated, err := migrateDistillGuidelines(tmp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if migrated {
			t.Error("should not migrate when new file already exists")
		}

		// new file should still have original content
		data, _ := os.ReadFile(filepath.Join(guidanceDir, "DISTILL.md"))
		if string(data) != "new" {
			t.Error("new file should not be overwritten")
		}
	})

	t.Run("migrates untracked file via fallback", func(t *testing.T) {
		tmp := t.TempDir()
		initGitRepo(t, tmp)

		// write DISTILL.md but do NOT commit it — it's untracked, so git mv will fail
		oldContent := "untracked custom guidelines"
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "DISTILL.md"), []byte(oldContent), 0o644))

		migrated, err := migrateDistillGuidelines(tmp)
		require.NoError(t, err)
		if !migrated {
			t.Error("expected migration to occur for untracked file")
		}

		// old file should be gone
		if _, err := os.Stat(filepath.Join(tmp, "DISTILL.md")); err == nil {
			t.Error("old DISTILL.md should be removed")
		}

		// new file should exist with same content
		data, err := os.ReadFile(filepath.Join(tmp, "memory", "guidance", "DISTILL.md"))
		require.NoError(t, err)
		if string(data) != oldContent {
			t.Errorf("expected %q, got %q", oldContent, string(data))
		}
	})
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

	groups := groupObservationsByDay(obs, nil)
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

	groups := groupObservationsByDay(obs, nil)
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
			wantStart: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),    // Monday
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

	// 3 UUID7 files for same day — all returned (distill may run multiple times per day)
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

	contents, names, err := readWeeklyFilesForMonth(weeklyDir, 2026, 3, nil) // March
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

	// no state file — should infer weekly/monthly from existing files
	state := loadDistillStateV2(tmp, tcPath)

	weekly := state.lastWeeklyTime()
	if weekly.IsZero() {
		t.Error("expected non-zero lastWeeklyTime from high-water inference")
	}

	monthly := state.lastMonthlyTime()
	if monthly.IsZero() {
		t.Error("expected non-zero lastMonthlyTime from high-water inference")
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
			name:     "jsonl meta header",
			content:  "{\"_meta\":{\"schema_version\":\"2\",\"source_type\":\"discussion\",\"recorded_at\":\"2026-03-10T14:23:00Z\"}}\n{\"headline\":\"test\"}",
			filename: "other-name.jsonl",
			want:     "2026-03-10",
		},
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

func TestResolveDistillRepos_NoEnv(t *testing.T) {
	t.Parallel()

	// With no DISTILL_REPOS env, falls back to resolving from projectRoot.
	// We pass a non-project dir, so it should return nil repos (no error).
	repos, err := resolveDistillRepos(t.TempDir())
	assert.NoError(t, err)
	assert.Nil(t, repos)
}

func TestResolveDistillRepos_InvalidPath(t *testing.T) {
	// NOT parallel: mutates environment
	t.Setenv("DISTILL_REPOS", "/nonexistent/path")

	_, err := resolveDistillRepos(t.TempDir())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a SageOx project")
}

func TestResolveDistillRepos_EmptyEnv(t *testing.T) {
	// NOT parallel: mutates environment
	t.Setenv("DISTILL_REPOS", ":::")

	_, err := resolveDistillRepos(t.TempDir())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid repos")
}
