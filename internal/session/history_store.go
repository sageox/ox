package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/paths"
)

// History storage errors.
var (
	// ErrHistoryNilHistory is returned when a nil history is passed to store
	ErrHistoryNilHistory = errors.New("captured history cannot be nil")

	// ErrHistoryEmptyEntries is returned when history has no entries to store
	ErrHistoryEmptyEntries = errors.New("captured history has no entries")

	// ErrHistoryStorageFailed is returned when storage operation fails
	ErrHistoryStorageFailed = errors.New("failed to store captured history")
)

const (
	// historyFilename is the default filename for captured history within a session folder
	historyFilename = "prior-history.jsonl"
)

// StoreCapturedHistory stores validated history to the appropriate location.
// If activeRecording is true, merges history into the active session's raw.jsonl.
// Otherwise creates a new session folder with the history as prior-history.jsonl.
//
// Returns the path where history was stored.
func StoreCapturedHistory(history *CapturedHistory, agentID string, activeRecording bool) (storagePath string, err error) {
	if history == nil {
		return "", ErrHistoryNilHistory
	}
	if len(history.Entries) == 0 {
		return "", ErrHistoryEmptyEntries
	}

	// validate history before storing
	result := ValidateHistory(history)
	if !result.Valid {
		return "", fmt.Errorf("%w: %v", ErrHistoryStorageFailed, result.Errors)
	}

	storagePath = GetHistoryStoragePath(agentID, activeRecording)
	if storagePath == "" {
		return "", fmt.Errorf("%w: unable to determine storage path", ErrHistoryStorageFailed)
	}

	if activeRecording {
		// merge into active recording's raw.jsonl
		sessionPath := filepath.Dir(storagePath)
		if err := MergeHistoryWithRecording(sessionPath, history); err != nil {
			return "", fmt.Errorf("%w: merge failed: %w", ErrHistoryStorageFailed, err)
		}
		return storagePath, nil
	}

	// create new session folder and write history
	if err := os.MkdirAll(filepath.Dir(storagePath), 0755); err != nil {
		return "", fmt.Errorf("%w: create directory: %w", ErrHistoryStorageFailed, err)
	}

	if err := WriteHistoryJSONL(storagePath, history); err != nil {
		return "", fmt.Errorf("%w: write history: %w", ErrHistoryStorageFailed, err)
	}

	return storagePath, nil
}

// GetHistoryStoragePath determines where to store captured history.
// If activeRecording is true, returns path to active session's raw.jsonl.
// Otherwise returns path for a new prior-history.jsonl in a new session folder.
//
// Path resolution:
//   - Active recording: <session_path>/raw.jsonl
//   - New session: <sessions_base>/<new_session_name>/prior-history.jsonl
func GetHistoryStoragePath(agentID string, activeRecording bool) string {
	// try to find active recording state
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	if activeRecording {
		state, err := LoadRecordingStateForAgent(cwd, agentID)
		if err == nil && state != nil && state.SessionPath != "" {
			return filepath.Join(state.SessionPath, rawFilename)
		}
		// no active recording found, fall back to new session
	}

	// determine sessions base path
	sessionsBase := getSessionsBasePath(cwd)
	if sessionsBase == "" {
		// fallback to cache location
		sessionsBase = paths.SessionCacheDir("")
	}

	// generate new session name
	sessionID := agentID
	if sessionID == "" {
		sessionID = fmt.Sprintf("history-%d", time.Now().Unix())
	}
	sessionName := GenerateSessionName(sessionID, "history")

	return filepath.Join(sessionsBase, sessionName, historyFilename)
}

// getSessionsBasePath finds the sessions directory for the current project.
func getSessionsBasePath(projectRoot string) string {
	// try to get repo ID from project
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			return filepath.Join(contextPath, "sessions")
		}
	}

	// fallback to project-local sessions
	return filepath.Join(projectRoot, "sessions")
}

// MergeHistoryWithRecording merges captured history into an active recording's raw.jsonl.
// History entries are prepended before existing recorded entries, with seq numbers adjusted.
//
// The merge process:
//  1. Read existing raw.jsonl entries
//  2. Assign seq numbers to history entries (starting from 1)
//  3. Adjust existing entry seq numbers to continue after history
//  4. Write merged entries back to raw.jsonl
func MergeHistoryWithRecording(sessionPath string, history *CapturedHistory) error {
	if history == nil {
		return ErrHistoryNilHistory
	}
	if len(history.Entries) == 0 {
		return ErrHistoryEmptyEntries
	}

	rawPath := filepath.Join(sessionPath, rawFilename)

	// read existing entries from raw.jsonl
	existingEntries, existingMeta, err := readRawJSONLEntries(rawPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing raw.jsonl: %w", err)
	}

	// prepare merged entries
	mergedEntries := make([]map[string]any, 0, len(history.Entries)+len(existingEntries))

	// convert history entries to raw format with seq numbers
	historyOffset := 1
	for i, he := range history.Entries {
		entry := map[string]any{
			"ts":      he.Timestamp.Format(time.RFC3339Nano),
			"type":    he.Type,
			"content": he.Content,
			"seq":     historyOffset + i,
			"source":  HistorySourcePlanningHistory,
		}
		if he.ToolName != "" {
			entry["tool_name"] = he.ToolName
		}
		if he.ToolInput != "" {
			entry["tool_input"] = he.ToolInput
		}
		if he.ToolOutput != "" {
			entry["tool_output"] = he.ToolOutput
		}
		mergedEntries = append(mergedEntries, entry)
	}

	// adjust seq numbers for existing entries and append
	seqOffset := len(history.Entries)
	for _, entry := range existingEntries {
		// adjust seq number if present
		if seq, ok := entry["seq"].(float64); ok {
			entry["seq"] = int(seq) + seqOffset
		}
		mergedEntries = append(mergedEntries, entry)
	}

	// write merged file
	return writeMergedRawJSONL(rawPath, existingMeta, mergedEntries)
}

// readRawJSONLEntries reads entries from a raw.jsonl file.
// Returns entries (excluding header/footer) and the header metadata if present.
func readRawJSONLEntries(path string) ([]map[string]any, map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var entries []map[string]any
	var meta map[string]any

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip invalid lines
		}

		entryType, _ := entry["type"].(string)
		switch entryType {
		case "header":
			meta = entry
		case "footer":
			// skip footer, will regenerate
		default:
			entries = append(entries, entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan file: %w", err)
	}

	return entries, meta, nil
}

// writeMergedRawJSONL writes a merged raw.jsonl file with header, entries, and footer.
func writeMergedRawJSONL(path string, meta map[string]any, entries []map[string]any) error {
	// create temp file for atomic write
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "raw-*.jsonl.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // clean up on error
	}()

	encoder := json.NewEncoder(tmpFile)

	// write header if present
	if meta != nil {
		if err := encoder.Encode(meta); err != nil {
			return fmt.Errorf("write header: %w", err)
		}
	}

	// write entries
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("write entry: %w", err)
		}
	}

	// write footer
	footer := map[string]any{
		"type":        "footer",
		"closed_at":   time.Now().Format(time.RFC3339Nano),
		"entry_count": len(entries),
	}
	if err := encoder.Encode(footer); err != nil {
		return fmt.Errorf("write footer: %w", err)
	}

	// sync and close temp file
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename to final path: %w", err)
	}

	return nil
}

// WriteHistoryJSONL writes captured history to a JSONL file.
// Writes the _meta line first, then each entry on its own line.
func WriteHistoryJSONL(path string, history *CapturedHistory) error {
	if history == nil {
		return ErrHistoryNilHistory
	}

	// ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)

	// write metadata line (always write, even if empty - schema requires it)
	metaLine := map[string]any{
		"_meta": history.Meta,
	}
	if err := encoder.Encode(metaLine); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// write entries
	for _, entry := range history.Entries {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("write entry seq=%d: %w", entry.Seq, err)
		}
	}

	// sync to ensure durability
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync file: %w", err)
	}

	return nil
}

// LoadHistoryFromSession loads captured history from a session folder.
// Looks for prior-history.jsonl in the session directory.
func LoadHistoryFromSession(sessionPath string) (*CapturedHistory, error) {
	historyPath := filepath.Join(sessionPath, historyFilename)
	return ParseHistoryFile(historyPath)
}

// SessionHasHistory checks if a session folder contains prior-history.jsonl.
func SessionHasHistory(sessionPath string) bool {
	historyPath := filepath.Join(sessionPath, historyFilename)
	_, err := os.Stat(historyPath)
	return err == nil
}
