package query

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseDayFromRFC3339 ---

func TestParseDayFromRFC3339(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ts       *string
		fallback string
		want     string
	}{
		{
			name:     "nil pointer returns fallback",
			ts:       nil,
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
		{
			name:     "valid RFC3339 extracts date",
			ts:       ptr("2026-03-15T10:30:00Z"),
			fallback: "2026-01-01",
			want:     "2026-03-15",
		},
		{
			name:     "valid RFC3339 with offset normalizes to UTC",
			ts:       ptr("2026-03-15T23:00:00+08:00"),
			fallback: "2026-01-01",
			// 23:00+08:00 = 15:00 UTC on the same day
			want: "2026-03-15",
		},
		{
			name:     "valid RFC3339 midnight UTC boundary",
			ts:       ptr("2026-03-15T00:00:00Z"),
			fallback: "2026-01-01",
			want:     "2026-03-15",
		},
		{
			name:     "invalid format returns fallback",
			ts:       ptr("not-a-date"),
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
		{
			name:     "empty string returns fallback",
			ts:       ptr(""),
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
		{
			name:     "date-only (no time) returns fallback — not RFC3339",
			ts:       ptr("2026-03-15"),
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseDayFromRFC3339(tt.ts, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- parseDayFromTimestamp ---

func TestParseDayFromTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ts       string
		fallback string
		want     string
	}{
		{
			name:     "valid RFC3339 extracts date",
			ts:       "2026-03-20T08:00:00Z",
			fallback: "2026-01-01",
			want:     "2026-03-20",
		},
		{
			name:     "invalid timestamp returns fallback",
			ts:       "not-a-timestamp",
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
		{
			name:     "empty string returns fallback",
			ts:       "",
			fallback: "2026-01-01",
			want:     "2026-01-01",
		},
		{
			name:     "timestamp with timezone offset",
			ts:       "2026-03-20T01:00:00+05:30",
			fallback: "2026-01-01",
			// 01:00+05:30 = 19:30 UTC on 2026-03-19
			want: "2026-03-19",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseDayFromTimestamp(tt.ts, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- ByDay ---

func TestByDay_PRClusterBucketing(t *testing.T) {
	t.Parallel()

	mergedAt := "2026-03-10T15:00:00Z"
	updatedAt := "2026-03-11T15:00:00Z"

	result := &ActivityResult{
		PRClusters: []PRCluster{
			// uses MergedAt when set
			{Number: 1, Title: "merged PR", MergedAt: &mergedAt, UpdatedAt: &updatedAt},
			// falls back to UpdatedAt when MergedAt is nil
			{Number: 2, Title: "open PR", MergedAt: nil, UpdatedAt: &updatedAt},
		},
	}

	byDay := result.ByDay()

	// PR 1 should be in 2026-03-10 (from MergedAt)
	day10, ok := byDay["2026-03-10"]
	require.True(t, ok, "expected bucket for 2026-03-10")
	require.Len(t, day10.PRClusters, 1)
	assert.Equal(t, 1, day10.PRClusters[0].Number)

	// PR 2 should be in 2026-03-11 (from UpdatedAt)
	day11, ok := byDay["2026-03-11"]
	require.True(t, ok, "expected bucket for 2026-03-11")
	require.Len(t, day11.PRClusters, 1)
	assert.Equal(t, 2, day11.PRClusters[0].Number)
}

func TestByDay_StandaloneIssueBucketing(t *testing.T) {
	t.Parallel()

	updatedAt := "2026-03-18T09:00:00Z"

	result := &ActivityResult{
		StandaloneIssues: []StandaloneIssue{
			{Number: 5, UpdatedAt: &updatedAt},
			{Number: 6, UpdatedAt: nil}, // nil → today's bucket
		},
	}

	byDay := result.ByDay()
	today := time.Now().UTC().Format("2006-01-02")

	day18, ok := byDay["2026-03-18"]
	require.True(t, ok)
	assert.Equal(t, 5, day18.StandaloneIssues[0].Number)

	dayToday := byDay[today]
	require.NotNil(t, dayToday)
	assert.Equal(t, 6, dayToday.StandaloneIssues[0].Number)
}

func TestByDay_StandaloneCommitBucketing(t *testing.T) {
	t.Parallel()

	result := &ActivityResult{
		StandaloneCommits: []StandaloneCommit{
			{SHA: "abc", Timestamp: "2026-03-05T12:00:00Z"},
			{SHA: "def", Timestamp: "bad-timestamp"}, // fallback to today
		},
	}

	byDay := result.ByDay()
	today := time.Now().UTC().Format("2006-01-02")

	day5 := byDay["2026-03-05"]
	require.NotNil(t, day5)
	assert.Equal(t, "abc", day5.StandaloneCommits[0].SHA)

	dayToday := byDay[today]
	require.NotNil(t, dayToday)
	assert.Equal(t, "def", dayToday.StandaloneCommits[0].SHA)
}

func TestByDay_MetadataCountsPerBucket(t *testing.T) {
	t.Parallel()

	mergedAt := "2026-03-01T10:00:00Z"
	updatedAt := "2026-03-01T11:00:00Z"

	result := &ActivityResult{
		PRClusters: []PRCluster{
			{Number: 1, MergedAt: &mergedAt},
			{Number: 2, MergedAt: &mergedAt},
		},
		StandaloneIssues: []StandaloneIssue{
			{Number: 10, UpdatedAt: &updatedAt},
		},
		StandaloneCommits: []StandaloneCommit{
			{SHA: "aaa", Timestamp: "2026-03-01T09:00:00Z"},
			{SHA: "bbb", Timestamp: "2026-03-01T08:00:00Z"},
			{SHA: "ccc", Timestamp: "2026-03-01T07:00:00Z"},
		},
	}

	byDay := result.ByDay()
	bucket := byDay["2026-03-01"]
	require.NotNil(t, bucket)

	assert.Equal(t, 2, bucket.Metadata.PRCount)
	assert.Equal(t, 1, bucket.Metadata.IssueCount)
	assert.Equal(t, 3, bucket.Metadata.CommitCount)
}

func TestByDay_EmptyResult(t *testing.T) {
	t.Parallel()

	result := &ActivityResult{}
	byDay := result.ByDay()
	assert.Empty(t, byDay)
}

func TestByDay_PRUsesTodayForNilTimestamps(t *testing.T) {
	t.Parallel()

	// PR with both MergedAt and UpdatedAt nil falls into today's bucket
	result := &ActivityResult{
		PRClusters: []PRCluster{
			{Number: 99, MergedAt: nil, UpdatedAt: nil},
		},
	}

	today := time.Now().UTC().Format("2006-01-02")
	byDay := result.ByDay()

	dayToday, ok := byDay[today]
	require.True(t, ok, "nil timestamps should fall into today's bucket")
	require.Len(t, dayToday.PRClusters, 1)
	assert.Equal(t, 99, dayToday.PRClusters[0].Number)
}

// --- SortedDays ---

func TestSortedDays(t *testing.T) {
	t.Parallel()

	byDay := map[string]*ActivityResult{
		"2026-03-20": {},
		"2026-03-05": {},
		"2026-03-12": {},
		"2026-01-01": {},
	}

	days := SortedDays(byDay)
	assert.Equal(t, []string{"2026-01-01", "2026-03-05", "2026-03-12", "2026-03-20"}, days)
}

func TestSortedDays_Empty(t *testing.T) {
	t.Parallel()

	days := SortedDays(map[string]*ActivityResult{})
	assert.Empty(t, days)
}

func TestSortedDays_SingleEntry(t *testing.T) {
	t.Parallel()

	byDay := map[string]*ActivityResult{"2026-03-01": {}}
	days := SortedDays(byDay)
	assert.Equal(t, []string{"2026-03-01"}, days)
}

// ptr is a helper to create a *string from a string literal.
func ptr(s string) *string {
	return &s
}
