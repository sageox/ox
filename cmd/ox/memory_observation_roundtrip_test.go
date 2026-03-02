package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initTestGitRepoForRoundtrip(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Dev"},
		{"git", "config", "user.email", "dev@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test User",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test User",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}
	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("repo"), 0644))
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}

func TestObservationRoundtrip_SingleObservation(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	obs := []observation{{Content: "We decided to use PostgreSQL for analytics"}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, dayCounts, err := scanPendingObservations(obsDir, time.Time{})
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Equal(t, "We decided to use PostgreSQL for analytics", results[0].Content)
	assert.False(t, results[0].RecordedAt.IsZero(), "RecordedAt should be set")
	assert.NotEmpty(t, results[0].SourceFile, "SourceFile should be populated")

	// day counts should have exactly one day with one observation
	totalCount := 0
	for _, c := range dayCounts {
		totalCount += c
	}
	assert.Equal(t, 1, totalCount)
}

func TestObservationRoundtrip_MultipleObservations(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	obs := []observation{
		{Content: "first observation"},
		{Content: "second observation"},
		{Content: "third observation"},
	}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, _, err := scanPendingObservations(obsDir, time.Time{})
	require.NoError(t, err)

	require.Len(t, results, 3)

	contents := make([]string, len(results))
	for i, r := range results {
		contents[i] = r.Content
	}
	assert.Contains(t, contents, "first observation")
	assert.Contains(t, contents, "second observation")
	assert.Contains(t, contents, "third observation")
}

func TestObservationRoundtrip_MultiplePuts(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	err := writeObservation(dir, []observation{{Content: "from first put"}})
	require.NoError(t, err)

	err = writeObservation(dir, []observation{{Content: "from second put"}})
	require.NoError(t, err)

	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, _, err := scanPendingObservations(obsDir, time.Time{})
	require.NoError(t, err)

	require.Len(t, results, 2)

	contents := make([]string, len(results))
	for i, r := range results {
		contents[i] = r.Content
	}
	assert.Contains(t, contents, "from first put")
	assert.Contains(t, contents, "from second put")

	// each put should create a separate file, so SourceFile should differ
	assert.NotEqual(t, results[0].SourceFile, results[1].SourceFile,
		"separate puts should produce separate files")
}

func TestObservationRoundtrip_SinceFilter(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	err := writeObservation(dir, []observation{{Content: "should be filtered out"}})
	require.NoError(t, err)

	// use a since time well in the future so all observations are excluded
	future := time.Now().Add(24 * time.Hour)
	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, _, err := scanPendingObservations(obsDir, future)
	require.NoError(t, err)

	assert.Len(t, results, 0, "future since time should filter out all observations")
}

func TestObservationRoundtrip_LargeObservation(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	// ~19KB, just under the 20480 byte max
	largeContent := strings.Repeat("a", 19*1024)
	err := writeObservation(dir, []observation{{Content: largeContent}})
	require.NoError(t, err)

	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, _, err := scanPendingObservations(obsDir, time.Time{})
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Equal(t, largeContent, results[0].Content, "large content should be preserved exactly")
	assert.Len(t, results[0].Content, 19*1024)
}

func TestObservationRoundtrip_ContentPreserved(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepoForRoundtrip(t, dir)

	// content with special characters: embedded newlines (JSON-escaped),
	// unicode, quotes, backslashes, tabs
	specialCases := []observation{
		{Content: "line one\nline two\nline three"},
		{Content: "unicode: \u00e9\u00e0\u00fc \u4e16\u754c \U0001F680"},
		{Content: `quotes: "hello" and 'world'`},
		{Content: "backslash: C:\\Users\\dev\\path"},
		{Content: "tabs:\there\tand\tthere"},
	}

	err := writeObservation(dir, specialCases)
	require.NoError(t, err)

	obsDir := filepath.Join(dir, "memory", observationDirName)
	results, _, err := scanPendingObservations(obsDir, time.Time{})
	require.NoError(t, err)

	require.Len(t, results, len(specialCases))

	// build a set of returned contents for order-independent comparison
	resultContents := make(map[string]bool, len(results))
	for _, r := range results {
		resultContents[r.Content] = true
	}

	for _, sc := range specialCases {
		assert.True(t, resultContents[sc.Content],
			"expected content not found after roundtrip: %q", sc.Content)
	}
}
