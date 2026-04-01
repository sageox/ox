package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDistillStateV2_LastWeeklyTime_InvalidFormat(t *testing.T) {
	t.Parallel()

	state := &distillStateV2{LastWeekly: "invalid"}
	got := state.lastWeeklyTime()
	if !got.IsZero() {
		t.Errorf("lastWeeklyTime() with invalid format = %v, want zero", got)
	}
}

func TestDistillStateV2_LastMonthlyTime_InvalidFormat(t *testing.T) {
	t.Parallel()

	state := &distillStateV2{LastMonthly: "invalid"}
	got := state.lastMonthlyTime()
	if !got.IsZero() {
		t.Errorf("lastMonthlyTime() with invalid format = %v, want zero", got)
	}
}

func TestDetermineLayers_DailyOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	state := &distillStateV2{
		// weekly was done recently so no weeks should be enumerated
		LastWeekly: now.Add(-24 * time.Hour).Format(time.RFC3339),
		// monthly was done recently
		LastMonthly: now.Format(time.RFC3339),
	}
	plan := determineLayers(state, "daily", now, nil)
	if !plan.Daily {
		t.Error("expected Daily=true when explicit='daily'")
	}
	if len(plan.Weeks) != 0 {
		t.Errorf("expected no weeks when explicit='daily', got %d", len(plan.Weeks))
	}
	if len(plan.Months) != 0 {
		t.Errorf("expected no months when explicit='daily', got %d", len(plan.Months))
	}
}

func TestDetermineLayers_WeeklyOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	state := &distillStateV2{
		LastWeekly: now.Add(-14 * 24 * time.Hour).Format(time.RFC3339),
	}
	plan := determineLayers(state, "weekly", now, nil)
	if plan.Daily {
		t.Error("expected Daily=false when explicit='weekly'")
	}
	if len(plan.Weeks) == 0 {
		t.Error("expected weeks to be enumerated when weekly is stale")
	}
	if len(plan.Months) != 0 {
		t.Errorf("expected no months when explicit='weekly', got %d", len(plan.Months))
	}
}

func TestDetermineLayers_NoWeeksWhenRecent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	state := &distillStateV2{
		// weekly done 2 days ago, less than 7 day threshold
		LastWeekly: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
	}
	plan := determineLayers(state, "", now, nil)
	if len(plan.Weeks) != 0 {
		t.Errorf("expected no weeks when last weekly is recent, got %d", len(plan.Weeks))
	}
}

func TestDistillPlanIsEmpty_AllCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		plan  distillPlan
		empty bool
	}{
		{"zero value", distillPlan{}, true},
		{"daily only", distillPlan{Daily: true}, false},
		{"weeks only", distillPlan{Weeks: []isoWeek{{2026, 1}}}, false},
		{"months only", distillPlan{Months: []string{"2026-01"}}, false},
		{"all set", distillPlan{Daily: true, Weeks: []isoWeek{{2026, 1}}, Months: []string{"2026-01"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.plan.isEmpty() != tt.empty {
				t.Errorf("isEmpty() = %v, want %v", tt.plan.isEmpty(), tt.empty)
			}
		})
	}
}

func TestInferDailyHighWater_NoMDFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dailyDir := filepath.Join(dir, "memory", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// write a non-.md file
	os.WriteFile(filepath.Join(dailyDir, "2026-03-10.txt"), []byte("text"), 0o644)

	got := inferDailyHighWater(dir)
	if !got.IsZero() {
		t.Errorf("inferDailyHighWater with no .md files = %v, want zero", got)
	}
}

func TestInferDailyHighWater_WithSubdirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dailyDir := filepath.Join(dir, "memory", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// create a subdirectory that looks like a date
	os.MkdirAll(filepath.Join(dailyDir, "2026-03-10"), 0o755)
	// and a real file
	os.WriteFile(filepath.Join(dailyDir, "2026-03-05.md"), []byte("content"), 0o644)

	got := inferDailyHighWater(dir)
	// should pick the file date, not the dir name
	want := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("inferDailyHighWater = %v, want %v", got, want)
	}
}

func TestInferWeeklyHighWater_NoFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	weeklyDir := filepath.Join(dir, "memory", "weekly")
	if err := os.MkdirAll(weeklyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := inferWeeklyHighWater(dir)
	if !got.IsZero() {
		t.Errorf("inferWeeklyHighWater with empty dir = %v, want zero", got)
	}
}

func TestInferWeeklyHighWater_PicksLatest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	weeklyDir := filepath.Join(dir, "memory", "weekly")
	if err := os.MkdirAll(weeklyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(weeklyDir, "2026-W05.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(weeklyDir, "2026-W10.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(weeklyDir, "2026-W08.md"), []byte("content"), 0o644)

	got := inferWeeklyHighWater(dir)
	if got.IsZero() {
		t.Fatal("inferWeeklyHighWater returned zero time")
	}
	// W10 is the latest; end of ISO week 10 should be a Sunday
	if got.Weekday() != time.Sunday {
		t.Errorf("end of week should be Sunday, got %v", got.Weekday())
	}
}

func TestInferMonthlyHighWater_NonMDSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	monthlyDir := filepath.Join(dir, "memory", "monthly")
	if err := os.MkdirAll(monthlyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// not a .md file
	os.WriteFile(filepath.Join(monthlyDir, "2026-02.txt"), []byte("content"), 0o644)
	// a .md file with wrong naming
	os.WriteFile(filepath.Join(monthlyDir, "notes.md"), []byte("content"), 0o644)

	got := inferMonthlyHighWater(dir)
	if !got.IsZero() {
		t.Errorf("inferMonthlyHighWater with no valid files = %v, want zero", got)
	}
}

func TestInferMonthlyHighWater_PicksLatest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	monthlyDir := filepath.Join(dir, "memory", "monthly")
	if err := os.MkdirAll(monthlyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(monthlyDir, "2026-01.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(monthlyDir, "2026-03.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(monthlyDir, "2026-02.md"), []byte("content"), 0o644)

	got := inferMonthlyHighWater(dir)
	if got.IsZero() {
		t.Fatal("inferMonthlyHighWater returned zero time")
	}
	// should be end of March 2026
	want := endOfMonth(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if !got.Equal(want) {
		t.Errorf("inferMonthlyHighWater = %v, want %v", got, want)
	}
}

func TestReadDailyFilesForDateRange_SortingWithinSameDay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// create files for same day with different UUID suffixes
	os.WriteFile(filepath.Join(dir, "2026-03-10-zzz.md"), []byte("second"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-03-10-aaa.md"), []byte("first"), 0o644)

	contents, names, err := readDailyFilesForDateRange(dir, "2026-03-10", "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 files, got %d", len(names))
	}
	// within same day, should be sorted alphabetically by full filename
	if names[0] != "2026-03-10-aaa.md" {
		t.Errorf("first file = %q, want 2026-03-10-aaa.md", names[0])
	}
	if contents[0] != "first" {
		t.Errorf("first content = %q, want 'first'", contents[0])
	}
}

func TestReadDailyFilesForDateRange_ExcludesOutOfRange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "2026-03-01.md"), []byte("before"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-03-05.md"), []byte("in range"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-03-10.md"), []byte("after"), 0o644)

	contents, names, err := readDailyFilesForDateRange(dir, "2026-03-03", "2026-03-07")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(names), names)
	}
	if names[0] != "2026-03-05.md" {
		t.Errorf("expected 2026-03-05.md, got %q", names[0])
	}
	if contents[0] != "in range" {
		t.Errorf("expected 'in range', got %q", contents[0])
	}
}

func TestReadDailyFilesForDateRange_SkipsDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "2026-03-05.md"), 0o755) // dir, not file
	os.WriteFile(filepath.Join(dir, "2026-03-06.md"), []byte("real file"), 0o644)

	contents, names, err := readDailyFilesForDateRange(dir, "2026-03-01", "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 file, got %d", len(names))
	}
	if names[0] != "2026-03-06.md" {
		t.Errorf("expected 2026-03-06.md, got %q", names[0])
	}
	_ = contents
}

func TestFormatDailyMemory_AllCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		date      string
		content   string
		obsCount  int
		factCount int
		wantPart  string
	}{
		{
			name:     "observations only",
			date:     "2026-03-15",
			content:  "some observations",
			obsCount: 5, factCount: 0,
			wantPart: "5 observations",
		},
		{
			name:     "facts only",
			date:     "2026-03-15",
			content:  "some facts",
			obsCount: 0, factCount: 3,
			wantPart: "3 facts",
		},
		{
			name:     "both observations and facts",
			date:     "2026-03-15",
			content:  "mixed",
			obsCount: 5, factCount: 3,
			wantPart: "5 observations and 3 facts",
		},
		{
			name:     "zero both uses observations label",
			date:     "2026-03-15",
			content:  "empty",
			obsCount: 0, factCount: 0,
			wantPart: "0 observations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatDailyMemory(tt.date, tt.content, tt.obsCount, tt.factCount, nil)
			if len(got) == 0 {
				t.Fatal("formatDailyMemory returned empty string")
			}
			// should contain the date in header
			if !strings.Contains(got, tt.date) {
				t.Errorf("output missing date %q", tt.date)
			}
			// should contain the source description
			if !strings.Contains(got, tt.wantPart) {
				t.Errorf("output missing %q in: %s", tt.wantPart, got)
			}
		})
	}
}

func TestEnumerateWeeks_ZeroLastTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	weeks := enumerateWeeks(time.Time{}, now, time.UTC)

	// with zero lastTime, should look back 91 days max
	if len(weeks) == 0 {
		t.Error("expected some weeks to be enumerated with zero lastTime")
	}
	// all weeks should have their Sunday before now
	for _, w := range weeks {
		_, end := isoWeekRange(w.Year, w.Week)
		if end.After(now) {
			t.Errorf("week %d-W%02d end %v is after now %v", w.Year, w.Week, end, now)
		}
	}
}

func TestEnumerateMonths_ZeroLastTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	months := enumerateMonths(time.Time{}, now, nil)

	if len(months) == 0 {
		t.Error("expected some months with zero lastTime")
	}
	// should not include current month (March 2026) since it's not complete
	for _, m := range months {
		if m == "2026-03" {
			t.Error("should not include incomplete current month")
		}
	}
}

func TestEnumerateMonths_DoesNotIncludeIncompleteMonth(t *testing.T) {
	t.Parallel()

	// mid-month: the current month is not complete
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	lastTime := time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC)
	months := enumerateMonths(lastTime, now, nil)

	// should include May but not June
	for _, m := range months {
		if m == "2026-06" {
			t.Error("should not include incomplete June")
		}
	}

	found := false
	for _, m := range months {
		if m == "2026-05" {
			found = true
		}
	}
	if !found {
		t.Error("should include completed May")
	}
}

