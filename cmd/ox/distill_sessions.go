package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

// sessionNameRe parses session directory names.
// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Captures: date (group 1), username (group 2).
var sessionNameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T\d{2}-\d{2}-(.+)-\w+$`)

// minSessionQuality is the minimum quality score for a session to be included
// in fact extraction. Sessions scoring below this are considered too low-signal.
// A zero score means the LLM didn't provide one (backward compat) — include those.
const minSessionQuality = 0.2

// sessionInput holds parsed data for a single session directory.
type sessionInput struct {
	DirName   string                            // e.g., "2026-01-06T14-32-ryan-Ox7f3a"
	Date      string                            // "2026-01-06" (extracted from dir name)
	Who       string                            // username extracted from dir name
	Hash      string                            // content hash from scan, avoids re-reading summary.json
	StartedAt time.Time                         // from raw.jsonl _meta.started_at; zero if unavailable
	Summary   *sessionsummary.SummarizeResponse // parsed summary.json
}

// extractSessionFacts scans the given ledger for sessions with summary.json,
// transforms structured session data into facts, and writes JSONL files
// to memory/.session-facts/<date>/ in the team context.
//
// repoID identifies the repo for per-repo state tracking.
// ledgerPath is the path to the ledger directory containing sessions/.
//
// No LLM calls — pure data transformation from structured summary.json.
func extractSessionFacts(cmd *cobra.Command, tc *config.TeamContext, repoID, ledgerPath string) error {
	if ledgerPath == "" {
		slog.Debug("no ledger path, skipping session fact extraction", "repo", repoID)
		return nil
	}
	if _, err := os.Stat(filepath.Join(ledgerPath, "sessions")); err != nil {
		slog.Debug("no sessions directory in ledger, skipping", "repo", repoID, "path", ledgerPath)
		return nil
	}

	pending, err := scanPendingSessions(ledgerPath, tc.Path)
	if err != nil {
		return fmt.Errorf("scan sessions: %w", err)
	}

	if len(pending) == 0 {
		return nil
	}

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending sessions: %d\n", len(pending))
		for _, s := range pending {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", s.DirName, s.Summary.Title)
		}
		return nil
	}

	for _, s := range pending {
		// Use actual UTC StartedAt when available; fall back to date-only string
		// so parseFactDate uses the directory date as-is (no false UTC instant).
		recordedAt := s.Date
		if !s.StartedAt.IsZero() {
			recordedAt = s.StartedAt.UTC().Format(time.RFC3339)
		}

		extractedFacts := sessionSummaryToFacts(s)
		if len(extractedFacts) == 0 {
			slog.Debug("no facts extracted from session", "session", s.DirName)
			// write empty marker so scanPendingSessions skips this session
			markerUID, _ := uuid.NewV7() // error only if crypto/rand fails — zero UUID collision is acceptable
			markerFile := filepath.Join("memory", ".session-facts", s.Date, s.DirName+"-"+markerUID.String()+".jsonl")
			markerHeader := facts.FileHeader{
				Meta: facts.FileMeta{
					SchemaVersion: facts.SchemaVersion,
					SourceType:    facts.SourceSession,
					RecordedAt:    recordedAt,
					SourceHash:    s.Hash,
				},
			}
			if err := facts.WriteFacts(filepath.Join(tc.Path, markerFile), markerHeader, nil); err != nil {
				slog.Warn("failed to write empty session fact marker", "session", s.DirName, "error", err)
				continue
			}
			if err := commitMemoryFile(tc.Path, markerFile, fmt.Sprintf("memory: mark session %s (no facts)", s.DirName)); err != nil {
				slog.Warn("failed to commit session fact marker", "session", s.DirName, "error", err)
				if removeErr := os.Remove(filepath.Join(tc.Path, markerFile)); removeErr != nil && !os.IsNotExist(removeErr) {
					slog.Warn("failed to clean up uncommitted session fact marker", "session", s.DirName, "error", removeErr)
				}
			}
			continue
		}

		header := facts.FileHeader{
			Meta: facts.FileMeta{
				SchemaVersion: facts.SchemaVersion,
				SourceType:    facts.SourceSession,
				RecordedAt:    recordedAt,
				SourceHash:    s.Hash,
			},
		}

		uid, _ := uuid.NewV7() // error only if crypto/rand fails — zero UUID collision is acceptable
		factFile := filepath.Join("memory", ".session-facts", s.Date, s.DirName+"-"+uid.String()+".jsonl")
		fullPath := filepath.Join(tc.Path, factFile)

		if err := facts.WriteFacts(fullPath, header, extractedFacts); err != nil {
			slog.Warn("failed to write session facts", "session", s.DirName, "error", err)
			continue
		}

		if err := commitMemoryFile(tc.Path, factFile, fmt.Sprintf("memory: extract facts from session %s", s.DirName)); err != nil {
			slog.Warn("failed to commit session facts", "session", s.DirName, "error", err)
			if removeErr := os.Remove(fullPath); removeErr != nil && !os.IsNotExist(removeErr) {
				slog.Warn("failed to clean up uncommitted session facts", "session", s.DirName, "error", removeErr)
			}
			continue
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Extracted %d facts from session: %s\n", len(extractedFacts), s.Summary.Title)
	}

	return nil
}

// readSessionStartedAt reads the first line of raw.jsonl in a session directory
// and parses the _meta.started_at field as an RFC3339 timestamp.
// Returns zero time if raw.jsonl is missing or _meta is unparseable.
func readSessionStartedAt(sessionDir string) time.Time {
	f, err := os.Open(filepath.Join(sessionDir, "raw.jsonl"))
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return time.Time{}
	}

	var meta struct {
		Meta struct {
			StartedAt string `json:"started_at"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil || meta.Meta.StartedAt == "" {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, meta.Meta.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// scanPendingSessions reads the sessions/ directory in the ledger and returns
// sessions that need fact extraction. A session is skipped if its fact file
// already exists and the embedded source_hash matches the current summary hash.
// Legacy fact files without source_hash are re-extracted.
func scanPendingSessions(ledgerPath, tcPath string) ([]sessionInput, error) {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var pending []sessionInput
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()

		// parse date and username from session name
		date, who := parseSessionName(dirName)
		if date == "" {
			slog.Debug("skip session dir with unparseable name", "dir", dirName)
			continue
		}

		// check if summary.json exists
		summaryPath := filepath.Join(sessionsDir, dirName, "summary.json")
		summaryData, err := os.ReadFile(summaryPath)
		if err != nil {
			continue // no summary.json, skip
		}

		// derive the final date before dedup check — started_at may differ from dir name
		startedAt := readSessionStartedAt(filepath.Join(sessionsDir, dirName))
		sessionDate := date
		if !startedAt.IsZero() {
			sessionDate = startedAt.UTC().Format("2006-01-02")
		}

		// compute content hash for change detection
		currentHash := contentHash(string(summaryData))

		// check if fact file already exists with matching source_hash (UUID7 glob + legacy fallback)
		sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts", sessionDate)
		existingHash := findLatestFactFileSourceHash(sessionFactsDir, dirName+"-*.jsonl")
		if existingHash == "" {
			existingHash = readFactFileSourceHash(filepath.Join(sessionFactsDir, dirName+".jsonl"))
		}
		if existingHash == currentHash {
			continue // fact file is up to date
		}

		// parse summary.json
		var summary sessionsummary.SummarizeResponse
		if err := json.Unmarshal(summaryData, &summary); err != nil {
			slog.Debug("malformed summary.json, skipping", "session", dirName, "error", err)
			continue
		}

		// quality gate: skip low-quality sessions
		if summary.QualityScore > 0 && summary.QualityScore < minSessionQuality {
			slog.Debug("skip low-quality session", "session", dirName, "score", summary.QualityScore)
			continue
		}

		pending = append(pending, sessionInput{
			DirName:   dirName,
			Date:      sessionDate,
			Who:       who,
			Hash:      currentHash,
			StartedAt: startedAt,
			Summary:   &summary,
		})
	}

	// sort by date (oldest first)
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].DirName < pending[j].DirName
	})

	return pending, nil
}

// parseSessionName extracts the date and username from a session directory name.
// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Returns empty strings if the name doesn't match the expected format.
func parseSessionName(name string) (date, who string) {
	m := sessionNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// sessionContentHash computes a content hash of a session's summary.json.
func sessionContentHash(ledgerPath, dirName string) string {
	data, err := os.ReadFile(filepath.Join(ledgerPath, "sessions", dirName, "summary.json"))
	if err != nil {
		slog.Debug("failed to hash session summary", "session", dirName, "error", err)
		return ""
	}
	return contentHash(string(data))
}

// sessionSummaryToFacts transforms a session summary into uniform facts.
// Pure data transformation — no LLM calls.
func sessionSummaryToFacts(input sessionInput) []facts.Fact {
	var result []facts.Fact
	s := input.Summary
	ts := input.Date
	if !input.StartedAt.IsZero() {
		ts = input.StartedAt.UTC().Format(time.RFC3339)
	}
	ref := "sessions/" + input.DirName

	// session context fact (title + summary)
	if s.Title != "" && s.Summary != "" {
		summary := s.Summary
		if len(s.KeyActions) > 0 {
			summary += " Key actions: " + strings.Join(s.KeyActions, "; ")
		}
		result = append(result, facts.Fact{
			Headline:   s.Title,
			Summary:    summary,
			SourceType: facts.SourceSession,
			SourceRef:  ref,
			Timestamp:  ts,
			Category:   facts.CategoryContext,
			Who:        input.Who,
		})
	}

	if s.AgentSummary != nil {
		// decisions
		for _, d := range s.AgentSummary.Decisions {
			who := d.Owner
			if who == "" {
				who = input.Who
			}
			result = append(result, facts.Fact{
				Headline:   d.What,
				Rationale:  d.Why,
				SourceType: facts.SourceSession,
				SourceRef:  ref,
				Timestamp:  ts,
				Category:   facts.CategoryDecision,
				Who:        who,
			})
		}

		// action items
		for _, a := range s.AgentSummary.ActionItems {
			who := a.Assignee
			if who == "" {
				who = input.Who
			}
			var summary string
			if a.Priority != "" {
				summary = fmt.Sprintf("Priority: %s", a.Priority)
			}
			result = append(result, facts.Fact{
				Headline:   a.Task,
				Summary:    summary,
				SourceType: facts.SourceSession,
				SourceRef:  ref,
				Timestamp:  ts,
				Category:   facts.CategoryActionItem,
				Who:        who,
			})
		}

		// open questions
		for _, q := range s.AgentSummary.OpenQuestions {
			result = append(result, facts.Fact{
				Headline:   q.Question,
				Summary:    q.Context,
				SourceType: facts.SourceSession,
				SourceRef:  ref,
				Timestamp:  ts,
				Category:   facts.CategoryOpenQuestion,
				Who:        input.Who,
			})
		}
	}

	// aha moments — skip "question" type (overlaps with OpenQuestions)
	for _, aha := range s.AhaMoments {
		switch aha.Type {
		case "insight", "breakthrough", "synthesis":
			result = append(result, facts.Fact{
				Headline:   aha.Highlight,
				Summary:    aha.Why,
				SourceType: facts.SourceSession,
				SourceRef:  ref,
				Timestamp:  ts,
				Category:   facts.CategoryLearning,
				Who:        input.Who,
			})
		case "decision":
			result = append(result, facts.Fact{
				Headline:   aha.Highlight,
				Summary:    aha.Why,
				SourceType: facts.SourceSession,
				SourceRef:  ref,
				Timestamp:  ts,
				Category:   facts.CategoryDecision,
				Who:        input.Who,
			})
		}
	}

	return result
}

// readPendingSessionFacts reads fact files from memory/.session-facts/<date>/
// that were written since the given timestamp, grouped by session date.
// Uses file mtime (not directory date) for the since filter so that
// late-arriving sessions (e.g., March 10 session synced on March 27)
// are included even when since is past the session date.
// Returns a map of YYYY-MM-DD → []discussionFactEntry for seamless merge
// with discussion and github facts in distillDaily.
func readPendingSessionFacts(tcPath string, since time.Time, tz ...*time.Location) (map[string][]discussionFactEntry, error) {
	sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts")
	dateDirs, err := os.ReadDir(sessionFactsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session-facts dir: %w", err)
	}

	result := make(map[string][]discussionFactEntry)

	for _, dateDir := range dateDirs {
		if !dateDir.IsDir() {
			continue
		}

		date := dateDir.Name()
		if _, err := time.Parse("2006-01-02", date); err != nil {
			continue
		}

		// read all .jsonl files in this date directory
		datePath := filepath.Join(sessionFactsDir, date)
		files, err := os.ReadDir(datePath)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}

			// filter by file mtime — includes late-arriving sessions
			// whose session date is older than since
			if !since.IsZero() {
				info, err := f.Info()
				if err == nil && info.ModTime().Before(since) {
					continue
				}
			}

			data, err := os.ReadFile(filepath.Join(datePath, f.Name()))
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			// Use parseFactDate to extract the date from content metadata,
			// converting UTC timestamps to the team timezone for day-bucketing.
			// Falls back to directory name if no parseable date in content.
			factDate := parseFactDate(content, f.Name(), tz...)
			if factDate == "" {
				factDate = date // fallback to directory name
			}

			result[factDate] = append(result[factDate], discussionFactEntry{
				Content: content,
				RelPath: filepath.Join("memory", ".session-facts", date, f.Name()),
				Date:    factDate,
			})
		}
	}

	return result, nil
}
