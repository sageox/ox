package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/internal/vtt"
	"github.com/sageox/ox/pkg/discussion"
)

// maxTranscriptChars caps transcript text sent to LLM to avoid excessive tokens.
const maxTranscriptChars = 30000

// highImportanceThreshold is the minimum importance score for a chapter to be
// included in learnings and key context output. Chapters at or below this score
// are considered low-signal and excluded from fact extraction.
const highImportanceThreshold = 0.5

// discussionInput holds parsed data for a single discussion directory.
type discussionInput struct {
	DirName        string // directory name (e.g., "2026-03-10-1423-ryan")
	Title          string
	CreatedAt      time.Time
	Summary        string
	Transcript     string // formatted speaker text from VTT, or empty
	Annotations    string // formatted server annotations, or empty
	SummaryJSONDir string // discussion dir path when server-generated summary.json exists
}

// discussionMetadata matches the metadata.json schema in discussion dirs.
type discussionMetadata struct {
	RecordingID string `json:"recording_id"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"` // RFC3339
	UserID      string `json:"user_id"`
}

// scanPendingDiscussions reads the discussions/ directory in the team context
// and returns discussions that need (re-)extraction based on source_hash in fact files.
// Each discussion dir is expected to contain metadata.json, summary.md, and optionally transcript.vtt.
func scanPendingDiscussions(tcPath string) ([]discussionInput, error) {
	discussionsDir := filepath.Join(tcPath, "discussions")
	entries, err := os.ReadDir(discussionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read discussions dir: %w", err)
	}

	var pending []discussionInput
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		dirPath := filepath.Join(discussionsDir, dirName)

		// parse metadata.json
		meta, err := loadDiscussionMetadata(dirPath)
		if err != nil {
			slog.Debug("skip discussion dir", "dir", dirName, "error", err)
			continue
		}

		// compute content hash for change detection
		currentHash := discussionContentHash(dirPath)

		// check existing fact files for source_hash match (UUID7 glob + legacy fallback)
		factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
		existingHash := findLatestFactFileSourceHash(factsDir, dirName+"-*.jsonl")
		if existingHash == "" {
			existingHash = readFactFileSourceHash(filepath.Join(factsDir, dirName+".jsonl"))
		}

		// skip if fact file exists with matching source_hash;
		// all other cases (missing, legacy .md, stale hash) need extraction
		if existingHash == currentHash && currentHash != "" {
			continue
		}

		createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
		if err != nil {
			slog.Debug("malformed discussion timestamp, using zero time", "dir", dirName, "raw", meta.CreatedAt, "error", err)
		}

		di := discussionInput{
			DirName:     dirName,
			Title:       meta.Title,
			CreatedAt:   createdAt,
			Summary:     loadDiscussionSummary(dirPath),
			Transcript:  loadDiscussionTranscript(dirPath),
			Annotations: loadDiscussionAnnotations(dirPath),
		}

		// if server-generated summary.json exists, record the dir path
		// so fact extraction can skip the LLM and use structured data directly
		summaryJSONFile := filepath.Join(dirPath, "summary.json")
		if _, err := os.Stat(summaryJSONFile); err == nil {
			di.SummaryJSONDir = dirPath
		}

		pending = append(pending, di)
	}

	// sort by creation time (oldest first)
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	return pending, nil
}

// readFactFileSourceHash reads the _meta.source_hash from the first line of a JSONL fact file.
// Returns empty string if the file doesn't exist, can't be parsed, or has no source_hash.
func readFactFileSourceHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return ""
	}
	var header facts.FileHeader
	if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
		return ""
	}
	return header.Meta.SourceHash
}

// loadDiscussionMetadata reads and parses metadata.json from a discussion dir.
func loadDiscussionMetadata(dirPath string) (*discussionMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dirPath, "metadata.json"))
	if err != nil {
		return nil, fmt.Errorf("read metadata.json: %w", err)
	}
	var meta discussionMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata.json: %w", err)
	}
	if meta.Title == "" {
		return nil, fmt.Errorf("metadata.json missing title")
	}
	return &meta, nil
}

// loadDiscussionSummary reads summary.md from a discussion dir.
// Returns empty string if missing.
func loadDiscussionSummary(dirPath string) string {
	data, err := os.ReadFile(filepath.Join(dirPath, "summary.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadDiscussionTranscript parses transcript.vtt and returns formatted speaker text.
// Falls back to empty string if transcript is missing or unparseable.
// Truncates to maxTranscriptChars.
func loadDiscussionTranscript(dirPath string) string {
	data, err := os.ReadFile(filepath.Join(dirPath, "transcript.vtt"))
	if err != nil {
		return ""
	}

	cues, err := vtt.Parse(data)
	if err != nil {
		slog.Debug("failed to parse VTT", "dir", dirPath, "error", err)
		return ""
	}

	text := vtt.FormatAsText(cues)
	if len(text) > maxTranscriptChars {
		text = text[:maxTranscriptChars] + "\n\n[transcript truncated]"
	}
	return text
}

// loadDiscussionAnnotations loads annotations.json and formats as text lines.
// Each annotation becomes "- [type] content". Returns empty string if missing.
func loadDiscussionAnnotations(dirPath string) string {
	af, err := discussion.LoadAnnotations(dirPath)
	if err != nil {
		slog.Debug("failed to load annotations", "dir", dirPath, "error", err)
		return ""
	}
	if af == nil || len(af.Annotations) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, a := range af.Annotations {
		fmt.Fprintf(&sb, "- [%s] %s\n", a.Type, a.Content)
	}
	return strings.TrimSpace(sb.String())
}

// coreDiscussionFiles are the original files used for content hashing before PR #293.
// Used for legacy hash fallback when migrating from the old 3-file hash to the full hash.
var coreDiscussionFiles = []string{"metadata.json", "summary.md", "transcript.vtt"}

// serverDiscussionFiles are server-generated artifacts added to hashing in PR #293.
var serverDiscussionFiles = []string{"keyframes.json", "summary.json", "annotations.json"}

// discussionContentHash computes a hash of a discussion's content files
// for change detection. Includes core files and server-generated artifacts.
func discussionContentHash(dirPath string) string {
	var parts []string
	for _, name := range coreDiscussionFiles {
		if data, err := os.ReadFile(filepath.Join(dirPath, name)); err == nil {
			parts = append(parts, string(data))
		}
	}
	for _, name := range serverDiscussionFiles {
		if data, err := os.ReadFile(filepath.Join(dirPath, name)); err == nil {
			parts = append(parts, string(data))
		}
	}
	return contentHash(parts...)
}

// categorizeAnnotations groups annotations into fact categories aligned with
// DiscussionFactsPrompt output (decisions, learnings, open_questions, action_items).
func categorizeAnnotations(af *discussion.AnnotationsFile) (decisions, learnings, actionItems, openQuestions []string) {
	if af == nil {
		return
	}
	for _, a := range af.Annotations {
		switch a.Type {
		case discussion.AnnotationDecision, discussion.AnnotationConsensus:
			decisions = append(decisions, a.Content)
		case discussion.AnnotationActionItem:
			actionItems = append(actionItems, a.Content)
		case discussion.AnnotationDisagreement, discussion.AnnotationQuestion:
			openQuestions = append(openQuestions, a.Content)
		case discussion.AnnotationInsight, discussion.AnnotationLearning:
			learnings = append(learnings, a.Content)
		}
	}
	return
}

// extractFactsFromSummaryJSON generates JSONL fact output directly from server-generated
// structured data (summary.json), skipping the LLM entirely.
// Pure data transformation — no network calls.
//
// Works with both v1 and v2 server summary formats. The top-level fact categories
// (decisions, learnings, etc.) are identical in both versions. Chapters are only
// present in v2 and contribute to context facts when available.
func extractFactsFromSummaryJSON(d discussionInput, sourceHash string) (string, error) {
	summary, err := discussion.LoadSummary(d.SummaryJSONDir)
	if err != nil {
		return "", fmt.Errorf("load summary.json: %w", err)
	}
	if summary == nil {
		return "", fmt.Errorf("summary.json not found")
	}
	if !summary.HasCategorizedFacts() {
		return "", fmt.Errorf("summary.json has no categorized facts")
	}

	ts := d.CreatedAt.Format(time.RFC3339)
	sourceRef := fmt.Sprintf("discussions/%s", d.DirName)

	var parsedFacts []facts.Fact

	for _, item := range summary.Decisions {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item.Text(),
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryDecision,
		})
	}
	for _, item := range summary.Learnings {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item.Text(),
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryLearning,
		})
	}
	for _, item := range summary.ActionItems {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item.Text(),
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryActionItem,
		})
	}
	for _, item := range summary.OpenQuestions {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item.Text(),
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryOpenQuestion,
		})
	}
	for _, item := range summary.Requirements {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item.Text(),
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryContext,
		})
	}

	// key context from TechnicalContext, Constraints, NonGoals, and (v2 only) high-importance chapters
	var keyContext []string
	keyContext = append(keyContext, summary.TechnicalContext.Technologies...)
	keyContext = append(keyContext, summary.TechnicalContext.Architecture...)
	keyContext = append(keyContext, summary.TechnicalContext.Integrations...)
	keyContext = append(keyContext, summary.TechnicalContext.Notes...)
	keyContext = append(keyContext, summary.Constraints...)
	keyContext = append(keyContext, summary.NonGoals...)
	if summary.SchemaVersion >= 2 {
		for _, ch := range summary.Chapters {
			if ch.Importance > highImportanceThreshold && ch.Summary != "" {
				keyContext = append(keyContext, ch.Summary)
			}
		}
	}
	for _, item := range keyContext {
		parsedFacts = append(parsedFacts, facts.Fact{
			Headline:   item,
			SourceType: facts.SourceDiscussion,
			SourceRef:  sourceRef,
			Timestamp:  ts,
			Category:   facts.CategoryContext,
		})
	}

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceDiscussion,
			RecordedAt:    ts,
			SourceHash:    sourceHash,
		},
	}

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}

	var sb strings.Builder
	sb.Write(headerBytes)
	sb.WriteByte('\n')
	for _, f := range parsedFacts {
		line, _ := json.Marshal(f)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// discussionFactEntry represents a single discussion fact file with its parsed date.
type discussionFactEntry struct {
	Content string
	RelPath string
	Date    string // YYYY-MM-DD
}

// factFooterDateRe matches the "(created YYYY-MM-DD)" footer in discussion fact files.
var factFooterDateRe = regexp.MustCompile(`\(created (\d{4}-\d{2}-\d{2})\)`)

// factFilenameDateRe matches YYYY-MM-DD prefix in discussion fact filenames / dirnames.
var factFilenameDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)

// readPendingDiscussionFacts reads fact files from memory/.discussion-facts/
// that were created since the given timestamp, grouped by parsed date.
// Dates are parsed from content (footer) or filename, not filesystem mtime.
// If tz is non-nil, RFC3339 timestamps are converted to that timezone for date grouping.
// Returns a map of YYYY-MM-DD → []discussionFactEntry.
func readPendingDiscussionFacts(tcPath string, since time.Time, tz ...*time.Location) (map[string][]discussionFactEntry, error) {
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")

	// compute cutoff date in the same timezone used by parseFactDate
	var cutoffDate string
	if !since.IsZero() {
		cutoff := since
		if len(tz) > 0 && tz[0] != nil {
			cutoff = since.In(tz[0])
		}
		cutoffDate = cutoff.Format("2006-01-02")
	}

	entries, err := os.ReadDir(factsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read discussion-facts dir: %w", err)
	}

	result := make(map[string][]discussionFactEntry)

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".jsonl")) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(factsDir, entry.Name()))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		// parse date from footer first, then fallback to filename
		date := parseFactDate(content, entry.Name(), tz...)
		if date == "" {
			continue
		}

		// filter by since (using same timezone as parseFactDate)
		if cutoffDate != "" && date < cutoffDate {
			continue
		}

		result[date] = append(result[date], discussionFactEntry{
			Content: content,
			RelPath: filepath.Join("memory", ".discussion-facts", entry.Name()),
			Date:    date,
		})
	}

	return result, nil
}

// parseFactDate extracts a YYYY-MM-DD date from fact file content.
// Tries (in order): JSONL _meta header, markdown footer, filename prefix.
// If tz is non-nil, RFC3339 timestamps are converted to that timezone before extracting the date.
func parseFactDate(content, filename string, tz ...*time.Location) string {
	var loc *time.Location
	if len(tz) > 0 {
		loc = tz[0]
	}
	// try JSONL _meta header: {"_meta":{"recorded_at":"2026-03-10T14:23:00Z",...}}
	if firstLine, _, ok := strings.Cut(content, "\n"); ok || content != "" {
		if !ok {
			firstLine = content
		}
		firstLine = strings.TrimSpace(firstLine)
		var header facts.FileHeader
		if err := json.Unmarshal([]byte(firstLine), &header); err == nil && header.Meta.RecordedAt != "" {
			if t, err := time.Parse(time.RFC3339, header.Meta.RecordedAt); err == nil && t.Year() > 1 {
				if loc != nil {
					return t.In(loc).Format("2006-01-02")
				}
				return t.Format("2006-01-02")
			}
		}
	}
	// try footer: "(created 2026-03-10)"
	if m := factFooterDateRe.FindStringSubmatch(content); m != nil {
		if t, err := time.Parse("2006-01-02", m[1]); err == nil && t.Year() > 1 {
			return m[1]
		}
	}
	// fallback: filename prefix "2026-03-10-1423-ryan.md" -> "2026-03-10"
	if m := factFilenameDateRe.FindStringSubmatch(filename); m != nil {
		if t, err := time.Parse("2006-01-02", m[1]); err == nil && t.Year() > 1 {
			return m[1]
		}
	}
	return ""
}

// DiscussionIndexEntry holds data for one line in the per-discussion index.
type DiscussionIndexEntry struct {
	DirName     string
	Title       string
	Date        string   // YYYY-MM-DD
	VisualTypes []string // content types from keyframes
	HasSummary  bool     // server-generated summary.json exists
}

// BuildDiscussionIndex scans the discussions/ directory and returns entries
// for generating a per-discussion index file. Each entry preserves individual
// discussion identity with visual content tags for agent drill-down.
func BuildDiscussionIndex(tcPath string) ([]DiscussionIndexEntry, error) {
	discussionsDir := filepath.Join(tcPath, "discussions")
	entries, err := os.ReadDir(discussionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read discussions dir: %w", err)
	}

	var result []DiscussionIndexEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		dirPath := filepath.Join(discussionsDir, dirName)

		meta, err := loadDiscussionMetadata(dirPath)
		if err != nil {
			continue
		}

		date := ""
		if m := factFilenameDateRe.FindStringSubmatch(dirName); m != nil {
			date = m[1]
		} else if meta.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil {
				date = t.Format("2006-01-02")
			}
		}

		ie := DiscussionIndexEntry{
			DirName:    dirName,
			Title:      meta.Title,
			Date:       date,
			HasSummary: fileExists(filepath.Join(dirPath, "summary.json")),
		}

		// detect visual content types from keyframes.json
		if kf, err := discussion.LoadKeyframes(dirPath); err == nil && kf != nil {
			ie.VisualTypes = discussion.AllVisualTypes(kf)
		}

		result = append(result, ie)
	}

	// sort newest first
	sort.Slice(result, func(i, j int) bool {
		return result[i].DirName > result[j].DirName
	})

	return result, nil
}

// FormatDiscussionIndex renders discussion index entries as markdown lines.
// Format: "YYYY-MM-DD Title [visual-tags] -- discussions/dirname/"
func FormatDiscussionIndex(entries []DiscussionIndexEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		if e.Date != "" {
			sb.WriteString(e.Date)
			sb.WriteString(" ")
		}
		sb.WriteString(e.Title)
		if len(e.VisualTypes) > 0 {
			sb.WriteString(" [")
			sb.WriteString(strings.Join(e.VisualTypes, ", "))
			sb.WriteString("]")
		}
		sb.WriteString(" — discussions/")
		sb.WriteString(e.DirName)
		sb.WriteString("/\n")
	}
	return sb.String()
}
