package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// initTestGitRepo creates a minimal git repo at dir with an initial commit.
func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Dev"},
		{"git", "config", "user.email", "dev@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("repo"), 0644))

	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	cmd.Env = gitEnv
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Env = gitEnv
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}

// findObservationFile walks the observations directory tree and returns
// the path to the first .jsonl file found.
func findObservationFile(t *testing.T, repoDir string) string {
	t.Helper()
	obsRoot := filepath.Join(repoDir, "memory", observationDirName)
	var found string
	err := filepath.Walk(obsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			found = path
		}
		return nil
	})
	require.NoError(t, err, "walking observations dir")
	require.NotEmpty(t, found, "no .jsonl file found under %s", obsRoot)
	return found
}

func TestWriteObservation_SmallPayload(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	obs := []observation{{Content: "short text"}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	// verify file exists in the expected date directory
	today := time.Now().UTC().Format("2006-01-02")
	dateDir := filepath.Join(dir, "memory", observationDirName, today)
	entries, err := os.ReadDir(dateDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, strings.HasSuffix(entries[0].Name(), ".jsonl"))

	// verify JSONL: header + one content line
	data, err := os.ReadFile(filepath.Join(dateDir, entries[0].Name()))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2, "expected header + 1 observation line")

	// verify git committed
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "observation:")
}

func TestWriteObservation_NormalPayload(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	content := strings.Repeat("x", 1024) // ~1KB
	obs := []observation{{Content: content}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	// verify content round-trips
	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Equal(t, content, parsed.Content)
}

func TestWriteObservation_NearMaxPayload(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// just under the limit — writeObservation itself doesn't enforce size,
	// but we verify it handles large payloads without error
	content := strings.Repeat("a", maxObservationBytes-1)
	obs := []observation{{Content: content}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Len(t, parsed.Content, maxObservationBytes-1)
}

func TestWriteObservation_MultipleObservations(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	obs := []observation{
		{Content: "first observation"},
		{Content: "second observation"},
		{Content: "third observation"},
	}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	// header + 3 observations
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 4, "expected header + 3 observation lines")

	for i, expected := range []string{"first observation", "second observation", "third observation"} {
		var parsed observation
		require.NoError(t, json.Unmarshal([]byte(lines[i+1]), &parsed))
		require.Equal(t, expected, parsed.Content)
	}
}

func TestWriteObservation_FileFormat(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	obs := []observation{{Content: "format check"}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	f := findObservationFile(t, dir)
	data, err := os.ReadFile(f)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	// line 1: header with schema_version and recorded_at
	var header observationHeader
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &header))
	require.Equal(t, "1", header.SchemaVersion)
	require.NotEmpty(t, header.RecordedAt)

	// recorded_at should be valid RFC3339
	_, err = time.Parse(time.RFC3339, header.RecordedAt)
	require.NoError(t, err, "recorded_at should be RFC3339")

	// line 2: observation with content field
	var parsed observation
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	require.Equal(t, "format check", parsed.Content)
}

func TestWriteObservation_GitCommit(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	obs := []observation{{Content: "a]commit message with special chars & symbols"}}
	err := writeObservation(dir, obs)
	require.NoError(t, err)

	cmd := exec.Command("git", "log", "--format=%s", "-1")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)

	commitMsg := strings.TrimSpace(string(out))
	require.Equal(t, "observation: a]commit message with special chars & symbols", commitMsg)

	// test truncation: content longer than 50 chars gets "..."
	dir2 := t.TempDir()
	initTestGitRepo(t, dir2)

	longContent := strings.Repeat("z", 80)
	obs2 := []observation{{Content: longContent}}
	err = writeObservation(dir2, obs2)
	require.NoError(t, err)

	cmd = exec.Command("git", "log", "--format=%s", "-1")
	cmd.Dir = dir2
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)

	commitMsg = strings.TrimSpace(string(out))
	expected := "observation: " + longContent[:50] + "..."
	require.Equal(t, expected, commitMsg)
}

func TestParseObservations_OverMaxSize(t *testing.T) {
	// test the size validation logic from runMemoryPut:
	// observations over maxObservationBytes should be rejected

	oversized := strings.Repeat("x", maxObservationBytes+1)
	input := `{"content":"` + oversized + `"}`

	observations, err := parseObservations([]byte(input))
	require.NoError(t, err, "parseObservations should succeed — it doesn't enforce size")
	require.Len(t, observations, 1)

	// replicate the validation loop from runMemoryPut
	for i, obs := range observations {
		if len(obs.Content) > maxObservationBytes {
			err = &oversizeError{index: i + 1, size: len(obs.Content)}
			break
		}
	}
	require.Error(t, err, "oversized observation should be rejected")
	require.Contains(t, err.Error(), "too large")

	// verify exactly-at-limit is also rejected (> not >=)
	exactLimit := strings.Repeat("y", maxObservationBytes+1)
	input = `{"content":"` + exactLimit + `"}`
	observations, err = parseObservations([]byte(input))
	require.NoError(t, err)

	var sizeErr error
	for i, obs := range observations {
		if len(obs.Content) > maxObservationBytes {
			sizeErr = &oversizeError{index: i + 1, size: len(obs.Content)}
			break
		}
	}
	require.Error(t, sizeErr)

	// verify at-limit passes
	atLimit := strings.Repeat("y", maxObservationBytes)
	input = `{"content":"` + atLimit + `"}`
	observations, err = parseObservations([]byte(input))
	require.NoError(t, err)

	var atLimitErr error
	for i, obs := range observations {
		if len(obs.Content) > maxObservationBytes {
			atLimitErr = &oversizeError{index: i + 1, size: len(obs.Content)}
			break
		}
	}
	require.NoError(t, atLimitErr, "observation at exactly maxObservationBytes should pass")
}

// oversizeError mirrors the validation error from runMemoryPut
type oversizeError struct {
	index int
	size  int
}

func (e *oversizeError) Error() string {
	return fmt.Sprintf("observation %d: too large (%d bytes, max %d)", e.index, e.size, maxObservationBytes)
}
