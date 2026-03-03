package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// writeObservation: git failure scenarios
// ---------------------------------------------------------------------------

func TestWriteObservation_NonGitDirectory(t *testing.T) {
	dir := t.TempDir() // plain dir, no git init

	obs := []observation{{Content: "orphaned observation"}}
	err := writeObservation(dir, obs)

	require.Error(t, err)
	require.Contains(t, err.Error(), "git add")

	// the JSONL file should still exist on disk even though git add failed
	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	entries, err := os.ReadDir(dateDir)
	require.NoError(t, err, "observation directory should exist")
	require.Len(t, entries, 1, "JSONL file should be written despite git failure")
	require.True(t, strings.HasSuffix(entries[0].Name(), ".jsonl"))

	// verify file content is valid
	data, err := os.ReadFile(filepath.Join(dateDir, entries[0].Name()))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2, "header + 1 observation")

	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Equal(t, "orphaned observation", parsed.Content)
}

func TestWriteObservation_GitAddFailure_CorruptedGit(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// corrupt git state by renaming .git directory
	gitDir := filepath.Join(dir, ".git")
	renamedGit := filepath.Join(dir, ".git-disabled")
	require.NoError(t, os.Rename(gitDir, renamedGit))
	t.Cleanup(func() {
		// restore so t.TempDir cleanup doesn't complain
		_ = os.Rename(renamedGit, gitDir)
	})

	obs := []observation{{Content: "should fail on git add"}}
	err := writeObservation(dir, obs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "git add")
}

// ---------------------------------------------------------------------------
// writeObservation: concurrent writes
// ---------------------------------------------------------------------------

func TestWriteObservation_ConcurrentPuts(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	const goroutines = 5
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			obs := []observation{{Content: strings.Repeat("x", idx+10)}}
			errs[idx] = writeObservation(dir, obs)
		}(i)
	}
	wg.Wait()

	// at least some should succeed; concurrent git commits may cause lock
	// contention, but unique UUIDs mean no file conflicts
	successCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	require.Greater(t, successCount, 0, "at least one concurrent write should succeed")

	// verify JSONL files exist for each success
	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	entries, err := os.ReadDir(dateDir)
	require.NoError(t, err)
	require.Equal(t, goroutines, len(entries), "all goroutines should create unique JSONL files")
}

// ---------------------------------------------------------------------------
// writeObservation: content encoding edge cases
// ---------------------------------------------------------------------------

func TestWriteObservation_UnicodeContent(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	tests := []struct {
		name    string
		content string
	}{
		{"emoji", "We decided to use PostgreSQL \U0001F680\U0001F4CA"},
		{"CJK", "\u6211\u4EEC\u51B3\u5B9A\u4F7F\u7528 PostgreSQL"},
		{"mixed scripts", "API \u30EC\u30B9\u30DD\u30F3\u30B9\u306F JSON \u5F62\u5F0F\u3067\u8FD4\u3059"},
		{"RTL Arabic", "\u0627\u0644\u062A\u0635\u0645\u064A\u0645 \u0627\u0644\u0645\u0639\u0645\u0627\u0631\u064A"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subDir := t.TempDir()
			initTestGitRepo(t, subDir)

			obs := []observation{{Content: tt.content}}
			err := writeObservation(subDir, obs)
			require.NoError(t, err)

			f := findObservationFile(t, subDir)
			data, err := os.ReadFile(f)
			require.NoError(t, err)

			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.Len(t, lines, 2)

			var parsed observation
			require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
			require.Equal(t, tt.content, parsed.Content, "unicode content should round-trip exactly")
		})
	}
}

func TestWriteObservation_ContentWithNewlinesAndSpecialChars(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	content := "line1\nline2\ttab\rcarriage\nbackslash\\quote\"done"
	obs := []observation{{Content: content}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// the observation content has \n but json.Marshal escapes them,
	// so the JSONL file should still be exactly 2 lines (header + 1 obs)
	require.Len(t, lines, 2, "JSON-escaped newlines should not create extra JSONL lines")

	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Equal(t, content, parsed.Content)
}

func TestWriteObservation_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// writeObservation itself does not validate; it writes whatever it gets
	obs := []observation{{Content: ""}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Equal(t, "", parsed.Content)
}

// ---------------------------------------------------------------------------
// writeObservation: commit message truncation edge cases
// ---------------------------------------------------------------------------

func TestWriteObservation_CommitMessageTruncation(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantMsgSuffix  bool // true if commit message should end with "..."
		wantContentLen int  // expected length of content portion in commit message
	}{
		{
			name:           "exactly 50 chars - no truncation",
			content:        strings.Repeat("a", 50),
			wantMsgSuffix:  false,
			wantContentLen: 50,
		},
		{
			name:           "51 chars - gets truncated",
			content:        strings.Repeat("b", 51),
			wantMsgSuffix:  true,
			wantContentLen: 50,
		},
		{
			name:           "49 chars - no truncation",
			content:        strings.Repeat("c", 49),
			wantMsgSuffix:  false,
			wantContentLen: 49,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initTestGitRepo(t, dir)

			obs := []observation{{Content: tt.content}}
			err := writeObservation(dir, obs)
			require.NoError(t, err)

			cmd := exec.Command("git", "log", "--format=%s", "-1")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err)

			commitMsg := strings.TrimSpace(string(out))
			prefix := "observation: "

			if tt.wantMsgSuffix {
				expected := prefix + tt.content[:50] + "..."
				require.Equal(t, expected, commitMsg)
			} else {
				expected := prefix + tt.content
				require.Equal(t, expected, commitMsg)
				require.False(t, strings.HasSuffix(commitMsg, "..."))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseObservations: additional edge cases
// ---------------------------------------------------------------------------

func TestParseObservations_WhitespaceOnlyContent(t *testing.T) {
	input := []byte(`{"content": "   \n  "}`)
	obs, err := parseObservations(input)
	require.NoError(t, err)

	// whitespace-only content is a non-empty string, so the single-object
	// parse path succeeds (Content != "")
	require.Len(t, obs, 1)
	require.Equal(t, "   \n  ", obs[0].Content)
}

func TestParseObservations_NullContent(t *testing.T) {
	input := []byte(`{"content": null}`)
	obs, err := parseObservations(input)
	require.NoError(t, err)

	// json.Unmarshal sets Content="" for null, so the single-object check
	// (Content != "") fails and falls through to JSONL parsing.
	// JSONL re-parses the same line, gets Content="" again, and returns it.
	require.Len(t, obs, 1)
	require.Equal(t, "", obs[0].Content)
}

func TestParseObservations_DuplicateContentKey(t *testing.T) {
	// Go's json.Unmarshal uses the last value when keys are duplicated
	input := []byte(`{"content":"first","content":"second"}`)
	obs, err := parseObservations(input)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	require.Equal(t, "second", obs[0].Content)
}

func TestParseObservations_ExtraFields(t *testing.T) {
	input := []byte(`{"content":"hello","extra":"ignored","nested":{"deep":true}}`)
	obs, err := parseObservations(input)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	require.Equal(t, "hello", obs[0].Content)
}

func TestParseObservations_MixedValidInvalid(t *testing.T) {
	input := []byte("{\"content\":\"valid\"}\n{bad json}")
	_, err := parseObservations(input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "line 2")
}

func TestParseObservations_LargeJSONL(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, `{"content":"observation `+strings.Repeat("x", i)+`"}`)
	}
	input := []byte(strings.Join(lines, "\n"))

	obs, err := parseObservations(input)
	require.NoError(t, err)
	require.Len(t, obs, 100)

	// verify first and last
	assert.Equal(t, "observation ", obs[0].Content)
	assert.Equal(t, "observation "+strings.Repeat("x", 99), obs[99].Content)
}

// ---------------------------------------------------------------------------
// writeObservation: directory creation
// ---------------------------------------------------------------------------

func TestWriteObservation_CreatesDateDirectory(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// the memory/.observations/<date>/ path should not exist yet
	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	_, err := os.Stat(dateDir)
	require.True(t, os.IsNotExist(err), "date dir should not exist before write")

	obs := []observation{{Content: "creates dirs"}}
	require.NoError(t, writeObservation(dir, obs))

	info, err := os.Stat(dateDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestWriteObservation_MultipleWritesSameDay(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// write two observations sequentially on the same day
	obs1 := []observation{{Content: "first write"}}
	require.NoError(t, writeObservation(dir, obs1))

	obs2 := []observation{{Content: "second write"}}
	require.NoError(t, writeObservation(dir, obs2))

	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	entries, err := os.ReadDir(dateDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "each write should produce a unique JSONL file")

	// verify git log has two observation commits
	cmd := exec.Command("git", "log", "--oneline", "--grep=observation:")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	commitLines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, commitLines, 2)
}

// ---------------------------------------------------------------------------
// writeObservation: JSONL file header validation
// ---------------------------------------------------------------------------

func TestWriteObservation_HeaderHasValidTimestamp(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	before := time.Now().UTC()
	obs := []observation{{Content: "timestamp check"}}
	require.NoError(t, writeObservation(dir, obs))
	after := time.Now().UTC()

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 1)

	var header observationHeader
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &header))

	recorded, err := time.Parse(time.RFC3339, header.RecordedAt)
	require.NoError(t, err)

	assert.False(t, recorded.Before(before.Truncate(time.Second)),
		"recorded_at should not be before test start")
	assert.False(t, recorded.After(after.Add(time.Second)),
		"recorded_at should not be after test end")
}

func TestWriteObservation_UUIDv7Filename(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	obs := []observation{{Content: "uuid check"}}
	require.NoError(t, writeObservation(dir, obs))

	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	entries, err := os.ReadDir(dateDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	name := entries[0].Name()
	require.True(t, strings.HasSuffix(name, ".jsonl"))

	// UUIDv7 format: 8-4-4-4-12 hex chars
	uuidPart := strings.TrimSuffix(name, ".jsonl")
	parts := strings.Split(uuidPart, "-")
	require.Len(t, parts, 5, "filename should be UUIDv7 format: %s", uuidPart)

	// version nibble (char index 0 of third group) should be '7' for UUIDv7
	require.True(t, len(parts[2]) == 4)
	assert.Equal(t, byte('7'), parts[2][0], "UUID version nibble should be 7")
}
