// session.go handles OpenCode session reading from SQLite.
//
// OpenCode stores sessions in ~/.local/share/opencode/opencode.db across three
// tables (all singular):
//
//	session(id, project_id, parent_id, directory, title, version, model JSON,
//	        time_created, time_updated, ...)
//	message(id, session_id, time_created, time_updated, data JSON)
//	part   (id, message_id, session_id, time_created, time_updated, data JSON)
//
// A message row carries only envelope fields (role, timestamps, model) in its
// data JSON; the conversation content lives in the part rows that reference it.
// Part data is a discriminated union on "type": text, tool, reasoning,
// step-start, step-finish, patch.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // +1.9MB to binary; pure-Go SQLite needed because OpenCode stores sessions in SQLite

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- OpenCode message schema ---

// ocMessageData is the JSON stored in message.data. OpenCode omits id and
// sessionID there because both are already columns.
type ocMessageData struct {
	Role string `json:"role"`
	Time struct {
		Created int64 `json:"created"` // Unix milliseconds
	} `json:"time"`
	ModelID    string `json:"modelID,omitempty"`
	ProviderID string `json:"providerID,omitempty"`
}

// ocPartData is the JSON stored in part.data, a union discriminated on Type.
type ocPartData struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	CallID string       `json:"callID,omitempty"`
	Tool   string       `json:"tool,omitempty"`
	State  *ocToolState `json:"state,omitempty"`
}

// ocToolState is the tool part's execution state. Input stays raw because
// OpenCode types it per-tool; ox forwards it verbatim.
type ocToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// ocSessionModel is the JSON stored in session.model.
type ocSessionModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
}

// rowQuery pairs every message with each of its parts. LEFT JOIN keeps
// part-less messages visible so the offset stays aligned with what a reader
// has actually seen.
const rowQuery = `SELECT m.data, p.data
	FROM message m
	LEFT JOIN part p ON p.message_id = m.id
	WHERE m.session_id = ?
	ORDER BY m.time_created ASC, m.id ASC, p.id ASC`

const rowCountQuery = `SELECT COUNT(*)
	FROM message m
	LEFT JOIN part p ON p.message_id = m.id
	WHERE m.session_id = ?`

// --- database helpers ---

func openDB() (*sql.DB, error) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return nil, fmt.Errorf("opencode data directory not found")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("opencode.db not found at %s", dbPath)
	}
	// read-only, and deliberately without a journal_mode pragma: journal mode
	// is a property of the database, not the connection, and setting it on a
	// read-only handle fails with "attempt to write a readonly database" —
	// which then masquerades as a schema problem in diagnose
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open opencode.db: %w", err)
	}
	return db, nil
}

func openCodeDBPath() string {
	dataDir := openCodeDataDir()
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "opencode.db")
}

// --- session discovery ---

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	sessionID, err := resolveSessionID(db, p)
	if err != nil {
		return nil, err
	}

	// start reading where the session currently ends
	offset, err := countRows(db, sessionID)
	if err != nil {
		return nil, err
	}

	// session file is a virtual handle — the rows live in SQLite, not on disk
	return &adapterprotocol.FindSessionResult{
		SessionFile: fmt.Sprintf("opencode:%s", sessionID),
		Offset:      offset,
	}, nil
}

// resolveSessionID picks the session to read: an explicit id when the caller
// knows it, otherwise the newest root session, preferring one whose working
// directory matches the repo we were asked about.
func resolveSessionID(db *sql.DB, p adapterprotocol.FindSessionParams) (string, error) {
	if p.AgentSessionID != "" {
		var id string
		err := db.QueryRow("SELECT id FROM session WHERE id = ? LIMIT 1", p.AgentSessionID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("session %s not found", p.AgentSessionID)
		}
		if err != nil {
			return "", fmt.Errorf("query session: %w", err)
		}
		return id, nil
	}

	var sinceMS int64
	if p.Since != "" {
		t, err := time.Parse(time.RFC3339, p.Since)
		if err != nil {
			return "", fmt.Errorf("invalid since %q: %w", p.Since, err)
		}
		sinceMS = t.UnixMilli()
	}

	// A project-scoped query (RepoRoot set) is confined to that project's own
	// sessions — never falling back to "newest session anywhere" — because
	// Ledgers are per-repo and shared with teammates; attaching another
	// project's session here would leak its conversation content into the
	// wrong Ledger. Only a genuinely unscoped query (RepoRoot == "") searches
	// across every directory.
	if p.RepoRoot != "" {
		root := filepath.Clean(p.RepoRoot)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		id, err := queryLatestSession(db, root, sinceMS)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("no opencode sessions found for %s", p.RepoRoot)
		}
		return id, nil
	}

	id, err := queryLatestSession(db, "", sinceMS)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("no opencode sessions found")
	}
	return id, nil
}

// queryLatestSession returns the newest root session whose directory is
// `directory` itself or a subdirectory beneath it, or "" when none match. An
// empty `directory` matches every session. The directory comparison happens
// in Go rather than SQL LIKE/wildcard concatenation, because a directory
// containing a literal '%' or '_' would otherwise be misread as a wildcard.
func queryLatestSession(db *sql.DB, directory string, sinceMS int64) (string, error) {
	var conds []string
	var args []any

	if sinceMS > 0 {
		conds = append(conds, "time_created >= ?")
		args = append(args, sinceMS)
	}

	// Every element of conds is a compile-time constant declared above, and
	// every value is bound as a parameter in args — nothing user-supplied is
	// ever concatenated into the SQL text.
	query := "SELECT id, directory FROM session WHERE parent_id IS NULL"
	if len(conds) > 0 {
		query += " AND " + strings.Join(conds, " AND ") //nolint:gosec // G202: conds holds only literals; values are bound parameters
	}
	query += " ORDER BY time_created DESC"

	rows, err := db.Query(query, args...)
	if err != nil {
		return "", fmt.Errorf("query sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, dir string
		if err := rows.Scan(&id, &dir); err != nil {
			return "", fmt.Errorf("scan session row: %w", err)
		}
		if directory == "" || underRoot(dir, directory) {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("query sessions: %w", err)
	}
	return "", nil
}

// underRoot reports whether dir is root itself or a subdirectory beneath it,
// after cleaning both. root is expected to already be cleaned (and, where
// possible, symlink-resolved) by the caller; dir comes straight from
// OpenCode's database and is cleaned here.
func underRoot(dir, root string) bool {
	dir = filepath.Clean(dir)
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, root+string(filepath.Separator))
}

// --- full session read ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	sessionID := extractSessionID(p.SessionFile)
	if sessionID == "" {
		return nil, fmt.Errorf("invalid session file: %s (expected opencode:<session-id>)", p.SessionFile)
	}

	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	entries, _, err := readMessages(db, sessionID, 0)
	if err != nil {
		return nil, err
	}

	meta, _ := readMetadata(db, sessionID)

	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	sessionID := extractSessionID(p.SessionFile)
	if sessionID == "" {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}

	db, err := openDB()
	if err != nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	defer func() { _ = db.Close() }()

	meta, _ := readMetadata(db, sessionID)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		Model: meta.Model,
	}, nil
}

// handleReadFromOffset reads rows added since the last offset. The offset is a
// count of message-part rows, not messages — a single assistant turn emits many
// parts, and a message-granular offset would re-emit them on every poll.
func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	sessionID := extractSessionID(p.SessionFile)
	if sessionID == "" {
		return nil, fmt.Errorf("invalid session file: %s", p.SessionFile)
	}

	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	entries, rowsRead, err := readMessages(db, sessionID, p.Offset)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.ReadFromOffsetResult{
		Entries:   entries,
		NewOffset: p.Offset + rowsRead,
	}, nil
}

// --- internal helpers ---

// readMessages reads message/part rows, skipping the first `offset` of them.
// It returns the parsed entries and the number of rows consumed, which is what
// the caller advances its offset by.
func readMessages(db *sql.DB, sessionID string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	query := rowQuery
	args := []any{sessionID}

	if offset > 0 {
		query += " LIMIT -1 OFFSET ?"
		args = append(args, offset)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []adapterprotocol.RawEntry
	var rowsRead int64

	for rows.Next() {
		var messageJSON string
		var partJSON sql.NullString

		if err := rows.Scan(&messageJSON, &partJSON); err != nil {
			return nil, 0, fmt.Errorf("scan message row for session %s: %w", sessionID, err)
		}
		rowsRead++

		var msg ocMessageData
		if err := json.Unmarshal([]byte(messageJSON), &msg); err != nil {
			continue // unparseable envelope — the part carries no usable role
		}
		if !partJSON.Valid {
			continue // message with no parts yet
		}

		ts := time.UnixMilli(msg.Time.Created).UTC()
		entries = append(entries, parsePart(msg.Role, partJSON.String, ts)...)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, rowsRead, nil
}

// countRows reports how many message-part rows a session currently has.
func countRows(db *sql.DB, sessionID string) (int64, error) {
	var n int64
	if err := db.QueryRow(rowCountQuery, sessionID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count rows for session %s: %w", sessionID, err)
	}
	return n, nil
}

// readMetadata extracts the model recorded on the session row.
func readMetadata(db *sql.DB, sessionID string) (*adapterprotocol.SessionMetadata, error) {
	var modelJSON sql.NullString
	err := db.QueryRow("SELECT model FROM session WHERE id = ? LIMIT 1", sessionID).Scan(&modelJSON)
	if err != nil {
		return nil, err
	}
	if !modelJSON.Valid || modelJSON.String == "" {
		return nil, nil
	}

	var m ocSessionModel
	if err := json.Unmarshal([]byte(modelJSON.String), &m); err != nil || m.ID == "" {
		return nil, nil
	}
	return &adapterprotocol.SessionMetadata{Model: m.ID}, nil
}

// parsePart converts one OpenCode part into ox entries. A completed tool part
// yields two — the call and its result — because ox records them separately.
func parsePart(role, partJSON string, ts time.Time) []adapterprotocol.RawEntry {
	var part ocPartData
	if err := json.Unmarshal([]byte(partJSON), &part); err != nil {
		return nil
	}

	switch part.Type {
	case "text":
		if part.Text == "" {
			return nil
		}
		return []adapterprotocol.RawEntry{makeEntry(role, ts, part.Text)}

	case "tool":
		return parseToolPart(part, ts)

	case "reasoning":
		// thinking content — not user-visible, skip
		return nil

	default:
		// step-start, step-finish, patch, and any future part type
		return nil
	}
}

func parseToolPart(part ocPartData, ts time.Time) []adapterprotocol.RawEntry {
	if part.State == nil {
		return nil
	}

	entries := []adapterprotocol.RawEntry{
		adapterruntime.ToolUseWithID(ts, part.Tool, string(part.State.Input), part.CallID),
	}

	switch part.State.Status {
	case "completed":
		entries = append(entries, adapterruntime.ToolResultWithID(ts, part.State.Output, false, part.CallID))
	case "error":
		output := part.State.Error
		if output == "" {
			output = part.State.Output
		}
		entries = append(entries, adapterruntime.ToolResultWithID(ts, output, true, part.CallID))
	}
	// pending/running tools have no result yet — the next poll picks it up

	return entries
}

func makeEntry(role string, ts time.Time, content string) adapterprotocol.RawEntry {
	switch role {
	case "user":
		return adapterruntime.UserEntry(ts, content)
	case "assistant":
		return adapterruntime.AssistantEntry(ts, content)
	case "system":
		return adapterruntime.SystemEntry(ts, content)
	default:
		return adapterruntime.AssistantEntry(ts, content)
	}
}

// extractSessionID parses the session ID from the virtual session file path.
// Format: "opencode:<session-id>"
func extractSessionID(sessionFile string) string {
	const prefix = "opencode:"
	if len(sessionFile) > len(prefix) && sessionFile[:len(prefix)] == prefix {
		return sessionFile[len(prefix):]
	}
	return ""
}
