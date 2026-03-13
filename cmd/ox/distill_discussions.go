package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/vtt"
)

// maxTranscriptChars caps transcript text sent to LLM to avoid excessive tokens.
const maxTranscriptChars = 30000

// discussionInput holds parsed data for a single discussion directory.
type discussionInput struct {
	DirName    string // directory name (e.g., "2026-03-10-1423-ryan")
	Title      string
	CreatedAt  time.Time
	Summary    string
	Transcript string // formatted speaker text from VTT, or empty
}

// discussionMetadata matches the metadata.json schema in discussion dirs.
type discussionMetadata struct {
	RecordingID string `json:"recording_id"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"` // RFC3339
	UserID      string `json:"user_id"`
}

// scanPendingDiscussions reads the discussions/ directory in the team context
// and returns discussions not yet tracked in state.ProcessedDiscussions.
// Each discussion dir is expected to contain metadata.json, summary.md, and optionally transcript.vtt.
func scanPendingDiscussions(tcPath string, processed map[string]string) ([]discussionInput, error) {
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

		// skip if already processed with same hash
		if prevHash, ok := processed[dirName]; ok && prevHash == currentHash {
			continue
		}

		createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
		if err != nil {
			slog.Debug("malformed discussion timestamp, using zero time", "dir", dirName, "raw", meta.CreatedAt, "error", err)
		}

		di := discussionInput{
			DirName:    dirName,
			Title:      meta.Title,
			CreatedAt:  createdAt,
			Summary:    loadDiscussionSummary(dirPath),
			Transcript: loadDiscussionTranscript(dirPath),
		}

		pending = append(pending, di)
	}

	// sort by creation time (oldest first)
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	return pending, nil
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

// discussionContentHash computes a hash of a discussion's content files
// for change detection. Includes metadata.json, summary.md, and transcript.vtt.
func discussionContentHash(dirPath string) string {
	var parts []string

	if data, err := os.ReadFile(filepath.Join(dirPath, "metadata.json")); err == nil {
		parts = append(parts, string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dirPath, "summary.md")); err == nil {
		parts = append(parts, string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dirPath, "transcript.vtt")); err == nil {
		parts = append(parts, string(data))
	}

	return contentHash(parts...)
}

// readPendingDiscussionFacts reads fact files from memory/.discussion-facts/
// that were created since the given timestamp.
// Returns file contents (for hashing) and relative paths from the team context root (for prompts).
func readPendingDiscussionFacts(tcPath string, since time.Time) (contents []string, relPaths []string, err error) {
	factsDir := filepath.Join(tcPath, "memory", ".discussion-facts")
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read discussion-facts dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// check modification time
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(factsDir, entry.Name()))
		if err != nil {
			continue
		}
		if content := strings.TrimSpace(string(data)); content != "" {
			contents = append(contents, content)
			relPaths = append(relPaths, filepath.Join("memory", ".discussion-facts", entry.Name()))
		}
	}

	return contents, relPaths, nil
}
