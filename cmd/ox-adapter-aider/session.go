// session.go handles session reading, parsing, discovery, and types for Aider.
//
// Aider chat history format (.aider.chat.history.md):
//
//	# aider chat started at 2024-01-15 14:30:45   <- session header
//	#### user message here                         <- user (H4 prefix)
//	assistant response here                        <- assistant (plain text)
//	> tool output here                             <- tool (blockquote prefix)
//
// See: https://aider.chat/docs/config/options.html#history-files
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const aiderHistoryFile = ".aider.chat.history.md"

// aider session header format: # aider chat started at 2024-01-15 14:30:45
const aiderTimestampLayout = "2006-01-02 15:04:05"
const aiderSessionPrefix = "# aider chat started at "

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readAiderFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadResult{Entries: entries}, nil
}

func handleReadMetadata(_ adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	// aider chat history has no embedded metadata (model, version)
	return &adapterprotocol.ReadMetadataResult{}, nil
}

func readAiderFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	return parseAiderMarkdown(bufio.NewScanner(f), time.Time{})
}

func readAiderFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return readAiderFromOffsetWithTS(path, offset, time.Time{})
}

func readAiderFromOffsetWithTS(path string, offset int64, initialTS time.Time) ([]adapterprotocol.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, fmt.Errorf("failed to seek: %w", err)
		}
	}

	entries, err := parseAiderMarkdown(bufio.NewScanner(f), initialTS)
	if err != nil {
		return nil, offset, err
	}

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

// parseAiderMarkdown parses aider's markdown chat history format.
// Role transitions flush the accumulated buffer as an entry.
func parseAiderMarkdown(scanner *bufio.Scanner, initialTS time.Time) ([]adapterprotocol.RawEntry, error) {
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []adapterprotocol.RawEntry
	var currentRole string
	var currentLines []string
	sessionTS := initialTS

	flush := func() {
		if currentRole == "" || len(currentLines) == 0 {
			currentRole = ""
			currentLines = nil
			return
		}
		content := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if content == "" {
			currentRole = ""
			currentLines = nil
			return
		}

		var e adapterprotocol.RawEntry
		switch currentRole {
		case "user":
			e = adapterruntime.UserEntry(sessionTS, content)
		case "assistant":
			e = adapterruntime.AssistantEntry(sessionTS, content)
		case "tool":
			e = adapterruntime.ToolResultEntry(sessionTS, content, false)
		}
		entries = append(entries, e)
		currentRole = ""
		currentLines = nil
	}

	for scanner.Scan() {
		line := scanner.Text()

		// session header: # aider chat started at 2024-01-15 14:30:45
		if strings.HasPrefix(line, aiderSessionPrefix) {
			flush()
			tsStr := strings.TrimPrefix(line, aiderSessionPrefix)
			if t, err := time.Parse(aiderTimestampLayout, strings.TrimSpace(tsStr)); err == nil {
				sessionTS = t
			}
			continue
		}

		// skip other H1 headers
		if strings.HasPrefix(line, "# ") {
			continue
		}

		// user message: #### prompt text
		if strings.HasPrefix(line, "#### ") {
			if currentRole != "user" {
				flush()
				currentRole = "user"
			}
			currentLines = append(currentLines, strings.TrimPrefix(line, "#### "))
			continue
		}

		// tool output: > output text
		if strings.HasPrefix(line, "> ") {
			if currentRole != "tool" {
				flush()
				currentRole = "tool"
			}
			currentLines = append(currentLines, strings.TrimPrefix(line, "> "))
			continue
		}

		// assistant text (everything else)
		if currentRole != "assistant" {
			flush()
			currentRole = "assistant"
		}
		currentLines = append(currentLines, line)
	}

	flush()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// resolveLatestSessionTS scans the history file up to the given byte offset
// and returns the timestamp from the last "# aider chat started at" header.
// This is used by serve mode so incremental reads inherit the correct timestamp.
func resolveLatestSessionTS(path string, offset int64) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	var lastTS time.Time
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var pos int64
	for scanner.Scan() {
		line := scanner.Text()
		lineLen := int64(len(scanner.Bytes())) + 1 // +1 for newline
		if offset > 0 && pos >= offset {
			break
		}
		if strings.HasPrefix(line, aiderSessionPrefix) {
			tsStr := strings.TrimPrefix(line, aiderSessionPrefix)
			if t, err := time.Parse(aiderTimestampLayout, strings.TrimSpace(tsStr)); err == nil {
				lastTS = t
			}
		}
		pos += lineLen
	}
	return lastTS
}

// --- session import ---

// parseAiderSessionByTimestamp reads the history file and returns only entries
// from the session that started at the given timestamp string.
// The timestamp should match the format in "# aider chat started at <ts>" headers
// (e.g., "2024-01-15 14:30:45"). It is normalized to RFC3339 UTC for comparison
// against parsed entries.
func parseAiderSessionByTimestamp(path, sessionTS string) ([]adapterprotocol.RawEntry, error) {
	// normalize to RFC3339 so it matches the wire format stored in RawEntry.Timestamp
	normalizedTS := sessionTS
	if t, err := time.Parse(aiderTimestampLayout, strings.TrimSpace(sessionTS)); err == nil {
		normalizedTS = t.UTC().Format(time.RFC3339)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	allEntries, err := parseAiderMarkdown(scanner, time.Time{})
	if err != nil {
		return nil, err
	}

	// aider sessions share a timestamp from the "# aider chat started at" header;
	// filter to entries whose timestamp matches the requested session
	var matched []adapterprotocol.RawEntry
	for _, e := range allEntries {
		if e.Timestamp == normalizedTS {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("no entries found for session timestamp %q", sessionTS)
	}
	return matched, nil
}

// --- session discovery ---

func findAiderSession(repoRoot, _, since, _ string) (string, error) {
	// aider always writes to .aider.chat.history.md in the project root
	if repoRoot == "" {
		return "", fmt.Errorf("repo-root is required for aider session discovery (was empty)")
	}

	historyPath := filepath.Join(repoRoot, aiderHistoryFile)
	info, err := os.Stat(historyPath)
	if err != nil {
		return "", fmt.Errorf("no aider session found: %w", err)
	}

	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			if !info.ModTime().After(t) {
				return "", fmt.Errorf("no aider sessions found after %s", since)
			}
		}
	}

	return historyPath, nil
}
