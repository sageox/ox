// Package session provides storage and management for agent session recordings.
// Sessions are stored in the XDG cache directory under context/<repo-id>/sessions/
// using a session-folder structure for organized storage of multiple output formats.
//
// TODO(server-side): move session file creation to server-side for MVP+1.
// Currently client writes all session files; server should own ledger writes.
//
// Session folder structure:
//
//	sessions/
//	└── <session-name>/
//	    ├── raw.jsonl        # unprocessed session capture
//	    ├── events.jsonl     # processed/structured events
//	    ├── summary.md       # ai-generated summary
//	    ├── session.md    # markdown session
//	    └── session.html  # html session
//
// Session name format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Example: 2026-01-06T14-32-ryan-Ox7f3a
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
)

const (
	rawFilename    = "raw.jsonl"
	eventsFilename = "events.jsonl"
	jsonlExt       = ".jsonl"
)

// Store manages session storage in the ledger.
// Path structure: {project}_sageox_ledger/sessions/
// Uses session-folder structure for organized storage of multiple output formats.
type Store struct {
	basePath string // full path to sessions directory
}

// NewStore creates a store for the given ledger path.
// The repoContextPath should be the full path to the ledger directory
// (e.g., {project}_sageox_ledger/).
func NewStore(repoContextPath string) (*Store, error) {
	if repoContextPath == "" {
		return nil, fmt.Errorf("%w: repo context path", ErrEmptyPath)
	}

	absPath, err := filepath.Abs(repoContextPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repo context path=%s: %w", repoContextPath, err)
	}

	basePath := filepath.Join(absPath, "sessions")

	// create base directory (session folders created on demand)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create sessions dir=%s: %w", basePath, err)
	}

	return &Store{
		basePath: basePath,
	}, nil
}

// BasePath returns the base path where sessions are stored.
func (s *Store) BasePath() string {
	return s.basePath
}

// GenerateSessionName creates a unique session folder name.
// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Example: 2026-01-06T14-32-ryan-Ox7f3a
func GenerateSessionName(sessionID, username string) string {
	now := time.Now()
	timestamp := now.Format("2006-01-02T15-04")

	// sanitize username for filesystem safety
	safeUsername := sanitizeFilename(username)
	if safeUsername == "" {
		safeUsername = "anonymous"
	}

	// sanitize sessionID for filesystem safety
	safeSessionID := sanitizeFilename(sessionID)
	if safeSessionID == "" {
		safeSessionID = "unknown"
	}

	return fmt.Sprintf("%s-%s-%s", timestamp, safeUsername, safeSessionID)
}

// GenerateFilename creates a unique filename for a session (legacy format).
// Format: 2026-01-05T10-30-<username>-<agentID>.jsonl
// The timestamp uses hour-minute precision with dashes for filesystem compatibility.
// Deprecated: Use GenerateSessionName for new session-folder structure.
func GenerateFilename(username, agentID string) string {
	return GenerateSessionName(agentID, username) + jsonlExt
}

// sanitizeFilename removes or replaces characters unsafe for filenames.
func sanitizeFilename(s string) string {
	// replace path separators and other problematic chars
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "*", "-")
	s = strings.ReplaceAll(s, "?", "-")
	s = strings.ReplaceAll(s, "\"", "-")
	s = strings.ReplaceAll(s, "<", "-")
	s = strings.ReplaceAll(s, ">", "-")
	s = strings.ReplaceAll(s, "|", "-")
	// collapse multiple dashes
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	// trim leading/trailing dashes
	s = strings.Trim(s, "-")
	return s
}

// CreateRaw creates a new raw session file and returns a writer.
// Uses session-folder structure: <session>/raw.jsonl
// The sessionName should be generated using GenerateSessionName().
func (s *Store) CreateRaw(sessionName string) (*SessionWriter, error) {
	return s.createSessionSession(sessionName, rawFilename)
}

// CreateEvents creates a new events session file and returns a writer.
// Uses session-folder structure: <session>/events.jsonl
// The sessionName should be generated using GenerateSessionName().
func (s *Store) CreateEvents(sessionName string) (*SessionWriter, error) {
	return s.createSessionSession(sessionName, eventsFilename)
}

// createSessionSession creates a session file in a session folder.
// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
func (s *Store) createSessionSession(sessionName, filename string) (*SessionWriter, error) {
	if sessionName == "" {
		return nil, ErrEmptyFilename
	}

	// strip .jsonl extension if present (legacy callers may pass full filename)
	sessionName = strings.TrimSuffix(sessionName, jsonlExt)

	sessionPath := filepath.Join(s.basePath, sessionName)

	// create session directory
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		return nil, fmt.Errorf("create session dir=%s: %w", sessionPath, err)
	}

	filePath := filepath.Join(sessionPath, filename)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("create session file=%s: %w", filePath, err)
	}

	return &SessionWriter{
		file:        f,
		filePath:    filePath,
		encoder:     json.NewEncoder(f),
		sessionName: sessionName,
	}, nil
}

// SessionWriter handles JSONL writing with proper header/footer semantics.
type SessionWriter struct {
	file        *os.File
	filePath    string
	encoder     *json.Encoder
	storeMeta   *StoreMeta
	count       int
	sessionName string // session folder name (empty for legacy files)
}

// StoreMeta contains session storage metadata written to the header.
// This is distinct from Metadata in eventlog.go which tracks event extraction metadata.
type StoreMeta struct {
	Version      string    `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	AgentID      string    `json:"agent_id,omitempty"`
	AgentType    string    `json:"agent_type,omitempty"`
	AgentVersion string    `json:"agent_version,omitempty"` // version of the coding agent (e.g., "1.0.3")
	Model        string    `json:"model,omitempty"`         // LLM model used (e.g., "claude-sonnet-4-20250514")
	Username     string    `json:"username,omitempty"`
	RepoID       string    `json:"repo_id,omitempty"`
	OxVersion    string    `json:"ox_version,omitempty"` // version of ox that created this session
}

// Writable is an interface for entries that can be written to a session.
// This is distinct from Entry in secrets.go and eventlog.go.
type Writable interface {
	// EntryType returns the type identifier for this entry (e.g., "message", "tool_call")
	EntryType() string
}

// WriteHeader writes the session header with metadata.
// Should be called once at the start of recording.
func (w *SessionWriter) WriteHeader(meta *StoreMeta) error {
	if meta == nil {
		meta = &StoreMeta{
			Version:   "1.0",
			CreatedAt: time.Now(),
		}
	}
	w.storeMeta = meta

	header := map[string]any{
		"type":     "header",
		"metadata": meta,
	}

	if err := w.encoder.Encode(header); err != nil {
		return fmt.Errorf("write header file=%s: %w", w.filePath, err)
	}
	return nil
}

// WriteEntry writes a single entry to the session.
func (w *SessionWriter) WriteEntry(entry Writable) error {
	if entry == nil {
		return ErrNilEntry
	}

	record := map[string]any{
		"type":      entry.EntryType(),
		"timestamp": time.Now(),
		"seq":       w.count,
		"data":      entry,
	}

	if err := w.encoder.Encode(record); err != nil {
		return fmt.Errorf("write entry seq=%d file=%s: %w", w.count, w.filePath, err)
	}

	w.count++
	return nil
}

// WriteRaw writes a raw entry map directly (for flexibility).
func (w *SessionWriter) WriteRaw(data map[string]any) error {
	if data == nil {
		return ErrNilData
	}

	// add timestamp and sequence if not present
	if _, ok := data["timestamp"]; !ok {
		data["timestamp"] = time.Now()
	}
	if _, ok := data["seq"]; !ok {
		data["seq"] = w.count
	}

	if err := w.encoder.Encode(data); err != nil {
		return fmt.Errorf("write raw entry seq=%d file=%s: %w", w.count, w.filePath, err)
	}

	w.count++
	return nil
}

// Count returns the number of entries written (excluding header/footer).
func (w *SessionWriter) Count() int {
	return w.count
}

// FilePath returns the path to the session file.
func (w *SessionWriter) FilePath() string {
	return w.filePath
}

// SessionName returns the session folder name (empty for legacy files).
func (w *SessionWriter) SessionName() string {
	return w.sessionName
}

// Close writes the footer and closes the file.
func (w *SessionWriter) Close() error {
	footer := map[string]any{
		"type":        "footer",
		"closed_at":   time.Now(),
		"entry_count": w.count,
	}

	if err := w.encoder.Encode(footer); err != nil {
		w.file.Close()
		return fmt.Errorf("write footer file=%s: %w", w.filePath, err)
	}

	if err := w.file.Sync(); err != nil {
		w.file.Close()
		return fmt.Errorf("sync file=%s: %w", w.filePath, err)
	}

	return w.file.Close()
}

// SessionInfo contains metadata about a stored session.
type SessionInfo struct {
	SessionName     string              `json:"session_name,omitempty"` // session folder name (empty for legacy)
	Filename        string              `json:"filename"`               // file name only
	FilePath        string              `json:"file_path"`              // full path to file
	Type            string              `json:"type"`                   // "raw" or "events"
	Size            int64               `json:"size"`
	CreatedAt       time.Time           `json:"created_at"`
	ModTime         time.Time           `json:"mod_time"`
	HydrationStatus lfs.HydrationStatus `json:"hydration_status,omitempty"` // hydrated/dehydrated/partial
	Username        string              `json:"username,omitempty"`         // from meta.json
	Summary         string              `json:"summary,omitempty"`          // from meta.json
	Recording       bool                `json:"recording,omitempty"`        // true if session is actively being recorded
	AgentID         string              `json:"agent_id,omitempty"`         // from .recording.json when recording
}

// ListSessions returns session files from the last 7 days, sorted by date descending.
// Use ListSessionsSince for custom time windows or ListAllSessions for all sessions.
func (s *Store) ListSessions() ([]SessionInfo, error) {
	return s.ListSessionsSince(time.Now().Add(-DefaultListWindow))
}

// ListAllSessions returns all session files regardless of age.
// This may be slow with many sessions due to meta.json reads.
func (s *Store) ListAllSessions() ([]SessionInfo, error) {
	return s.ListSessionsSince(time.Time{})
}

// ListSessionsSince returns session files created after the given time.
// Pass zero time to list all sessions.
func (s *Store) ListSessionsSince(since time.Time) ([]SessionInfo, error) {
	sessions, err := s.listSessionSessions(since)
	if err != nil {
		return nil, fmt.Errorf("list session sessions: %w", err)
	}

	// sort by mod time descending (newest first)
	slices.SortFunc(sessions, func(a, b SessionInfo) int {
		return b.ModTime.Compare(a.ModTime)
	})

	return sessions, nil
}

// DefaultListWindow is the default time window for listing sessions.
// Only sessions within this window will have meta.json read for hydration status.
// Use time.Duration(0) to list all sessions.
const DefaultListWindow = 7 * 24 * time.Hour

// listSessionSessions lists sessions from session folders.
// Includes both hydrated sessions (with content files) and dehydrated sessions
// (only meta.json present, content in LFS blob storage).
// If since is non-zero, only sessions created after that time are fully processed;
// older sessions are skipped entirely for performance.
func (s *Store) listSessionSessions(since time.Time) ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir=%s: %w", s.basePath, err)
	}

	var sessions []SessionInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		// parse timestamp from directory name to filter by time
		sessionTime := parseFilenameTimestamp(name)
		if !since.IsZero() && !sessionTime.IsZero() && sessionTime.Before(since) {
			continue // skip sessions older than the cutoff
		}

		sessionPath := filepath.Join(s.basePath, name)

		// check hydration status from meta.json if present
		var hydrationStatus lfs.HydrationStatus
		var username, summary string
		var createdAt time.Time
		meta, metaErr := lfs.ReadSessionMeta(sessionPath)
		if metaErr == nil && meta != nil {
			hydrationStatus = lfs.CheckHydrationStatus(sessionPath, meta)
			username = meta.Username
			summary = meta.Summary
			createdAt = meta.CreatedAt
		}

		// prefer timestamp from directory name
		if parsedTime := parseFilenameTimestamp(name); !parsedTime.IsZero() {
			createdAt = parsedTime
		}

		// check if this session is actively being recorded
		var isRecording bool
		var recordingAgentID string
		recPath := filepath.Join(sessionPath, recordingFile)
		if recData, recErr := os.ReadFile(recPath); recErr == nil {
			var recState RecordingState
			if err := json.Unmarshal(recData, &recState); err == nil {
				isRecording = true
				recordingAgentID = recState.AgentID
				if !recState.StartedAt.IsZero() && createdAt.IsZero() {
					createdAt = recState.StartedAt
				}
			}
		}

		// Create ONE entry per session directory (not per file)
		// Prefer raw.jsonl as the primary file, fallback to checking for any content
		rawPath := filepath.Join(sessionPath, rawFilename)
		var filePath string
		var fileSize int64
		var modTime time.Time

		if info, err := os.Stat(rawPath); err == nil {
			// raw.jsonl exists (hydrated or recording in progress)
			filePath = rawPath
			fileSize = info.Size()
			modTime = info.ModTime()
			if createdAt.IsZero() {
				createdAt = info.ModTime()
			}
		} else if meta != nil {
			// dehydrated session (only meta.json exists)
			// use raw.jsonl reference from manifest if available
			if ref, ok := meta.Files[rawFilename]; ok {
				filePath = rawPath
				fileSize = ref.Size
			} else if ref, ok := meta.Files[eventsFilename]; ok {
				// fallback to events.jsonl if raw.jsonl not in manifest
				filePath = filepath.Join(sessionPath, eventsFilename)
				fileSize = ref.Size
			}
			modTime = createdAt
			if createdAt.IsZero() {
				createdAt = time.Now()
			}
		} else if isRecording {
			// no raw.jsonl yet but recording is active (just started)
			filePath = recPath
			if recInfo, statErr := os.Stat(recPath); statErr == nil {
				modTime = recInfo.ModTime()
				if createdAt.IsZero() {
					createdAt = recInfo.ModTime()
				}
			} else {
				modTime = createdAt
			}
		} else {
			// no meta.json, no raw.jsonl, no recording — skip
			continue
		}

		// add single entry for this session
		sessions = append(sessions, SessionInfo{
			SessionName:     name,
			Filename:        filepath.Base(filePath),
			FilePath:        filePath,
			Type:            "raw",
			Size:            fileSize,
			CreatedAt:       createdAt,
			ModTime:         modTime,
			HydrationStatus: hydrationStatus,
			Username:        username,
			Summary:         summary,
			Recording:       isRecording,
			AgentID:         recordingAgentID,
		})
	}

	return sessions, nil
}

// ListRawSessionsSince returns only raw session files created after the given time.
func (s *Store) ListRawSessionsSince(since time.Time) ([]SessionInfo, error) {
	sessions, err := s.listSessionSessions(since)
	if err != nil {
		return nil, err
	}

	var files []SessionInfo
	for _, t := range sessions {
		if t.Type == "raw" {
			files = append(files, t)
		}
	}

	slices.SortFunc(files, func(a, b SessionInfo) int {
		return b.ModTime.Compare(a.ModTime)
	})

	return files, nil
}

// ListSessionNames returns unique session folder names, sorted by date descending.
func (s *Store) ListSessionNames() ([]string, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir=%s: %w", s.basePath, err)
	}

	var sessions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessions = append(sessions, entry.Name())
	}

	// sort by timestamp descending (newest first)
	slices.SortFunc(sessions, func(a, b string) int {
		timeA := parseFilenameTimestamp(a)
		timeB := parseFilenameTimestamp(b)
		return timeB.Compare(timeA)
	})

	return sessions, nil
}

// ResolveSessionName resolves a partial session name (e.g. agent ID suffix like "OxKMZN")
// to the full session directory name (e.g. "2026-01-28T18-56-ajit-sageox-ai-OxKMZN").
// Returns the input unchanged if it already matches exactly or no match is found.
// Returns an error if multiple sessions match the suffix.
func (s *Store) ResolveSessionName(name string) (string, error) {
	// check exact match first
	if _, err := os.Stat(filepath.Join(s.basePath, name)); err == nil {
		return name, nil
	}

	sessionNames, err := s.ListSessionNames()
	if err != nil {
		return name, err
	}

	var matches []string
	for _, sessionName := range sessionNames {
		if strings.HasSuffix(sessionName, "-"+name) {
			matches = append(matches, sessionName)
		}
	}

	switch len(matches) {
	case 0:
		return name, nil // no match, return as-is (caller will get a clear "not found" error)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous session name %q matches %d sessions: %s",
			name, len(matches), strings.Join(matches, ", "))
	}
}

// GetSessionPath returns the full path to a session folder.
func (s *Store) GetSessionPath(sessionName string) string {
	return filepath.Join(s.basePath, sessionName)
}

// IsSessionHydrated checks if content files exist locally for a session.
// Returns true if content files are present (authored locally or hydrated from LFS).
func (s *Store) IsSessionHydrated(sessionName string) bool {
	sessionPath := s.GetSessionPath(sessionName)
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		// no meta.json — check for raw.jsonl as fallback (legacy or pre-LFS sessions)
		rawPath := filepath.Join(sessionPath, rawFilename)
		_, rawErr := os.Stat(rawPath)
		return rawErr == nil
	}
	return lfs.CheckHydrationStatus(sessionPath, meta) == lfs.HydrationStatusHydrated
}

// CheckNeedsDownload returns the session name if the session exists as a stub
// (meta.json present but content files not hydrated).
// Returns empty string if the session doesn't exist or is already hydrated.
func (s *Store) CheckNeedsDownload(name string) string {
	sessionName := strings.TrimSuffix(name, jsonlExt)
	sessionPath := filepath.Join(s.basePath, sessionName)

	// check if session directory exists
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return ""
	}

	// check if meta.json exists (indicates the session was synced)
	metaPath := filepath.Join(sessionPath, "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		return ""
	}

	// if not hydrated, it needs download
	if !s.IsSessionHydrated(sessionName) {
		return sessionName
	}

	return ""
}

// ReadLFSSessionMeta reads the LFS meta.json for a session.
// Returns nil if the session has no meta.json (legacy or pre-LFS session).
func (s *Store) ReadLFSSessionMeta(sessionName string) (*lfs.SessionMeta, error) {
	sessionPath := s.GetSessionPath(sessionName)
	return lfs.ReadSessionMeta(sessionPath)
}

// inferTypeFromFilename returns "raw" or "events" for session files, empty string otherwise.
func inferTypeFromFilename(filename string) string {
	switch filename {
	case rawFilename: // "raw.jsonl"
		return "raw"
	case eventsFilename: // "events.jsonl"
		return "events"
	default:
		return ""
	}
}

// parseFilenameTimestamp extracts timestamp from filename/session format: 2006-01-02T15-04-*
func parseFilenameTimestamp(name string) time.Time {
	// expect format: 2006-01-02T15-04-username-sessionid[.jsonl]
	if len(name) < 16 {
		return time.Time{}
	}

	// extract timestamp portion (first 16 chars: 2006-01-02T15-04)
	timestampStr := name[:16]
	t, err := time.ParseInLocation("2006-01-02T15-04", timestampStr, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// StoredSession represents a read session with its entries.
// This is distinct from Session in secrets.go which is for redaction.
type StoredSession struct {
	Info    SessionInfo      `json:"info"`
	Meta    *StoreMeta       `json:"metadata,omitempty"`
	Entries []map[string]any `json:"entries"`
	Footer  map[string]any   `json:"footer,omitempty"`
}

// ReadSession reads a session by session name or filename.
func (s *Store) ReadSession(name string) (*StoredSession, error) {
	// strip .jsonl if present for compatibility
	sessionName := strings.TrimSuffix(name, jsonlExt)

	// check session folder for raw.jsonl
	sessionRawPath := filepath.Join(s.basePath, sessionName, rawFilename)
	if _, err := os.Stat(sessionRawPath); err == nil {
		return s.readSessionFile(sessionRawPath, "raw", sessionName)
	}

	// check session folder for events.jsonl
	sessionEventsPath := filepath.Join(s.basePath, sessionName, eventsFilename)
	if _, err := os.Stat(sessionEventsPath); err == nil {
		return s.readSessionFile(sessionEventsPath, "events", sessionName)
	}

	return nil, fmt.Errorf("%w: name=%s", ErrSessionNotFound, name)
}

// ReadSessionRaw reads the raw session from a session folder.
func (s *Store) ReadSessionRaw(sessionName string) (*StoredSession, error) {
	filePath := filepath.Join(s.basePath, sessionName, rawFilename)
	return s.readSessionFile(filePath, "raw", sessionName)
}

// ReadSessionEvents reads the events session from a session folder.
func (s *Store) ReadSessionEvents(sessionName string) (*StoredSession, error) {
	filePath := filepath.Join(s.basePath, sessionName, eventsFilename)
	return s.readSessionFile(filePath, "events", sessionName)
}

// ReadRawSession reads the raw session file from a session folder.
func (s *Store) ReadRawSession(filename string) (*StoredSession, error) {
	sessionName := strings.TrimSuffix(filename, jsonlExt)
	sessionPath := filepath.Join(s.basePath, sessionName, rawFilename)
	return s.readSessionFile(sessionPath, "raw", sessionName)
}

// ReadEventsSession reads the events session file from a session folder.
func (s *Store) ReadEventsSession(filename string) (*StoredSession, error) {
	sessionName := strings.TrimSuffix(filename, jsonlExt)
	sessionPath := filepath.Join(s.basePath, sessionName, eventsFilename)
	return s.readSessionFile(sessionPath, "events", sessionName)
}

// readSessionFile reads and parses a session file.
func (s *Store) readSessionFile(filePath, sessionType, sessionName string) (*StoredSession, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open session file=%s: %w", filePath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat session file=%s: %w", filePath, err)
	}

	// determine the name to parse for timestamp
	nameForTimestamp := filepath.Base(filePath)
	if sessionName != "" {
		nameForTimestamp = sessionName
	}

	session := &StoredSession{
		Info: SessionInfo{
			SessionName: sessionName,
			Filename:    filepath.Base(filePath),
			FilePath:    filePath,
			Type:        sessionType,
			Size:        info.Size(),
			CreatedAt:   parseFilenameTimestamp(nameForTimestamp),
			ModTime:     info.ModTime(),
		},
		Entries: make([]map[string]any, 0),
	}

	scanner := bufio.NewScanner(f)
	// increase buffer size for potentially large entries
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip invalid lines
		}

		entryType, _ := entry["type"].(string)
		switch entryType {
		case "header":
			if metadata, ok := entry["metadata"].(map[string]any); ok {
				session.Meta = ParseStoreMeta(metadata)
			}
		case "footer":
			session.Footer = entry
		default:
			// check for _meta header format (alternative header style)
			if meta, ok := entry["_meta"].(map[string]any); ok {
				session.Meta = ParseStoreMeta(meta)
				continue
			}
			session.Entries = append(session.Entries, entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session file=%s: %w", filePath, err)
	}

	return session, nil
}

// ReadSessionFromPath reads and parses a JSONL session from an arbitrary file path.
// This is useful for generating HTML from sessions outside the managed store location.
func ReadSessionFromPath(filePath string) (*StoredSession, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: path=%s", ErrSessionNotFound, absPath)
		}
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	session := &StoredSession{
		Info: SessionInfo{
			Filename:  filepath.Base(absPath),
			FilePath:  absPath,
			Type:      "raw", // assume raw for external files
			Size:      info.Size(),
			CreatedAt: parseFilenameTimestamp(filepath.Base(absPath)),
			ModTime:   info.ModTime(),
		},
		Entries: make([]map[string]any, 0),
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip invalid lines
		}

		entryType, _ := entry["type"].(string)
		switch entryType {
		case "header":
			if metadata, ok := entry["metadata"].(map[string]any); ok {
				session.Meta = ParseStoreMeta(metadata)
			}
		case "footer":
			session.Footer = entry
		default:
			// check for _meta header format (alternative header style)
			if meta, ok := entry["_meta"].(map[string]any); ok {
				session.Meta = ParseStoreMeta(meta)
				continue
			}
			session.Entries = append(session.Entries, entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return session, nil
}

// ParseStoreMeta converts a map to StoreMeta struct.
// Supports both standard format (version, agent_id, created_at) and
// alternative format (schema_version, session_id, started_at).
func ParseStoreMeta(m map[string]any) *StoreMeta {
	meta := &StoreMeta{}

	// version (or schema_version)
	if v, ok := m["version"].(string); ok {
		meta.Version = v
	} else if v, ok := m["schema_version"].(string); ok {
		meta.Version = v
	}

	// agent_id (or session_id)
	if v, ok := m["agent_id"].(string); ok {
		meta.AgentID = v
	} else if v, ok := m["session_id"].(string); ok {
		meta.AgentID = v
	}

	if v, ok := m["agent_type"].(string); ok {
		meta.AgentType = v
	}
	if v, ok := m["agent_version"].(string); ok {
		meta.AgentVersion = v
	}
	if v, ok := m["model"].(string); ok {
		meta.Model = v
	}

	// username (or ox_username)
	if v, ok := m["username"].(string); ok {
		meta.Username = v
	} else if v, ok := m["ox_username"].(string); ok {
		meta.Username = v
	}

	if v, ok := m["repo_id"].(string); ok {
		meta.RepoID = v
	}
	if v, ok := m["ox_version"].(string); ok {
		meta.OxVersion = v
	}

	// created_at (or started_at)
	createdAtStr := ""
	if v, ok := m["created_at"].(string); ok {
		createdAtStr = v
	} else if v, ok := m["started_at"].(string); ok {
		createdAtStr = v
	}
	if createdAtStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, createdAtStr); err == nil {
			meta.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			meta.CreatedAt = t
		}
	}

	return meta
}

// GetLatest returns the most recent session info (from any type).
// Searches all sessions regardless of age.
func (s *Store) GetLatest() (*SessionInfo, error) {
	sessions, err := s.ListAllSessions()
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, ErrNoSessions
	}

	// already sorted by date descending
	return &sessions[0], nil
}

// GetLatestRaw returns the most recent raw session info.
// Searches all sessions regardless of age.
func (s *Store) GetLatestRaw() (*SessionInfo, error) {
	sessions, err := s.ListRawSessionsSince(time.Time{})
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, ErrNoRawSessions
	}

	return &sessions[0], nil
}

// Delete removes a session folder by name.
func (s *Store) Delete(name string) error {
	sessionName := strings.TrimSuffix(name, jsonlExt)
	sessionPath := filepath.Join(s.basePath, sessionName)
	if info, err := os.Stat(sessionPath); err == nil && info.IsDir() {
		if err := os.RemoveAll(sessionPath); err != nil {
			return fmt.Errorf("delete session folder=%s: %w", sessionPath, err)
		}
		return nil
	}

	return fmt.Errorf("%w: name=%s", ErrSessionNotFound, name)
}

// DeleteSession removes a session folder and all its contents.
func (s *Store) DeleteSession(sessionName string) error {
	sessionPath := filepath.Join(s.basePath, sessionName)
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: session=%s", ErrSessionNotFound, sessionName)
	}

	if err := os.RemoveAll(sessionPath); err != nil {
		return fmt.Errorf("delete session folder=%s: %w", sessionPath, err)
	}
	return nil
}

// Prune removes sessions older than the specified duration.
// Returns the number of sessions removed.
func (s *Store) Prune(olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	var removed int

	sessionNames, err := s.ListSessionNames()
	if err != nil {
		return 0, err
	}
	for _, sessionName := range sessionNames {
		sessionTime := parseFilenameTimestamp(sessionName)
		if !sessionTime.IsZero() && sessionTime.Before(cutoff) {
			if err := s.DeleteSession(sessionName); err == nil {
				removed++
			}
		}
	}

	return removed, nil
}

// GetCacheDir returns the SageOx cache directory.
//
// Path Resolution (via internal/paths package):
//
//	Default:           ~/.sageox/cache/
//	With OX_XDG_ENABLE: $XDG_CACHE_HOME/sageox/
//
// See internal/paths/doc.go for architecture rationale.
func GetCacheDir() string {
	return paths.CacheDir()
}

// GetContextPath returns the full path to a repo's session context directory.
//
// Path Resolution (via internal/paths package):
//
//	Default:           ~/.sageox/cache/sessions/<repo-id>/
//	With OX_XDG_ENABLE: $XDG_CACHE_HOME/sageox/sessions/<repo-id>/
//
// See internal/paths/doc.go for architecture rationale.
func GetContextPath(repoID string) string {
	return paths.SessionCacheDir(repoID)
}
