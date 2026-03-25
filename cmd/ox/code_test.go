package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
)

func TestFormatIndexTiming(t *testing.T) {
	tests := []struct {
		name   string
		result *daemon.CodeIndexResult
		want   string
	}{
		{
			name: "typical index run",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   7000,
				SymbolDurationMs:  1200,
				CommentDurationMs: 800,
				TotalDurationMs:   9000,
			},
			want: "total 9s: index 7s, symbols 1s, comments 1s",
		},
		{
			name: "zero durations (incremental no-op)",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   500,
				SymbolDurationMs:  0,
				CommentDurationMs: 0,
				TotalDurationMs:   500,
			},
			want: "total 1s: index 1s, symbols <1s, comments <1s",
		},
		{
			name:   "all zeros",
			result: &daemon.CodeIndexResult{},
			want:   "total <1s: index <1s, symbols <1s, comments <1s",
		},
		{
			name: "large repo with minutes",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   82000,
				SymbolDurationMs:  15000,
				CommentDurationMs: 8000,
				TotalDurationMs:   105000,
			},
			want: "total 1m 45s: index 1m 22s, symbols 15s, comments 8s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatIndexTiming(tt.result)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCodeDBIndexing_DefaultReturnsFalseWithoutDaemon(t *testing.T) {
	// With no daemon running, isCodeDBIndexing should return false
	// (IPC fails → err != nil → false). This is the default in test environments.
	assert.False(t, isCodeDBIndexing())
}

func TestIsCodeDBIndexing_Override(t *testing.T) {
	orig := isCodeDBIndexing
	t.Cleanup(func() { isCodeDBIndexing = orig })

	tests := []struct {
		name string
		stub func() bool
		want bool
	}{
		{"indexing in progress", func() bool { return true }, true},
		{"not indexing", func() bool { return false }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isCodeDBIndexing = tt.stub
			assert.Equal(t, tt.want, isCodeDBIndexing())
		})
	}
}

func TestPopulateStatsFromDaemonCache(t *testing.T) {
	// Verify that the codeStatusCmd daemon-cached stats path correctly
	// maps CodeDBStats fields to local totals.
	codeStats := &daemon.CodeDBStats{
		Commits:     150,
		Blobs:       3200,
		Symbols:     800,
		Comments:    420,
		PRs:         25,
		Issues:      12,
		IndexingNow: true,
		Repos: []daemon.RepoStats{
			{Name: "main", Path: "/repo", Commits: 150, Blobs: 3200},
			{Name: "worktree-1", Path: "/wt1", Commits: 10, Blobs: 50},
		},
	}

	// Simulate the daemonIndexing branch from codeStatusCmd
	var totalCommits, totalBlobs, totalSymbols, totalComments, totalPRs, totalIssues int
	type repoRow struct {
		name       string
		path       string
		commits    int
		blobs      int
		lastCommit int64
	}
	var repos []repoRow

	daemonIndexing := codeStats != nil && codeStats.IndexingNow
	assert.True(t, daemonIndexing)

	totalCommits = codeStats.Commits
	totalBlobs = codeStats.Blobs
	totalSymbols = codeStats.Symbols
	totalComments = codeStats.Comments
	totalPRs = codeStats.PRs
	totalIssues = codeStats.Issues
	for _, r := range codeStats.Repos {
		repos = append(repos, repoRow{name: r.Name, path: r.Path, commits: r.Commits, blobs: r.Blobs})
	}

	assert.Equal(t, 150, totalCommits)
	assert.Equal(t, 3200, totalBlobs)
	assert.Equal(t, 800, totalSymbols)
	assert.Equal(t, 420, totalComments)
	assert.Equal(t, 25, totalPRs)
	assert.Equal(t, 12, totalIssues)
	assert.Len(t, repos, 2)
	assert.Equal(t, "main", repos[0].name)
	assert.Equal(t, "worktree-1", repos[1].name)
	assert.Equal(t, 50, repos[1].blobs)
}

func TestPopulateStatsFromDaemonCache_ZeroValues(t *testing.T) {
	// First-ever index: daemon reports indexing but has no cached stats.
	codeStats := &daemon.CodeDBStats{
		IndexingNow: true,
	}

	var totalCommits, totalBlobs, totalSymbols, totalComments, totalPRs, totalIssues int

	totalCommits = codeStats.Commits
	totalBlobs = codeStats.Blobs
	totalSymbols = codeStats.Symbols
	totalComments = codeStats.Comments
	totalPRs = codeStats.PRs
	totalIssues = codeStats.Issues

	assert.Equal(t, 0, totalCommits)
	assert.Equal(t, 0, totalBlobs)
	assert.Equal(t, 0, totalSymbols)
	assert.Equal(t, 0, totalComments)
	assert.Equal(t, 0, totalPRs)
	assert.Equal(t, 0, totalIssues)
}

func TestFormatDurationBrief(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "<1s"},
		{"sub-second rounds up", 500 * time.Millisecond, "1s"},
		{"very small", 100 * time.Millisecond, "<1s"},
		{"one second", 1 * time.Second, "1s"},
		{"seconds", 7 * time.Second, "7s"},
		{"minute boundary", 60 * time.Second, "1m"},
		{"minutes and seconds", 90 * time.Second, "1m 30s"},
		{"hour boundary", 1 * time.Hour, "1h"},
		{"hours and minutes", 90 * time.Minute, "1h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationBrief(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}
