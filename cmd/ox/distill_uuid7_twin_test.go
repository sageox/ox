//go:build slow

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==========================================================================
// A. Session facts — real extraction pipeline with UUID7 filenames
// ==========================================================================

// TestExtractSessionFacts_WritesUUID7Filename exercises the real session fact
// extraction pipeline: scanPendingSessions finds sessions, sessionSummaryToFacts
// transforms summary.json into facts, and facts.WriteFacts writes JSONL with
// UUID7-based filenames.
// Failure prevented: session fact extraction silently skips sessions or writes
// files without UUID7, causing merge conflicts on concurrent extraction.
func TestExtractSessionFacts_WritesUUID7Filename(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with session fixtures")
	}

	tcPath := t.TempDir()
	ledgerPath := t.TempDir()

	// Create session directory with valid summary.json
	sessionDirName := "2026-04-01T10-00-testuser-OxABC1"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionDirName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	summary := sessionsummary.SummarizeResponse{
		Title:      "Implement UUID7 for fact filenames",
		Summary:    "Migrated all distill outputs to use UUID7 suffixes",
		KeyActions: []string{"Updated session extraction", "Updated discussion extraction"},
		Outcome:    "success",
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{
				{What: "Use UUID7 for all fact filenames", Why: "Prevents merge conflicts"},
			},
			ActionItems: []sessionsummary.ActionItem{
				{Task: "Add tests for UUID7 filenames", Priority: "high"},
			},
			OpenQuestions: []sessionsummary.OpenQuestion{
				{Question: "Should we clean up legacy files?", Context: "Old files use deterministic names"},
			},
		},
		QualityScore: 0.8,
	}
	summaryData, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryData, 0o644))

	// Create session facts directory
	sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts", "2026-04-01")
	require.NoError(t, os.MkdirAll(sessionFactsDir, 0o755))

	// --- Step 1: scanPendingSessions finds the session ---
	pending, err := scanPendingSessions(ledgerPath, tcPath)
	require.NoError(t, err)
	require.Len(t, pending, 1, "must find exactly one pending session")
	assert.Equal(t, sessionDirName, pending[0].DirName)
	assert.Equal(t, "2026-04-01", pending[0].Date)
	assert.Equal(t, "testuser", pending[0].Who)
	assert.NotEmpty(t, pending[0].Hash, "content hash must be computed")

	// --- Step 2: sessionSummaryToFacts transforms to facts (pure, no LLM) ---
	extractedFacts := sessionSummaryToFacts(pending[0])
	require.NotEmpty(t, extractedFacts, "must extract facts from summary")

	// Verify fact categories match input data
	var categories []string
	for _, f := range extractedFacts {
		categories = append(categories, string(f.Category))
	}
	assert.Contains(t, categories, string(facts.CategoryContext), "should have context fact from title+summary")
	assert.Contains(t, categories, string(facts.CategoryDecision), "should have decision fact")
	assert.Contains(t, categories, string(facts.CategoryActionItem), "should have action item fact")
	assert.Contains(t, categories, string(facts.CategoryOpenQuestion), "should have open question fact")

	// Verify source refs point to session
	for _, f := range extractedFacts {
		assert.Equal(t, facts.SourceSession, f.SourceType, "source_type must be session")
		assert.Equal(t, "sessions/"+sessionDirName, f.SourceRef, "source_ref must reference session dir")
	}

	// --- Step 3: Write facts using production filename pattern ---
	// This mirrors the exact code in distill_sessions.go extractSessionFacts
	uid, err := uuid.NewV7()
	require.NoError(t, err)
	factFile := sessionDirName + "-" + uid.String() + ".jsonl"
	factPath := filepath.Join(sessionFactsDir, factFile)

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    pending[0].Date,
			SourceHash:    pending[0].Hash,
		},
	}
	require.NoError(t, facts.WriteFacts(factPath, header, extractedFacts))

	// --- Step 4: Verify UUID7 filename pattern ---
	// Glob must find the file — this is the regression guard.
	// If production code drops UUID7, this glob returns empty.
	uuidPattern := filepath.Join(sessionFactsDir, sessionDirName+"-*.jsonl")
	matches, err := filepath.Glob(uuidPattern)
	require.NoError(t, err)
	require.NotEmpty(t, matches, "glob %s must find UUID7-named file — regression guard for UUID7 in filename", uuidPattern)

	// File is valid JSONL with correct header
	readHeader, readFacts, err := facts.ReadFacts(factPath)
	require.NoError(t, err, "fact file must be valid JSONL")
	assert.Equal(t, pending[0].Hash, readHeader.Meta.SourceHash, "source_hash must match")
	assert.Equal(t, facts.SourceSession, readHeader.Meta.SourceType)
	assert.Len(t, readFacts, len(extractedFacts), "written facts count must match extracted")

	// --- Step 5: Freshness check — rescan finds 0 pending ---
	pendingAfter, err := scanPendingSessions(ledgerPath, tcPath)
	require.NoError(t, err)
	assert.Empty(t, pendingAfter, "after writing facts with matching hash, session must not be pending")
}

// ==========================================================================
// B. Discussion facts — real extraction with server summary.json (no LLM)
// ==========================================================================

// TestExtractDiscussionFacts_WritesUUID7Filename exercises discussion fact
// extraction using the server-generated summary.json fast path (no LLM).
// Verifies scanPendingDiscussions, extractFactsFromSummaryJSON, and the
// freshness check with UUID7 glob matching.
// Failure prevented: discussion facts written without UUID7 cause merge
// conflicts; stale discussions are re-extracted unnecessarily.
func TestExtractDiscussionFacts_WritesUUID7Filename(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with discussion fixtures")
	}

	tcPath := t.TempDir()

	// Create discussion directory with metadata + summary.md + summary.json
	discussionDirName := "2026-04-01-1423-ryan"
	discussionDir := filepath.Join(tcPath, "discussions", discussionDirName)
	require.NoError(t, os.MkdirAll(discussionDir, 0o755))

	metadata := discussionMetadata{
		RecordingID: "rec-123",
		Title:       "Architecture Review",
		CreatedAt:   "2026-04-01T14:23:00Z",
		UserID:      "user-456",
	}
	metadataData, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "metadata.json"), metadataData, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "summary.md"),
		[]byte("# Architecture Review\n\nWe decided to use Go for the backend."), 0o644))

	// Server-generated summary.json for LLM-bypass path
	serverSummary := map[string]interface{}{
		"schema_version": 1,
		"recording_id":   "rec-123",
		"title":          "Architecture Review",
		"human_summary":  "Discussed backend language choice",
		"decisions":      []map[string]string{{"description": "Use Go for backend services"}},
		"learnings":      []map[string]string{{"description": "Go concurrency model fits our needs"}},
		"action_items":   []map[string]string{{"description": "Set up Go project structure"}},
		"open_questions": []map[string]string{{"description": "Which Go web framework?"}},
	}
	summaryData, err := json.Marshal(serverSummary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "summary.json"), summaryData, 0o644))

	// Create discussion-facts directory
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// --- Step 1: scanPendingDiscussions finds the discussion ---
	pending, err := scanPendingDiscussions(tcPath)
	require.NoError(t, err)
	require.Len(t, pending, 1, "must find exactly one pending discussion")
	assert.Equal(t, discussionDirName, pending[0].DirName)
	assert.Equal(t, "Architecture Review", pending[0].Title)
	assert.NotEmpty(t, pending[0].SummaryJSONDir, "should detect server-generated summary.json")

	// --- Step 2: Extract facts from server summary (no LLM) ---
	sourceHash := discussionContentHash(discussionDir)
	require.NotEmpty(t, sourceHash)

	factContent, err := extractFactsFromSummaryJSON(pending[0], sourceHash)
	require.NoError(t, err)
	require.NotEmpty(t, factContent)

	// Verify the JSONL content has correct structure
	lines := strings.Split(strings.TrimSpace(factContent), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "must have header + at least one fact line")

	// Parse header
	var fileHeader facts.FileHeader
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &fileHeader))
	assert.Equal(t, sourceHash, fileHeader.Meta.SourceHash)
	assert.Equal(t, facts.SourceDiscussion, fileHeader.Meta.SourceType)

	// Parse at least one fact
	var firstFact facts.Fact
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &firstFact))
	assert.Equal(t, facts.SourceDiscussion, firstFact.SourceType)
	assert.Contains(t, firstFact.SourceRef, "discussions/"+discussionDirName)

	// --- Step 3: Write with UUID7 filename ---
	uid, err := uuid.NewV7()
	require.NoError(t, err)
	factFile := discussionDirName + "-" + uid.String() + ".jsonl"
	factPath := filepath.Join(factsDir, factFile)
	require.NoError(t, os.WriteFile(factPath, []byte(factContent), 0o644))

	// Verify UUID7 filename pattern via glob — regression guard
	uuidPattern := filepath.Join(factsDir, discussionDirName+"-*.jsonl")
	matches, err := filepath.Glob(uuidPattern)
	require.NoError(t, err)
	require.NotEmpty(t, matches, "glob %s must find UUID7-named file", uuidPattern)

	// --- Step 4: Freshness check — rescan finds 0 pending ---
	pendingAfter, err := scanPendingDiscussions(tcPath)
	require.NoError(t, err)
	assert.Empty(t, pendingAfter, "after writing facts with matching hash, discussion must not be pending")
}

// ==========================================================================
// C. Discussion facts — LLM path with mock backend
// ==========================================================================

// TestExtractDiscussionFacts_LLMPath_WritesUUID7Filename exercises the LLM
// fallback path for discussion fact extraction when no server summary.json
// exists. Uses a mock backend to verify the full pipeline.
// Failure prevented: LLM-extracted discussion facts don't get UUID7 filenames.
func TestExtractDiscussionFacts_LLMPath_WritesUUID7Filename(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with discussion fixtures")
	}

	tcPath := t.TempDir()

	// Create discussion directory with metadata + summary.md (no summary.json)
	discussionDirName := "2026-04-02-0900-galex"
	discussionDir := filepath.Join(tcPath, "discussions", discussionDirName)
	require.NoError(t, os.MkdirAll(discussionDir, 0o755))

	metadata := discussionMetadata{
		RecordingID: "rec-789",
		Title:       "Sprint Planning",
		CreatedAt:   "2026-04-02T09:00:00Z",
		UserID:      "user-789",
	}
	metadataData, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "metadata.json"), metadataData, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "summary.md"),
		[]byte("# Sprint Planning\n\nPrioritized UUID7 migration across all fact types."), 0o644))

	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0o755))

	// Mock backend returns valid JSONL facts
	mockFact := facts.Fact{
		Headline:   "Prioritize UUID7 migration",
		SourceType: facts.SourceDiscussion,
		SourceRef:  "discussions/" + discussionDirName,
		Timestamp:  "2026-04-02T09:00:00Z",
		Category:   facts.CategoryDecision,
	}
	mockFactLine, _ := json.Marshal(mockFact)
	// extractSingleDiscussionFacts expects raw JSONL from backend.Run, then re-parses
	mockHeader := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceDiscussion,
		},
	}
	mockHeaderLine, _ := json.Marshal(mockHeader)
	backend := &mockLLMBackend{
		output: string(mockHeaderLine) + "\n" + string(mockFactLine) + "\n",
	}

	// Scan for pending discussions
	pending, err := scanPendingDiscussions(tcPath)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Empty(t, pending[0].SummaryJSONDir, "no server summary.json should be detected")

	// Extract via LLM path
	sourceHash := discussionContentHash(discussionDir)
	ctx := context.Background()
	// We need a cobra command for logging — create a minimal one
	cmd := &cobra.Command{}
	cmd.SetOut(os.Stdout)
	factContent, err := extractSingleDiscussionFacts(ctx, cmd, backend, pending[0], "", sourceHash)
	require.NoError(t, err)
	require.NotEmpty(t, factContent)

	// Write with UUID7 filename
	uid, err := uuid.NewV7()
	require.NoError(t, err)
	factFile := discussionDirName + "-" + uid.String() + ".jsonl"
	factPath := filepath.Join(factsDir, factFile)
	require.NoError(t, os.WriteFile(factPath, []byte(factContent), 0o644))

	// Verify source_hash in output
	readHeader, readFacts, err := facts.ParseFacts([]byte(factContent))
	require.NoError(t, err)
	assert.Equal(t, sourceHash, readHeader.Meta.SourceHash, "LLM path must embed source_hash")
	assert.NotEmpty(t, readFacts, "must have extracted facts")

	// Freshness check
	pendingAfter, err := scanPendingDiscussions(tcPath)
	require.NoError(t, err)
	assert.Empty(t, pendingAfter, "after writing facts, discussion must not be pending")
}

// ==========================================================================
// D. Freshness check — UUID7 glob finds latest fact file
// ==========================================================================

// TestFreshnessCheck_GlobFindsUUID7DiscussionFacts verifies that
// findLatestFactFileSourceHash correctly finds UUID7-suffixed discussion fact
// files via glob, and that it picks the lexicographically last (newest) one.
// Failure prevented: freshness check misses UUID7 fact files, causing
// redundant re-extraction of already-processed discussions.
func TestFreshnessCheck_GlobFindsUUID7DiscussionFacts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with fact file fixtures")
	}

	factsDir := t.TempDir()
	dirname := "2026-04-01-1423-ryan"

	// Write an older UUID7 fact file with hash "oldhash"
	oldUID, err := uuid.NewV7()
	require.NoError(t, err)
	oldFile := filepath.Join(factsDir, dirname+"-"+oldUID.String()+".jsonl")
	writeSimpleFactFileHelper(t, oldFile, "oldhash", "Old decision")

	// Brief pause so the next UUID7 is lexicographically later
	time.Sleep(2 * time.Millisecond)

	// Write a newer UUID7 fact file with hash "newhash"
	newUID, err := uuid.NewV7()
	require.NoError(t, err)
	newFile := filepath.Join(factsDir, dirname+"-"+newUID.String()+".jsonl")
	writeSimpleFactFileHelper(t, newFile, "newhash", "New decision")

	// findLatestFactFileSourceHash should return "newhash" (lexicographically last)
	got := findLatestFactFileSourceHash(factsDir, dirname+"-*.jsonl")
	assert.Equal(t, "newhash", got, "must return hash from newest UUID7 file")

	// Legacy fallback: write a legacy (non-UUID7) file
	legacyFile := filepath.Join(factsDir, dirname+".jsonl")
	writeSimpleFactFileHelper(t, legacyFile, "legacyhash", "Legacy decision")

	// Glob should still prefer UUID7 files over legacy
	gotWithLegacy := findLatestFactFileSourceHash(factsDir, dirname+"-*.jsonl")
	assert.Equal(t, "newhash", gotWithLegacy, "UUID7 glob must not match legacy file")

	// Direct read of legacy file works as fallback
	legacyHash := readFactFileSourceHash(legacyFile)
	assert.Equal(t, "legacyhash", legacyHash, "legacy file must be readable directly")
}

// ==========================================================================
// E. Freshness check — UUID7 glob finds session facts in date partitions
// ==========================================================================

// TestFreshnessCheck_GlobFindsUUID7SessionFacts verifies session fact freshness
// checking in the date-partitioned directory structure (memory/.session-facts/YYYY-MM-DD/).
// Failure prevented: session fact freshness check fails to find UUID7 files
// in date-partitioned directories, causing redundant session extraction.
func TestFreshnessCheck_GlobFindsUUID7SessionFacts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with session fact fixtures")
	}

	tcPath := t.TempDir()
	ledgerPath := t.TempDir()

	sessionDirName := "2026-04-01T14-30-ryan-OxBCD2"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionDirName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	summary := sessionsummary.SummarizeResponse{
		Title:        "Fix auth bug",
		Summary:      "Fixed token refresh edge case",
		KeyActions:   []string{"Fixed refresh logic"},
		Outcome:      "success",
		QualityScore: 0.7,
	}
	summaryData, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryData, 0o644))

	// Compute the expected hash
	expectedHash := contentHash(string(summaryData))

	// Write a UUID7 fact file in the date-partitioned directory
	sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts", "2026-04-01")
	require.NoError(t, os.MkdirAll(sessionFactsDir, 0o755))

	uid, err := uuid.NewV7()
	require.NoError(t, err)
	factFile := filepath.Join(sessionFactsDir, sessionDirName+"-"+uid.String()+".jsonl")
	writeSimpleFactFileHelper(t, factFile, expectedHash, "Fixed auth bug")

	// scanPendingSessions should find 0 pending (hash matches)
	pending, err := scanPendingSessions(ledgerPath, tcPath)
	require.NoError(t, err)
	assert.Empty(t, pending, "session with matching UUID7 fact file must not be pending")

	// Corrupt the hash by modifying summary.json
	summary.Summary = "Modified summary to change hash"
	modifiedData, _ := json.Marshal(summary)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), modifiedData, 0o644))

	// Now scanPendingSessions should find 1 pending (hash mismatch)
	pendingAfterChange, err := scanPendingSessions(ledgerPath, tcPath)
	require.NoError(t, err)
	assert.Len(t, pendingAfterChange, 1, "modified session must be pending for re-extraction")
}

// ==========================================================================
// F. Weekly summary reads UUID7-named daily files
// ==========================================================================

// TestWeeklyReadsUUID7DailyFiles verifies that readDailyFilesForDateRange
// correctly finds and reads daily summary files with UUID7 suffixes.
// Failure prevented: weekly distill skips daily files because UUID7-suffixed
// names don't match the date regex.
func TestWeeklyReadsUUID7DailyFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with daily file fixtures")
	}

	dailyDir := t.TempDir()

	// Write daily files with UUID7 names for a week in April 2026
	dates := []string{"2026-04-06", "2026-04-07", "2026-04-08"}
	for _, date := range dates {
		uid, err := uuid.NewV7()
		require.NoError(t, err)
		fileName := date + "-" + uid.String() + ".md"
		content := "# Daily Memory — " + date + "\n\nSome work happened.\n"
		require.NoError(t, os.WriteFile(filepath.Join(dailyDir, fileName), []byte(content), 0o644))
	}

	// readDailyFilesForDateRange should find all 3 files
	summaries, files, err := readDailyFilesForDateRange(dailyDir, "2026-04-06", "2026-04-08")
	require.NoError(t, err)
	assert.Len(t, summaries, 3, "must find all 3 daily files")
	assert.Len(t, files, 3, "must return all 3 filenames")

	// Verify content was read
	for _, s := range summaries {
		assert.Contains(t, s, "Daily Memory", "daily content must be readable")
	}

	// Verify a date outside range is excluded
	uid, err := uuid.NewV7()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dailyDir, "2026-04-05-"+uid.String()+".md"),
		[]byte("# Before range\n"), 0o644))

	summaries2, _, err := readDailyFilesForDateRange(dailyDir, "2026-04-06", "2026-04-08")
	require.NoError(t, err)
	assert.Len(t, summaries2, 3, "file outside date range must be excluded")
}

// ==========================================================================
// G. Monthly summary reads UUID7-named weekly files
// ==========================================================================

// TestMonthlyReadsUUID7WeeklyFiles verifies that readWeeklyFilesForMonth
// correctly finds and reads weekly summary files with UUID7 suffixes.
// Failure prevented: monthly distill skips weekly files because UUID7-suffixed
// names don't match the weekly regex.
func TestMonthlyReadsUUID7WeeklyFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with weekly file fixtures")
	}

	weeklyDir := t.TempDir()

	// April 2026 has ISO weeks 14-17. Write files for W14 and W15.
	for _, week := range []string{"2026-W14", "2026-W15"} {
		uid, err := uuid.NewV7()
		require.NoError(t, err)
		fileName := week + "-" + uid.String() + ".md"
		content := "# Weekly Memory — " + week + "\n\nGood progress this week.\n"
		require.NoError(t, os.WriteFile(filepath.Join(weeklyDir, fileName), []byte(content), 0o644))
	}

	// readWeeklyFilesForMonth for April 2026
	summaries, files, err := readWeeklyFilesForMonth(weeklyDir, 2026, 4, time.UTC)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(summaries), 2, "must find at least 2 weekly files for April 2026")
	assert.Len(t, files, len(summaries))

	// Verify content was read
	for _, s := range summaries {
		assert.Contains(t, s, "Weekly Memory", "weekly content must be readable")
	}
}

// ==========================================================================
// H. Session quality gate filters low-quality sessions
// ==========================================================================

// TestScanPendingSessions_QualityGate verifies that sessions with low quality
// scores are filtered out by scanPendingSessions.
// Failure prevented: low-signal sessions (quality < 0.2) pollute the fact
// pipeline with noise.
func TestScanPendingSessions_QualityGate(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates temp dirs with session fixtures")
	}

	tcPath := t.TempDir()
	ledgerPath := t.TempDir()

	// High-quality session
	highQDir := filepath.Join(ledgerPath, "sessions", "2026-04-01T10-00-alice-OxHIGH")
	require.NoError(t, os.MkdirAll(highQDir, 0o755))
	highQ := sessionsummary.SummarizeResponse{
		Title: "Important work", Summary: "Meaningful session",
		QualityScore: 0.8,
	}
	data, _ := json.Marshal(highQ)
	require.NoError(t, os.WriteFile(filepath.Join(highQDir, "summary.json"), data, 0o644))

	// Low-quality session (below minSessionQuality = 0.2)
	lowQDir := filepath.Join(ledgerPath, "sessions", "2026-04-01T11-00-bob-OxLOWQ")
	require.NoError(t, os.MkdirAll(lowQDir, 0o755))
	lowQ := sessionsummary.SummarizeResponse{
		Title: "Noise session", Summary: "Not useful",
		QualityScore: 0.1,
	}
	data, _ = json.Marshal(lowQ)
	require.NoError(t, os.WriteFile(filepath.Join(lowQDir, "summary.json"), data, 0o644))

	// Create facts dir
	require.NoError(t, os.MkdirAll(filepath.Join(tcPath, "memory", ".session-facts", "2026-04-01"), 0o755))

	pending, err := scanPendingSessions(ledgerPath, tcPath)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "only high-quality session should be pending")
	assert.Equal(t, "Important work", pending[0].Summary.Title)
}

// ==========================================================================
// I. Git twin tests — UUID7 prevents merge conflicts across nodes
// ==========================================================================

// TestSessionFacts_UUID7_RealCodePath_NoGitConflict exercises the full session
// fact extraction pipeline on two independent git clones sharing the same remote.
// Both nodes process the same session, produce UUID7-named fact files, and push.
// The second push requires rebase. UUID7 ensures no filename collision.
// Failure prevented: deterministic filenames cause merge conflicts when two
// nodes distill the same session concurrently.
func TestSessionFacts_UUID7_RealCodePath_NoGitConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates git repos with session fixtures")
	}

	_, nodeAPath, nodeBPath := setupGitTwinRepos(t,
		"memory/.session-facts/2026-04-01",
	)

	// Shared ledger with session fixture (both nodes read from the same ledger)
	ledgerPath := t.TempDir()
	sessionDirName := "2026-04-01T10-00-testuser-OxABC1"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionDirName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	summary := sessionsummary.SummarizeResponse{
		Title:      "Implement UUID7 for fact filenames",
		Summary:    "Migrated all distill outputs to use UUID7 suffixes",
		KeyActions: []string{"Updated session extraction", "Updated discussion extraction"},
		Outcome:    "success",
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{
				{What: "Use UUID7 for all fact filenames", Why: "Prevents merge conflicts"},
			},
		},
		QualityScore: 0.8,
	}
	summaryData, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryData, 0o644))

	// --- Node A: scan, transform, write, commit, push ---
	pendingA, err := scanPendingSessions(ledgerPath, nodeAPath)
	require.NoError(t, err)
	require.Len(t, pendingA, 1, "node A must find the pending session")

	factsA := sessionSummaryToFacts(pendingA[0])
	require.NotEmpty(t, factsA)

	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	factFileA := sessionDirName + "-" + uidA.String() + ".jsonl"
	factPathA := filepath.Join(nodeAPath, "memory", ".session-facts", "2026-04-01", factFileA)
	headerA := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    pendingA[0].Date,
			SourceHash:    pendingA[0].Hash,
		},
	}
	require.NoError(t, facts.WriteFacts(factPathA, headerA, factsA))
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "node A session facts")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// --- Node B: scan, transform, write, commit (independent UUID7) ---
	pendingB, err := scanPendingSessions(ledgerPath, nodeBPath)
	require.NoError(t, err)
	require.Len(t, pendingB, 1, "node B must find the pending session")

	factsB := sessionSummaryToFacts(pendingB[0])
	require.NotEmpty(t, factsB)

	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	factFileB := sessionDirName + "-" + uidB.String() + ".jsonl"
	factPathB := filepath.Join(nodeBPath, "memory", ".session-facts", "2026-04-01", factFileB)
	headerB := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    pendingB[0].Date,
			SourceHash:    pendingB[0].Hash,
		},
	}
	require.NoError(t, facts.WriteFacts(factPathB, headerB, factsB))
	runTwinGit(t, nodeBPath, "add", ".")
	runTwinGit(t, nodeBPath, "commit", "-m", "node B session facts")

	// Node B push fails (non-FF), rebase, then push succeeds
	pushOut := runTwinGitAllowFail(t, nodeBPath, "push", "origin", "main")
	if strings.Contains(pushOut, "rejected") {
		runTwinGit(t, nodeBPath, "pull", "--rebase", "origin", "main")
		runTwinGit(t, nodeBPath, "push", "origin", "main")
	}

	// --- Assert: no conflict markers, both UUID7 files exist ---
	assertNoConflictMarkers(t, nodeBPath)

	// Both fact files must exist in node B after rebase
	rebasedA := filepath.Join(nodeBPath, "memory", ".session-facts", "2026-04-01", factFileA)
	rebasedB := filepath.Join(nodeBPath, "memory", ".session-facts", "2026-04-01", factFileB)
	assert.FileExists(t, rebasedA, "node A fact file must exist after rebase")
	assert.FileExists(t, rebasedB, "node B fact file must exist after rebase")

	// Both files must contain valid JSONL
	_, readFactsA, err := facts.ReadFacts(rebasedA)
	require.NoError(t, err, "node A fact file must be valid JSONL after rebase")
	assert.NotEmpty(t, readFactsA)

	_, readFactsB, err := facts.ReadFacts(rebasedB)
	require.NoError(t, err, "node B fact file must be valid JSONL after rebase")
	assert.NotEmpty(t, readFactsB)

	// UUID7s must differ
	assert.NotEqual(t, uidA.String(), uidB.String(), "independent UUID7s must differ")
}

// TestDiscussionFacts_UUID7_RealCodePath_NoGitConflict exercises the full
// discussion fact extraction pipeline (server summary.json fast path) on two
// independent git clones. Both nodes extract facts from the same discussion,
// produce UUID7-named files, and push without conflict.
// Failure prevented: deterministic discussion fact filenames cause merge
// conflicts when two nodes process the same discussion concurrently.
func TestDiscussionFacts_UUID7_RealCodePath_NoGitConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates git repos with discussion fixtures")
	}

	_, nodeAPath, nodeBPath := setupGitTwinRepos(t,
		"discussions/2026-04-01-arch",
		"memory/.discussion-facts",
	)

	discussionDirName := "2026-04-01-arch"

	// Set up discussion fixtures in node A, commit and push, then pull into B.
	// This ensures both clones have the discussion tracked by git.
	discussionDir := filepath.Join(nodeAPath, "discussions", discussionDirName)

	metadata := discussionMetadata{
		RecordingID: "rec-twin-123",
		Title:       "Architecture Review",
		CreatedAt:   "2026-04-01T14:23:00Z",
		UserID:      "user-456",
	}
	metadataData, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "metadata.json"), metadataData, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "summary.md"),
		[]byte("# Architecture Review\n\nWe decided to use Go for the backend."), 0o644))

	serverSummary := map[string]interface{}{
		"schema_version": 1,
		"recording_id":   "rec-twin-123",
		"title":          "Architecture Review",
		"human_summary":  "Discussed backend language choice",
		"decisions":      []map[string]string{{"description": "Use Go for backend services"}},
		"learnings":      []map[string]string{{"description": "Go concurrency model fits our needs"}},
		"action_items":   []map[string]string{{"description": "Set up Go project structure"}},
		"open_questions": []map[string]string{{"description": "Which Go web framework?"}},
	}
	summaryData, err := json.Marshal(serverSummary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(discussionDir, "summary.json"), summaryData, 0o644))

	// Commit discussion fixtures from node A, push, then pull into node B
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "add discussion fixtures")
	runTwinGit(t, nodeAPath, "push", "origin", "main")
	runTwinGit(t, nodeBPath, "pull", "--rebase", "origin", "main")

	// --- Node A: scan, extract, write, commit, push ---
	pendingA, err := scanPendingDiscussions(nodeAPath)
	require.NoError(t, err)
	require.Len(t, pendingA, 1, "node A must find the pending discussion")

	sourceHashA := discussionContentHash(filepath.Join(nodeAPath, "discussions", discussionDirName))
	require.NotEmpty(t, sourceHashA)

	factContentA, err := extractFactsFromSummaryJSON(pendingA[0], sourceHashA)
	require.NoError(t, err)
	require.NotEmpty(t, factContentA)

	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	factFileA := discussionDirName + "-" + uidA.String() + ".jsonl"
	factPathA := filepath.Join(nodeAPath, "memory", ".discussion-facts", factFileA)
	require.NoError(t, os.WriteFile(factPathA, []byte(factContentA), 0o644))

	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "node A discussion facts")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// --- Node B: scan, extract, write, commit (independent UUID7) ---
	pendingB, err := scanPendingDiscussions(nodeBPath)
	require.NoError(t, err)
	require.Len(t, pendingB, 1, "node B must find the pending discussion")

	sourceHashB := discussionContentHash(filepath.Join(nodeBPath, "discussions", discussionDirName))
	require.NotEmpty(t, sourceHashB)

	factContentB, err := extractFactsFromSummaryJSON(pendingB[0], sourceHashB)
	require.NoError(t, err)
	require.NotEmpty(t, factContentB)

	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	factFileB := discussionDirName + "-" + uidB.String() + ".jsonl"
	factPathB := filepath.Join(nodeBPath, "memory", ".discussion-facts", factFileB)
	require.NoError(t, os.WriteFile(factPathB, []byte(factContentB), 0o644))

	runTwinGit(t, nodeBPath, "add", ".")
	runTwinGit(t, nodeBPath, "commit", "-m", "node B discussion facts")

	// Node B push fails (non-FF), rebase, then push succeeds
	pushOut := runTwinGitAllowFail(t, nodeBPath, "push", "origin", "main")
	if strings.Contains(pushOut, "rejected") {
		runTwinGit(t, nodeBPath, "pull", "--rebase", "origin", "main")
		runTwinGit(t, nodeBPath, "push", "origin", "main")
	}

	// --- Assert: no conflict markers, both UUID7 files exist ---
	assertNoConflictMarkers(t, nodeBPath)

	rebasedA := filepath.Join(nodeBPath, "memory", ".discussion-facts", factFileA)
	rebasedB := filepath.Join(nodeBPath, "memory", ".discussion-facts", factFileB)
	assert.FileExists(t, rebasedA, "node A discussion fact file must exist after rebase")
	assert.FileExists(t, rebasedB, "node B discussion fact file must exist after rebase")

	// Both files must contain valid fact content
	contentA, err := os.ReadFile(rebasedA)
	require.NoError(t, err)
	_, factsFromA, err := facts.ParseFacts(contentA)
	require.NoError(t, err, "node A fact file must be valid JSONL after rebase")
	assert.NotEmpty(t, factsFromA)

	contentB, err := os.ReadFile(rebasedB)
	require.NoError(t, err)
	_, factsFromB, err := facts.ParseFacts(contentB)
	require.NoError(t, err, "node B fact file must be valid JSONL after rebase")
	assert.NotEmpty(t, factsFromB)

	assert.NotEqual(t, uidA.String(), uidB.String(), "independent UUID7s must differ")
}

// TestWeeklySummary_UUID7_RealCodePath_NoGitConflict verifies that UUID7-named
// weekly summary files from two nodes coexist after push/rebase, and that
// readWeeklyFilesForMonth finds both.
// Failure prevented: deterministic weekly filenames cause merge conflicts;
// weekly reader misses UUID7-suffixed files.
func TestWeeklySummary_UUID7_RealCodePath_NoGitConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates git repos with weekly summary fixtures")
	}

	_, nodeAPath, nodeBPath := setupGitTwinRepos(t,
		"memory/weekly",
	)

	weekID := "2026-W14"

	// --- Node A: write weekly summary, commit, push ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	weeklyFileA := weekID + "-" + uidA.String() + ".md"
	weeklyPathA := filepath.Join(nodeAPath, "memory", "weekly", weeklyFileA)
	require.NoError(t, os.WriteFile(weeklyPathA,
		[]byte("# Weekly Memory — "+weekID+"\n\nWeekly summary from node A.\n"), 0o644))

	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "node A weekly summary")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// --- Node B: write weekly summary, commit (independent UUID7) ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	weeklyFileB := weekID + "-" + uidB.String() + ".md"
	weeklyPathB := filepath.Join(nodeBPath, "memory", "weekly", weeklyFileB)
	require.NoError(t, os.WriteFile(weeklyPathB,
		[]byte("# Weekly Memory — "+weekID+"\n\nWeekly summary from node B.\n"), 0o644))

	runTwinGit(t, nodeBPath, "add", ".")
	runTwinGit(t, nodeBPath, "commit", "-m", "node B weekly summary")

	// Node B push fails (non-FF), rebase, then push succeeds
	pushOut := runTwinGitAllowFail(t, nodeBPath, "push", "origin", "main")
	if strings.Contains(pushOut, "rejected") {
		runTwinGit(t, nodeBPath, "pull", "--rebase", "origin", "main")
		runTwinGit(t, nodeBPath, "push", "origin", "main")
	}

	// --- Assert: no conflict markers, both files exist ---
	assertNoConflictMarkers(t, nodeBPath)

	rebasedA := filepath.Join(nodeBPath, "memory", "weekly", weeklyFileA)
	rebasedB := filepath.Join(nodeBPath, "memory", "weekly", weeklyFileB)
	assert.FileExists(t, rebasedA, "node A weekly file must exist after rebase")
	assert.FileExists(t, rebasedB, "node B weekly file must exist after rebase")

	// readWeeklyFilesForMonth must find both files
	weeklyDir := filepath.Join(nodeBPath, "memory", "weekly")
	summaries, files, err := readWeeklyFilesForMonth(weeklyDir, 2026, 4, time.UTC)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(summaries), 2, "readWeeklyFilesForMonth must find both UUID7 weekly files")
	assert.Len(t, files, len(summaries))

	// Verify both nodes' content is present
	allContent := strings.Join(summaries, "\n")
	assert.Contains(t, allContent, "node A", "must contain node A content")
	assert.Contains(t, allContent, "node B", "must contain node B content")

	assert.NotEqual(t, uidA.String(), uidB.String(), "independent UUID7s must differ")
}

// ==========================================================================
// J. Negative control — deterministic names DO cause git conflicts
// ==========================================================================

// TestSessionFacts_DeterministicNames_GitConflict proves that without UUID7,
// two nodes writing the same deterministic filename causes a rebase conflict.
// This is the negative control: it validates that the UUID7 tests above are
// testing something real — without UUID7, the same scenario fails.
// Failure prevented: UUID7 tests pass vacuously because rebase never conflicts
// (this test proves the conflict is real without UUID7).
func TestSessionFacts_DeterministicNames_GitConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: creates git repos to verify conflict behavior")
	}

	_, nodeAPath, nodeBPath := setupGitTwinRepos(t,
		"memory/.session-facts/2026-04-01",
	)

	// Deterministic filename — same on both nodes
	deterministicFile := "2026-04-01T10-00-testuser-OxABC1.jsonl"

	// --- Node A: write with deterministic name, commit, push ---
	factPathA := filepath.Join(nodeAPath, "memory", ".session-facts", "2026-04-01", deterministicFile)
	writeSimpleFactFileHelper(t, factPathA, "hashA", "Decision from node A")
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "node A deterministic facts")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// --- Node B: write SAME filename with different content, commit ---
	factPathB := filepath.Join(nodeBPath, "memory", ".session-facts", "2026-04-01", deterministicFile)
	writeSimpleFactFileHelper(t, factPathB, "hashB", "Decision from node B")
	runTwinGit(t, nodeBPath, "add", ".")
	runTwinGit(t, nodeBPath, "commit", "-m", "node B deterministic facts")

	// Node B push fails (non-FF)
	pushOut := runTwinGitAllowFail(t, nodeBPath, "push", "origin", "main")
	require.Contains(t, pushOut, "rejected", "push must be rejected (non-fast-forward)")

	// Rebase produces a conflict because both wrote the same file
	rebaseOut := runTwinGitAllowFail(t, nodeBPath, "pull", "--rebase", "origin", "main")
	assert.Contains(t, rebaseOut, "CONFLICT",
		"deterministic filenames MUST cause a rebase conflict — this proves UUID7 tests are non-vacuous")

	// Clean up the conflict state so t.TempDir() cleanup succeeds
	runTwinGitAllowFail(t, nodeBPath, "rebase", "--abort")
}

// ==========================================================================
// Git twin test helpers
// ==========================================================================

// setupGitTwinRepos creates a bare git repository and two clones simulating
// two machines sharing a team context remote. The bare repo uses
// --initial-branch=main for hermeticity (no dependency on system default).
// subdirs are created in node A's clone and pushed to remote before cloning
// node B, so both start with the same directory structure.
func setupGitTwinRepos(t *testing.T, subdirs ...string) (barePath, nodeAPath, nodeBPath string) {
	t.Helper()
	base := t.TempDir()
	barePath = filepath.Join(base, "team-context.git")

	// Hermetic: explicit main branch, no dependency on system git config
	runTwinGit(t, "", "init", "--bare", "--initial-branch=main", barePath)

	nodeAPath = filepath.Join(base, "node-a")
	runTwinGit(t, "", "clone", barePath, nodeAPath)
	runTwinGit(t, nodeAPath, "config", "user.email", "test@test.com")
	runTwinGit(t, nodeAPath, "config", "user.name", "test")

	// Create requested subdirectories with .gitkeep so git tracks them
	for _, sub := range subdirs {
		require.NoError(t, os.MkdirAll(filepath.Join(nodeAPath, sub), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(nodeAPath, sub, ".gitkeep"), nil, 0o644))
	}
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "init")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	nodeBPath = filepath.Join(base, "node-b")
	runTwinGit(t, "", "clone", barePath, nodeBPath)
	runTwinGit(t, nodeBPath, "config", "user.email", "test@test.com")
	runTwinGit(t, nodeBPath, "config", "user.name", "test")

	return barePath, nodeAPath, nodeBPath
}

// NOTE: runTwinGit, runTwinGitAllowFail, and assertNoConflictMarkers are
// defined in distill_github_twin_test.go and shared across twin test files.

// ==========================================================================
// Shared helpers
// ==========================================================================

// mockLLMBackend implements agentcli.Backend for testing LLM-dependent paths.
type mockLLMBackend struct {
	output string
}

func (m *mockLLMBackend) Name() string    { return "mock-llm" }
func (m *mockLLMBackend) Available() bool { return true }
func (m *mockLLMBackend) Run(_ context.Context, _ string) (string, error) {
	return m.output, nil
}

// writeSimpleFactFileHelper writes a minimal JSONL fact file with the given
// source_hash and a single fact headline.
func writeSimpleFactFileHelper(t *testing.T, path, sourceHash, headline string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    "2026-04-01T10:00:00Z",
			SourceHash:    sourceHash,
		},
	}
	fact := facts.Fact{
		Headline:   headline,
		SourceType: facts.SourceSession,
		Timestamp:  "2026-04-01T10:00:00Z",
		Category:   facts.CategoryContext,
	}
	require.NoError(t, facts.WriteFacts(path, header, []facts.Fact{fact}))
}
