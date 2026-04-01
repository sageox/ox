package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEndOfDay(t *testing.T) {
	tests := []struct {
		name  string
		input time.Time
		want  time.Time
	}{
		{
			name:  "normal date",
			input: time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC),
			want:  time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "already end of day",
			input: time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
			want:  time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "start of day",
			input: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "leap day",
			input: time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC),
			want:  time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endOfDay(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEndOfMonth(t *testing.T) {
	tests := []struct {
		name  string
		input time.Time
		want  time.Time
	}{
		{
			name:  "january 31 days",
			input: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "february non-leap",
			input: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 2, 28, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "february leap year",
			input: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "april 30 days",
			input: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC),
		},
		{
			name:  "december end of year",
			input: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endOfMonth(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDistillPlan_IsEmpty(t *testing.T) {
	tests := []struct {
		name string
		plan distillPlan
		want bool
	}{
		{name: "zero value", plan: distillPlan{}, want: true},
		{name: "daily only", plan: distillPlan{Daily: true}, want: false},
		{name: "weeks only", plan: distillPlan{Weeks: []isoWeek{{2026, 10}}}, want: false},
		{name: "months only", plan: distillPlan{Months: []string{"2026-03"}}, want: false},
		{name: "all set", plan: distillPlan{Daily: true, Weeks: []isoWeek{{2026, 10}}, Months: []string{"2026-03"}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.plan.isEmpty())
		})
	}
}

func TestEnumerateWeeks(t *testing.T) {
	tests := []struct {
		name     string
		lastTime time.Time
		now      time.Time
		wantMin  int // minimum expected weeks
		wantMax  int // maximum expected weeks
	}{
		{
			name:     "zero last time gets 13 week lookback",
			lastTime: time.Time{},
			now:      time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
			wantMin:  12, // ~13 weeks lookback, current week excluded if not complete
			wantMax:  14,
		},
		{
			name:     "one week gap",
			lastTime: time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
			wantMin:  1,
			wantMax:  3,
		},
		{
			name:     "same week returns empty",
			lastTime: time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC),  // Monday
			now:      time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC), // Tuesday same week
			wantMin:  0,
			wantMax:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weeks := enumerateWeeks(tt.lastTime, tt.now, time.UTC)
			assert.GreaterOrEqual(t, len(weeks), tt.wantMin, "got %d weeks: %v", len(weeks), weeks)
			assert.LessOrEqual(t, len(weeks), tt.wantMax, "got %d weeks: %v", len(weeks), weeks)

			// verify all weeks are completed (Sunday <= now)
			for _, w := range weeks {
				_, end := isoWeekRange(w.Year, w.Week)
				assert.False(t, end.After(tt.now), "week %d-%02d ends after now", w.Year, w.Week)
			}
		})
	}
}

func TestEnumerateMonths(t *testing.T) {
	tests := []struct {
		name     string
		lastTime time.Time
		now      time.Time
		want     []string
		wantMin  int
	}{
		{
			name:     "two completed months",
			lastTime: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			want:     []string{"2026-01", "2026-02"},
		},
		{
			name:     "current month excluded",
			lastTime: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
			now:      time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			want:     nil, // march not yet complete
		},
		{
			name:     "zero last time gets 12 month lookback",
			lastTime: time.Time{},
			now:      time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			wantMin:  11, // ~12 months, current month excluded
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			months := enumerateMonths(tt.lastTime, tt.now, nil)
			if tt.want != nil {
				assert.Equal(t, tt.want, months)
			}
			if tt.wantMin > 0 {
				assert.GreaterOrEqual(t, len(months), tt.wantMin, "expected at least %d months", tt.wantMin)
			}
			// verify each returned month is completed
			for _, m := range months {
				parsed, err := time.Parse("2006-01", m)
				require.NoError(t, err)
				monthEnd := endOfMonth(parsed)
				assert.False(t, monthEnd.After(tt.now), "month %s ends after now", m)
			}
		})
	}
}

func TestFormatDailyMemory_SourceVariants(t *testing.T) {
	tests := []struct {
		name      string
		obsCount  int
		factCount int
		wantIn    string
	}{
		{
			name:      "observations only",
			obsCount:  5,
			factCount: 0,
			wantIn:    "5 observations",
		},
		{
			name:      "facts only",
			obsCount:  0,
			factCount: 3,
			wantIn:    "3 facts",
		},
		{
			name:      "both observations and facts",
			obsCount:  7,
			factCount: 2,
			wantIn:    "7 observations and 2 facts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDailyMemory("2026-03-25", "Distilled content here.", tt.obsCount, tt.factCount, nil)
			assert.Contains(t, result, "# Daily Memory")
			assert.Contains(t, result, "2026-03-25")
			assert.Contains(t, result, "Distilled content here.")
			assert.Contains(t, result, tt.wantIn)
		})
	}
}

func TestContentHash_Properties(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		h1 := contentHash("input1", "input2")
		h2 := contentHash("input1", "input2")
		assert.Equal(t, h1, h2)
	})

	t.Run("different inputs differ", func(t *testing.T) {
		h1 := contentHash("a")
		h2 := contentHash("b")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("order matters", func(t *testing.T) {
		h1 := contentHash("a", "b")
		h2 := contentHash("b", "a")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("separator prevents collision", func(t *testing.T) {
		// "a" + "bc" should differ from "ab" + "c"
		h1 := contentHash("a", "bc")
		h2 := contentHash("ab", "c")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("fixed 16 char length", func(t *testing.T) {
		assert.Len(t, contentHash("x"), 16)
		assert.Len(t, contentHash("x", "y", "z"), 16)
		assert.Len(t, contentHash(""), 16)
	})

	t.Run("empty input", func(t *testing.T) {
		h := contentHash()
		assert.Len(t, h, 16)
	})
}

func TestGroupObservationsByDay_EdgeCases(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		groups := groupObservationsByDay(nil, nil)
		assert.Empty(t, groups)
	})

	t.Run("zero time observations skipped", func(t *testing.T) {
		obs := []distillObservation{
			{Content: "has time", RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)},
			{Content: "no time", RecordedAt: time.Time{}},
		}
		groups := groupObservationsByDay(obs, nil)
		assert.Len(t, groups, 1)
		assert.Len(t, groups["2026-03-10"], 1)
	})

	t.Run("preserves order within day", func(t *testing.T) {
		obs := []distillObservation{
			{Content: "first", RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)},
			{Content: "second", RecordedAt: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)},
			{Content: "third", RecordedAt: time.Date(2026, 3, 10, 18, 0, 0, 0, time.UTC)},
		}
		groups := groupObservationsByDay(obs, nil)
		require.Len(t, groups["2026-03-10"], 3)
		assert.Equal(t, "first", groups["2026-03-10"][0].Content)
		assert.Equal(t, "second", groups["2026-03-10"][1].Content)
		assert.Equal(t, "third", groups["2026-03-10"][2].Content)
	})
}

func TestScanPendingDiscussions_Pure(t *testing.T) {
	t.Run("missing directory returns nil", func(t *testing.T) {
		result, err := scanPendingDiscussions("/nonexistent/path")
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("empty directory returns nil", func(t *testing.T) {
		tmp := t.TempDir()
		discussionsDir := filepath.Join(tmp, "discussions")
		require.NoError(t, os.MkdirAll(discussionsDir, 0755))

		result, err := scanPendingDiscussions(tmp)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("skips non-directory entries", func(t *testing.T) {
		tmp := t.TempDir()
		discussionsDir := filepath.Join(tmp, "discussions")
		require.NoError(t, os.MkdirAll(discussionsDir, 0755))
		// create a file (not dir) in discussions
		require.NoError(t, os.WriteFile(filepath.Join(discussionsDir, "notes.txt"), []byte("hi"), 0644))

		result, err := scanPendingDiscussions(tmp)
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestInferDailyHighWater_SkipsNonMD(t *testing.T) {
	tmp := t.TempDir()
	dailyDir := filepath.Join(tmp, "memory", "daily")
	require.NoError(t, os.MkdirAll(dailyDir, 0755))

	// write a .txt file and a subdirectory
	require.NoError(t, os.WriteFile(filepath.Join(dailyDir, "2026-03-15.txt"), []byte("not md"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dailyDir, "2026-03-20.md"), 0755)) // dir, not file

	got := inferDailyHighWater(tmp)
	assert.True(t, got.IsZero(), "should ignore non-.md files and directories")
}

func TestReadDailyFilesForDateRange_NonexistentDir(t *testing.T) {
	contents, names, err := readDailyFilesForDateRange("/nonexistent", "2026-03-01", "2026-03-31")
	require.NoError(t, err)
	assert.Nil(t, contents)
	assert.Nil(t, names)
}

func TestSaveAndLoadDistillStateV2(t *testing.T) {
	tmp := t.TempDir()
	requireSageoxDir(t, tmp)

	original := &distillStateV2{
		SchemaVersion: "2",
		TeamID:        "team-round-trip",
		LastWeekly:    "2026-03-15T10:00:00Z",
		LastMonthly:   "2026-02-28T23:59:59Z",
	}

	require.NoError(t, saveDistillStateV2(tmp, original))

	loaded := loadDistillStateV2(tmp, tmp)
	assert.Equal(t, "2", loaded.SchemaVersion)
	assert.Equal(t, "team-round-trip", loaded.TeamID)
	assert.Equal(t, "2026-03-15T10:00:00Z", loaded.LastWeekly)
	assert.Equal(t, "2026-02-28T23:59:59Z", loaded.LastMonthly)
}

func TestDetermineLayers_ExplicitMonthly(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	plan := determineLayers(&distillStateV2{}, "monthly", now, nil)
	assert.False(t, plan.Daily, "daily should be false for explicit monthly")
	assert.Empty(t, plan.Weeks, "weeks should be empty for explicit monthly")
	assert.NotEmpty(t, plan.Months, "months should be non-empty for explicit monthly")
}

func TestISOWeekRange_CrossYearBoundary(t *testing.T) {
	// ISO week 1 of 2026 starts on Monday Dec 29, 2025
	start, end := isoWeekRange(2026, 1)
	assert.Equal(t, time.Monday, start.Weekday())
	assert.Equal(t, time.Sunday, end.Weekday())
	assert.Equal(t, 2025, start.Year(), "ISO week 1 of 2026 starts in Dec 2025")
	assert.Equal(t, 2026, end.Year(), "ISO week 1 of 2026 ends in Jan 2026")
}

func TestISOWeekRange_LastWeekOfYear(t *testing.T) {
	// 2025 has ISO week 52 as the last week
	start, end := isoWeekRange(2025, 52)
	assert.Equal(t, time.Monday, start.Weekday())
	assert.Equal(t, time.Sunday, end.Weekday())
	// week 52 of 2025: Dec 22-28
	assert.Equal(t, 12, int(start.Month()))
	assert.Equal(t, 22, start.Day())
}
