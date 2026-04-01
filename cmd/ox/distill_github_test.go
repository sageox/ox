package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBackend implements agentcli.Backend for testing.
type mockBackend struct {
	promptReceived string
	output         string
}

func (m *mockBackend) Name() string    { return "mock" }
func (m *mockBackend) Available() bool { return true }
func (m *mockBackend) Run(_ context.Context, prompt string) (string, error) {
	m.promptReceived = prompt
	return m.output, nil
}

// trackingBackend is a mockBackend that also counts how many times Run was called.
type trackingBackend struct {
	callCount int
	output    string
}

func (t *trackingBackend) Name() string    { return "tracking" }
func (t *trackingBackend) Available() bool { return true }
func (t *trackingBackend) Run(_ context.Context, _ string) (string, error) {
	t.callCount++
	return t.output, nil
}

func TestBuildGitHubExtractorPrompt(t *testing.T) {
	t.Parallel()

	clustersJSON := `[{"type":"pull_request","number":42}]`
	prompt := buildGitHubExtractorPrompt(clustersJSON, "7 days", "")

	// System prompt present
	assert.Contains(t, prompt, "You are a signal extractor for an alignment feed system")
	assert.Contains(t, prompt, "DECISIONS IN REVIEWS")
	assert.Contains(t, prompt, "SCOPE AND IMPACT FROM THE PR")

	// Output format includes category field
	assert.Contains(t, prompt, `"category"`)

	// User prompt wraps clusters in batch tags
	assert.Contains(t, prompt, "<batch>")
	assert.Contains(t, prompt, clustersJSON)
	assert.Contains(t, prompt, "</batch>")

	// Interval placeholder replaced
	assert.Contains(t, prompt, "7 days")
	assert.NotContains(t, prompt, "{interval}")
}

func TestBuildGitHubExtractorPrompt_ContainsBatchTags(t *testing.T) {
	t.Parallel()

	prompt := buildGitHubExtractorPrompt("[]", "24 hours", "")
	assert.Contains(t, prompt, "<batch>\n[]\n</batch>")
}

func TestBuildGitHubExtractorPrompt_WithGuidelines(t *testing.T) {
	t.Parallel()

	prompt := buildGitHubExtractorPrompt("[]", "1 day", "Focus on security-related changes.")

	// Guidelines should be injected
	assert.Contains(t, prompt, "<team-guidelines>")
	assert.Contains(t, prompt, "Focus on security-related changes.")
	assert.Contains(t, prompt, "</team-guidelines>")

	// System prompt still present
	assert.Contains(t, prompt, "You are a signal extractor for an alignment feed system")
}

func TestReadPendingGitHubFacts_EmptyDir(t *testing.T) {
	t.Parallel()

	result, err := readPendingGitHubFacts(t.TempDir(), time.Time{})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestReadPendingGitHubFacts_WithFactFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	content := "# GitHub Facts: 2026-03-20\n\nSome facts\n\n---\n*Extracted from GitHub activity (created 2026-03-20)*\n"
	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-20.md"), []byte(content), 0o644))

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)
	require.Len(t, result["2026-03-20"], 1)
	assert.Contains(t, result["2026-03-20"][0].Content, "GitHub Facts")
}

func TestReadPendingGitHubFacts_V2MetaHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// v2 JSONL file with _meta header + fact lines
	content := `{"_meta":{"schema_version":"2","source_type":"github","recorded_at":"2026-03-20T10:00:00Z"}}
{"headline":"Adopted rate limiting","source_type":"github","timestamp":"2026-03-20T10:00:00Z"}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-eeee-7abc-8def-0123456789ab.jsonl"),
		[]byte(content), 0o644))

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)
	require.Len(t, result["2026-03-20"], 1)
	assert.Equal(t, "2026-03-20", result["2026-03-20"][0].Date)
	assert.Contains(t, result["2026-03-20"][0].Content, `"_meta"`)
}

// setupExtractGitHubFacts creates a temp dir with CodeDB data and a git-initialized
// team context directory for testing extractGitHubFacts.
// The populateFn runs while the store is open for inserting test data;
// the store is closed before returning so extractGitHubFacts can reopen it.
func setupExtractGitHubFacts(t *testing.T, populateFn func(s *store.Store)) (projectRoot string, tcPath string, codeDBDir string) {
	t.Helper()

	projectRoot = t.TempDir()

	// Create CodeDB at the fallback path, populate, then close
	codeDBDir = filepath.Join(projectRoot, ".sageox", "cache", "codedb")
	s, err := store.Open(codeDBDir)
	require.NoError(t, err)
	if populateFn != nil {
		populateFn(s)
	}
	require.NoError(t, s.Close())

	// Create team context dir as a git repo (commitMemoryFile needs git)
	tcPath = t.TempDir()
	gitInit := exec.Command("git", "init")
	gitInit.Dir = tcPath
	require.NoError(t, gitInit.Run())
	gitCfgName := exec.Command("git", "config", "user.name", "test")
	gitCfgName.Dir = tcPath
	require.NoError(t, gitCfgName.Run())
	gitCfgEmail := exec.Command("git", "config", "user.email", "test@test.com")
	gitCfgEmail.Dir = tcPath
	require.NoError(t, gitCfgEmail.Run())

	return projectRoot, tcPath, codeDBDir
}

func TestExtractGitHubFacts_Integration(t *testing.T) {
	t.Parallel()

	ts := time.Now().UTC().Add(-1 * time.Hour).Unix()
	_, tcPath, codeDBDir := setupExtractGitHubFacts(t, func(s *store.Store) {
		res, err := s.Exec(`INSERT INTO pull_requests
			(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			42, "Add rate limiting", "Implements token bucket", "alice", "merged",
			"[]", ts, ts, ts, "https://github.com/org/repo/pull/42")
		require.NoError(t, err)
		prID, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = s.Exec(`INSERT INTO pr_comments (pr_id, author, body, path, line, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, prID, "bob", "review comment", "api/handler.go", 10, ts)
		require.NoError(t, err)
	})

	// Set up mock backend
	backend := &mockBackend{
		output: `{"headline":"Adopted token bucket rate limiting","summary":"Token bucket at 100 req/min","rationale":"Traffic growth","who":"alice","source_type":"github","source_ref":"https://github.com/org/repo/pull/42","timestamp":"2026-03-20T00:00:00Z"}`,
	}

	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	// Verify prompt was sent to backend
	assert.Contains(t, backend.promptReceived, "signal extractor")
	assert.Contains(t, backend.promptReceived, "<batch>")
	assert.Contains(t, backend.promptReceived, "Add rate limiting")

	// Verify fact file was written with UUID7 filename
	// Use the same timestamp as the inserted data to avoid UTC midnight boundary flakes
	expectedDay := time.Unix(ts, 0).UTC().Format("2006-01-02")
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	entries, err := os.ReadDir(factsDir)
	require.NoError(t, err)

	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), expectedDay) && strings.HasSuffix(e.Name(), ".jsonl") {
			content, err := os.ReadFile(filepath.Join(factsDir, e.Name()))
			require.NoError(t, err)
			contentStr := string(content)
			assert.Contains(t, contentStr, "Adopted token bucket rate limiting")
			// v2 _meta header should be present
			assert.Contains(t, contentStr, `"_meta"`)
			assert.Contains(t, contentStr, `"schema_version":"2"`)
			assert.Contains(t, contentStr, `"source_type":"github"`)
			// query_since and source_hash should be present
			assert.Contains(t, contentStr, `"query_since"`)
			assert.Contains(t, contentStr, `"source_hash"`)
			// UUID7 filename should be longer than just "YYYY-MM-DD.jsonl"
			assert.Greater(t, len(e.Name()), len(expectedDay+".jsonl"))
			found = true
		}
	}
	assert.True(t, found, "expected UUID7 fact file for %s", expectedDay)
}

func TestExtractGitHubFacts_DryRun(t *testing.T) {
	// NOT parallel: mutates package-level distillDryRun

	prTime := time.Now().UTC().Add(-1 * time.Hour)
	ts := prTime.Unix()
	_, tcPath, codeDBDir := setupExtractGitHubFacts(t, func(s *store.Store) {
		_, err := s.Exec(`INSERT INTO pull_requests
			(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			42, "Test PR", "body", "alice", "open", "[]", ts, ts, nil, "")
		require.NoError(t, err)
	})

	backend := &mockBackend{}
	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	oldDryRun := distillDryRun
	distillDryRun = true
	defer func() { distillDryRun = oldDryRun }()

	err := extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	// Backend should NOT have been called
	assert.Empty(t, backend.promptReceived)
	// Output should show what would be processed, with per-day date
	output := buf.String()
	assert.Contains(t, output, "GitHub activity")
	expectedDay := prTime.Format("2006-01-02")
	assert.Contains(t, output, expectedDay, "dry-run should show per-day date")
}

func TestExtractGitHubFacts_EmptySkip(t *testing.T) {
	t.Parallel()

	_, tcPath, codeDBDir := setupExtractGitHubFacts(t, nil)

	backend := &mockBackend{}
	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	// Backend should NOT have been called (no activity)
	assert.Empty(t, backend.promptReceived)
}

// insertPRInCodeDB opens the CodeDB store, inserts a PR, and closes it.
func insertPRInCodeDB(t *testing.T, projectRoot string, number int, title, author, state string, ts int64) {
	t.Helper()
	codeDBDir := filepath.Join(projectRoot, ".sageox", "cache", "codedb")
	s, err := store.Open(codeDBDir)
	require.NoError(t, err)
	_, err = s.Exec(`INSERT INTO pull_requests
		(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		number, title, "body", author, state, "[]", ts, ts, ts, "")
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestExtractGitHubFacts_TwoRunStateTracking(t *testing.T) {
	t.Parallel()

	baseTime := time.Now().UTC().Add(-2 * time.Hour)
	projectRoot, tcPath, codeDBDir := setupExtractGitHubFacts(t, func(s *store.Store) {
		for i := 1; i <= 3; i++ {
			ts := baseTime.Add(time.Duration(i) * time.Minute).Unix()
			_, err := s.Exec(`INSERT INTO pull_requests
				(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				i, "PR", "body", "alice", "merged", "[]", ts, ts, ts, "")
			require.NoError(t, err)
		}
	})

	backend := &mockBackend{output: `{"headline":"first run","source_type":"github","timestamp":"2026-03-23T00:00:00Z"}`}
	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	// First run: processes PRs #1-3
	err := extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)
	assert.Contains(t, backend.promptReceived, "<batch>")

	// Verify first run wrote UUID7 fact file(s) with source_hash/date prefix in _meta
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	entries1, err := os.ReadDir(factsDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries1), 1, "first run should produce at least 1 fact file")

	// Insert PR #4 with current timestamp — second run should pick it up
	// because inferGitHubQueryHighWater reads source_hash/date prefix from first run's files
	ts4 := time.Now().UTC().Unix()
	insertPRInCodeDB(t, projectRoot, 4, "New PR", "bob", "open", ts4)

	// Second run: should use source_hash/date prefix from first run's fact files as window start
	backend.promptReceived = ""
	backend.output = `{"headline":"second run","source_type":"github","timestamp":"2026-03-23T00:00:00Z"}`
	cmd.SetOut(&bytes.Buffer{})

	err = extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	// The second run should have processed PR #4 (high-water from fact files works)
	assert.Contains(t, backend.promptReceived, "New PR")

	// Both runs should have created separate UUID7 files (no overwrite)
	entries2, err := os.ReadDir(factsDir)
	require.NoError(t, err)
	assert.Greater(t, len(entries2), len(entries1), "second run should add new UUID7 files without overwriting first run")
}

func TestExtractGitHubFacts_IntraDayRerun(t *testing.T) {
	t.Parallel()

	ts := time.Now().UTC().Add(-1 * time.Hour).Unix()
	projectRoot, tcPath, codeDBDir := setupExtractGitHubFacts(t, func(s *store.Store) {
		_, err := s.Exec(`INSERT INTO pull_requests
			(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			42, "First PR", "body", "alice", "merged", "[]", ts, ts, ts, "")
		require.NoError(t, err)
	})

	backend := &mockBackend{output: `{"headline":"run 1","source_type":"github","timestamp":"2026-03-23T00:00:00Z"}`}
	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	// First run
	err := extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	entries1, err := os.ReadDir(factsDir)
	require.NoError(t, err)
	firstRunCount := len(entries1)
	assert.GreaterOrEqual(t, firstRunCount, 1, "first run should produce fact files")

	// Insert new activity — second run picks up from source_hash/date prefix in first run's files
	insertPRInCodeDB(t, projectRoot, 43, "Second PR", "bob", "open", time.Now().UTC().Unix())

	backend.promptReceived = ""
	backend.output = `{"headline":"run 2","source_type":"github","timestamp":"2026-03-23T00:00:00Z"}`
	cmd.SetOut(&bytes.Buffer{})

	// Second run same day — uses source_hash/date prefix from first run's fact files
	err = extractGitHubFacts(context.Background(), cmd, backend, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	entries2, err := os.ReadDir(factsDir)
	require.NoError(t, err)

	// UUID7 filenames prevent overwrite — both runs produce separate files
	assert.Greater(t, len(entries2), firstRunCount, "intra-day re-run should add files, not overwrite")

	// Verify all files are for valid days and have UUID7 suffix.
	// The first run inserts data 1 hour ago (may be previous UTC day near midnight),
	// the second run inserts data at now — both are valid dates.
	firstRunDay := time.Unix(ts, 0).UTC().Format("2006-01-02")
	secondRunDay := time.Now().UTC().Format("2006-01-02")
	for _, e := range entries2 {
		validDay := strings.HasPrefix(e.Name(), firstRunDay) || strings.HasPrefix(e.Name(), secondRunDay)
		assert.True(t, validDay, "file %s should be for %s or %s", e.Name(), firstRunDay, secondRunDay)
		assert.Greater(t, len(e.Name()), len("2006-01-02.jsonl"), "file %s should have UUID7 suffix", e.Name())
	}
}

func TestExtractGitHubFacts_MultiDayCatchup(t *testing.T) {
	t.Parallel()

	// Insert PRs across 3 different days
	day1 := time.Now().UTC().AddDate(0, 0, -3) // 3 days ago
	day2 := time.Now().UTC().AddDate(0, 0, -2) // 2 days ago
	day3 := time.Now().UTC().AddDate(0, 0, -1) // yesterday

	_, tcPath, codeDBDir := setupExtractGitHubFacts(t, func(s *store.Store) {
		for i, dayTS := range []time.Time{day1, day2, day3} {
			ts := dayTS.Unix()
			_, err := s.Exec(`INSERT INTO pull_requests
				(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				i+1, fmt.Sprintf("PR for day %d", i+1), "body", "alice", "merged",
				"[]", ts, ts, ts, "")
			require.NoError(t, err)
		}
	})

	tracker := &trackingBackend{output: `{"headline":"facts","source_type":"github","timestamp":"2026-03-23T00:00:00Z"}`}

	tc := &config.TeamContext{Path: tcPath}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	// No existing fact files — inferGitHubQueryHighWater falls back to 7 days ago,
	// which covers all 3 days of inserted data
	err := extractGitHubFacts(context.Background(), cmd, tracker, tc, "test-repo", codeDBDir, "")
	require.NoError(t, err)

	// Verify fact files were created for each day
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	entries, err := os.ReadDir(factsDir)
	require.NoError(t, err)

	// Should have at least 3 fact files (one per day)
	assert.GreaterOrEqual(t, len(entries), 3, "multi-day catch-up should produce one file per day")

	// Verify each day has a fact file
	dayStrs := []string{
		day1.UTC().Format("2006-01-02"),
		day2.UTC().Format("2006-01-02"),
		day3.UTC().Format("2006-01-02"),
	}
	for _, dayStr := range dayStrs {
		found := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), dayStr) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected fact file for %s", dayStr)
	}

	// Backend should have been called 3 times (once per day)
	assert.Equal(t, 3, tracker.callCount, "should call LLM once per day")
}

func TestExtractGitHubFacts_HighWaterInference(t *testing.T) {
	t.Parallel()

	// Insert a PR from 2 days ago
	twoDaysAgo := time.Now().UTC().AddDate(0, 0, -2)
	_, tcPath, _ := setupExtractGitHubFacts(t, func(s *store.Store) {
		ts := twoDaysAgo.Unix()
		_, err := s.Exec(`INSERT INTO pull_requests
			(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			1, "Old PR", "body", "alice", "merged", "[]", ts, ts, ts, "")
		require.NoError(t, err)
	})

	// Pre-populate existing fact files (simulates fresh clone with existing facts)
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))
	threeDaysAgo := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
	content := fmt.Sprintf("# GitHub Facts: %s\n\nOld facts\n\n---\n*Extracted from GitHub activity (created %s)*\n", threeDaysAgo, threeDaysAgo)
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, threeDaysAgo+"-019526a0-aaaa-7abc-8def-0123456789ab.md"),
		[]byte(content), 0o644))

	// inferGitHubQueryHighWater should fall back to filename-based inference
	// (no .jsonl files with source_hash/date prefix, but .md files have date prefix)
	since := inferGitHubQueryHighWater(tcPath)

	// High-water should be inferred from the most recent fact file date
	expectedDate := threeDaysAgo
	assert.Equal(t, expectedDate, since.Format("2006-01-02"),
		"should infer high-water from existing fact file date")
}

func TestInferGitHubFactsHighWater_NoFiles(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	hw := inferGitHubFactsHighWater(tcPath)
	assert.True(t, hw.IsZero(), "should return zero time when no facts dir exists")
}

func TestInferGitHubFactsHighWater_EmptyDir(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tcPath, "memory", ".github-facts"), 0o755))

	hw := inferGitHubFactsHighWater(tcPath)
	assert.True(t, hw.IsZero(), "should return zero time for empty facts dir")
}

func TestInferGitHubFactsHighWater_OldNaming(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-15.md"), []byte("facts"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-18.md"), []byte("facts"), 0o644))

	hw := inferGitHubFactsHighWater(tcPath)
	assert.Equal(t, "2026-03-18", hw.Format("2006-01-02"))
}

func TestInferGitHubFactsHighWater_UUID7Naming(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-19-019526a0-7e8b-7abc-8def-0123456789ab.md"),
		[]byte("facts"), 0o644))

	hw := inferGitHubFactsHighWater(tcPath)
	assert.Equal(t, "2026-03-19", hw.Format("2006-01-02"))
}

func TestInferGitHubFactsHighWater_Mixed(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Old naming for earlier date
	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-15.md"), []byte("old"), 0o644))
	// UUID7 naming for later date
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-7e8b-7abc-8def-0123456789ab.md"),
		[]byte("new"), 0o644))

	hw := inferGitHubFactsHighWater(tcPath)
	assert.Equal(t, "2026-03-20", hw.Format("2006-01-02"),
		"should pick the latest date across mixed naming")
}

func TestInferGitHubFactsHighWater_JSONLOnly(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Only .jsonl files (no .md) — should still infer high-water
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-22-019526a0-aaaa-7abc-8def-0123456789ab.jsonl"),
		[]byte("facts"), 0o644))

	hw := inferGitHubFactsHighWater(tcPath)
	assert.Equal(t, "2026-03-22", hw.Format("2006-01-02"),
		"should detect high-water from .jsonl files")
}

func TestReadPendingGitHubFacts_FiltersBySince(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// old fact
	old := "# GitHub Facts: 2026-03-10\n\nOld\n\n---\n*Extracted (created 2026-03-10)*\n"
	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-10.md"), []byte(old), 0o644))

	// new fact
	newFact := "# GitHub Facts: 2026-03-20\n\nNew\n\n---\n*Extracted (created 2026-03-20)*\n"
	require.NoError(t, os.WriteFile(filepath.Join(factsDir, "2026-03-20.md"), []byte(newFact), 0o644))

	since := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	result, err := readPendingGitHubFacts(dir, since)
	require.NoError(t, err)

	assert.Nil(t, result["2026-03-10"]) // filtered out
	assert.Len(t, result["2026-03-20"], 1)
}

func TestReadPendingGitHubFacts_UUID7Filenames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	content := "# GitHub Facts: 2026-03-20\n\nFacts here\n\n---\n*Extracted from GitHub activity (created 2026-03-20)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-7e8b-7abc-8def-0123456789ab.md"),
		[]byte(content), 0o644))

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)
	require.Len(t, result["2026-03-20"], 1)
	assert.Contains(t, result["2026-03-20"][0].Content, "GitHub Facts: 2026-03-20")
	assert.Contains(t, result["2026-03-20"][0].RelPath, "2026-03-20-019526a0")
}

func TestReadPendingGitHubFacts_MultipleUUID7PerDay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Two UUID7 files for same day (simulates intra-day re-run)
	content1 := "# GitHub Facts: 2026-03-20\n\nFirst run\n\n---\n*Extracted from GitHub activity (created 2026-03-20)*\n"
	content2 := "# GitHub Facts: 2026-03-20\n\nSecond run\n\n---\n*Extracted from GitHub activity (created 2026-03-20)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-1111-7abc-8def-0123456789ab.md"),
		[]byte(content1), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-2222-7abc-8def-0123456789ab.md"),
		[]byte(content2), 0o644))

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)

	// Both files should be grouped under the same date key
	require.Len(t, result["2026-03-20"], 2)

	// Verify both files' content is present
	var contents []string
	for _, entry := range result["2026-03-20"] {
		contents = append(contents, entry.Content)
	}
	assert.Contains(t, contents[0]+contents[1], "First run")
	assert.Contains(t, contents[0]+contents[1], "Second run")
}

func TestReadPendingGitHubFacts_UUID7WithSinceFilter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Old fact with UUID7 name — should be filtered
	oldContent := "# GitHub Facts: 2026-03-10\n\nOld\n\n---\n*Extracted from GitHub activity (created 2026-03-10)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-10-019526a0-aaaa-7abc-8def-0123456789ab.md"),
		[]byte(oldContent), 0o644))

	// New fact with UUID7 name — should pass filter
	newContent := "# GitHub Facts: 2026-03-20\n\nNew\n\n---\n*Extracted from GitHub activity (created 2026-03-20)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-019526a0-bbbb-7abc-8def-0123456789ab.md"),
		[]byte(newContent), 0o644))

	since := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	result, err := readPendingGitHubFacts(dir, since)
	require.NoError(t, err)

	assert.Nil(t, result["2026-03-10"], "old UUID7 fact should be filtered by since")
	require.Len(t, result["2026-03-20"], 1, "new UUID7 fact should pass since filter")
}

func TestReadPendingGitHubFacts_MixedOldAndUUID7(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Old-style filename
	oldStyle := "# GitHub Facts: 2026-03-18\n\nOld style\n\n---\n*Extracted from GitHub activity (created 2026-03-18)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-18.md"),
		[]byte(oldStyle), 0o644))

	// New UUID7-style filename for same day
	newStyle := "# GitHub Facts: 2026-03-18\n\nNew style\n\n---\n*Extracted from GitHub activity (created 2026-03-18)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-18-019526a0-cccc-7abc-8def-0123456789ab.md"),
		[]byte(newStyle), 0o644))

	// UUID7 file for different day
	otherDay := "# GitHub Facts: 2026-03-19\n\nOther day\n\n---\n*Extracted from GitHub activity (created 2026-03-19)*\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-19-019526a0-dddd-7abc-8def-0123456789ab.md"),
		[]byte(otherDay), 0o644))

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)

	// Both old and new naming for 2026-03-18 grouped together
	require.Len(t, result["2026-03-18"], 2, "old and UUID7 filenames for same date should group together")
	require.Len(t, result["2026-03-19"], 1)
}

func TestReadPendingGitHubFacts_MultiDayCatchup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	factsDir := filepath.Join(dir, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Simulate 3 days of facts (what multi-day catch-up produces)
	days := []string{"2026-03-18", "2026-03-19", "2026-03-20"}
	for _, day := range days {
		content := fmt.Sprintf("# GitHub Facts: %s\n\nFacts for %s\n\n---\n*Extracted from GitHub activity (created %s)*\n", day, day, day)
		filename := fmt.Sprintf("%s-019526a0-%s-7abc-8def-0123456789ab.md", day, day[8:10])
		require.NoError(t, os.WriteFile(filepath.Join(factsDir, filename), []byte(content), 0o644))
	}

	result, err := readPendingGitHubFacts(dir, time.Time{})
	require.NoError(t, err)

	// Each day should have its own bucket
	assert.Len(t, result, 3, "should have 3 separate date buckets")
	for _, day := range days {
		require.Len(t, result[day], 1, "day %s should have exactly 1 fact", day)
		assert.Contains(t, result[day][0].Content, "Facts for "+day)
	}
}

// --- inferGitHubQueryHighWater tests ---

func TestInferGitHubQueryHighWater_WithFiles(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Two JSONL files with different date prefixes
	file1 := `{"_meta":{"schema_version":"2","source_type":"github","recorded_at":"2026-03-18T00:00:00Z"}}
{"headline":"fact1","source_type":"github","timestamp":"2026-03-18T10:00:00Z"}
`
	file2 := `{"_meta":{"schema_version":"2","source_type":"github","recorded_at":"2026-03-20T00:00:00Z"}}
{"headline":"fact2","source_type":"github","timestamp":"2026-03-20T10:00:00Z"}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-18-pr-100.jsonl"),
		[]byte(file1), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-20-pr-200.jsonl"),
		[]byte(file2), 0o644))

	hw := inferGitHubQueryHighWater(tcPath)

	// Should return start-of-day for the latest date prefix in filenames
	expected, _ := time.Parse("2006-01-02", "2026-03-20")
	assert.Equal(t, expected, hw, "should return latest date from filenames")
}

func TestInferGitHubQueryHighWater_NoQueryUntil_FallsBackToFilename(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Old-style .md file without source_hash/date prefix — should fall back to filename-based
	content := "# GitHub Facts: 2026-03-15\n\nOld facts\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-15-019526a0-aaaa-7abc-8def-0123456789ab.md"),
		[]byte(content), 0o644))

	hw := inferGitHubQueryHighWater(tcPath)

	// Should fall back to inferGitHubFactsHighWater (filename-based)
	assert.Equal(t, "2026-03-15", hw.Format("2006-01-02"),
		"should fall back to filename-based high water when no source_hash/date prefix present")
}

func TestInferGitHubQueryHighWater_NoFiles_DefaultsTo7DaysAgo(t *testing.T) {
	t.Parallel()

	tcPath := t.TempDir()
	hw := inferGitHubQueryHighWater(tcPath)

	expected := time.Now().UTC().AddDate(0, 0, -7)
	assert.InDelta(t, expected.Unix(), hw.Unix(), 2,
		"should default to ~7 days ago when no fact files exist")
}
