// session.go handles OpenCode session reading from SQLite.
//
// OpenCode stores sessions in ~/.local/share/opencode/opencode.db with two
// tables: sessions (id, title, created_at, updated_at) and messages (id,
// session_id, role, parts JSON, model, created_at). The parts column is a
// JSON array of {type, data} wrappers where type is one of: text, tool_call,
// tool_result, reasoning, finish.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // +1.9MB to binary; pure-Go SQLite needed because OpenCode stores sessions in SQLite

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- OpenCode message schema ---

type ocPartWrapper struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type ocTextContent struct {
	Text string `json:"text"`
}

type ocToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type ocToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// --- database helpers ---

func openDB() (*sql.DB, error) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return nil, fmt.Errorf("opencode data directory not found")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("opencode.db not found at %s", dbPath)
	}
	// open read-only with WAL mode for safe concurrent access
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
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

	var sessionID string

	if p.AgentSessionID != "" {
		// direct lookup by session ID
		err = db.QueryRow("SELECT id FROM sessions WHERE id = ? LIMIT 1", p.AgentSessionID).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("session %s not found", p.AgentSessionID)
		}
		if err != nil {
			return nil, fmt.Errorf("query session: %w", err)
		}
	} else {
		// find most recent session, optionally filtered by time
		query := "SELECT id FROM sessions WHERE parent_session_id IS NULL ORDER BY created_at DESC LIMIT 1"
		args := []any{}

		if p.Since != "" {
			t, err := time.Parse(time.RFC3339, p.Since)
			if err != nil {
				return nil, fmt.Errorf("invalid since %q: %w", p.Since, err)
			}
			query = "SELECT id FROM sessions WHERE parent_session_id IS NULL AND created_at >= ? ORDER BY created_at DESC LIMIT 1"
			args = append(args, t.Unix())
		}

		err = db.QueryRow(query, args...).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no opencode sessions found")
		}
		if err != nil {
			return nil, fmt.Errorf("query sessions: %w", err)
		}
	}

	// for offset-based reading, count current messages as offset
	var msgCount int64
	if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", sessionID).Scan(&msgCount); err != nil {
		return nil, fmt.Errorf("count messages for session %s: %w", sessionID, err)
	}

	// session file is the DB path + session ID (virtual path for the protocol)
	sessionFile := fmt.Sprintf("opencode:%s", sessionID)

	return &adapterprotocol.FindSessionResult{
		SessionFile: sessionFile,
		Offset:      msgCount,
	}, nil
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

	entries, err := readMessages(db, sessionID, 0)
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

// handleReadFromOffset reads messages added since the last offset (message count).
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

	entries, err := readMessages(db, sessionID, p.Offset)
	if err != nil {
		return nil, err
	}

	// new offset = old offset + new entries read
	var totalMsgs int64
	if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", sessionID).Scan(&totalMsgs); err != nil {
		return nil, fmt.Errorf("count messages for session %s: %w", sessionID, err)
	}

	return &adapterprotocol.ReadFromOffsetResult{
		Entries:   entries,
		NewOffset: totalMsgs,
	}, nil
}

// --- internal helpers ---

// readMessages reads messages from the database, optionally skipping the first `offset` rows.
func readMessages(db *sql.DB, sessionID string, offset int64) ([]adapterprotocol.RawEntry, error) {
	query := "SELECT role, parts, model, created_at FROM messages WHERE session_id = ? ORDER BY id ASC"
	args := []any{sessionID}

	if offset > 0 {
		query += " LIMIT -1 OFFSET ?"
		args = append(args, offset)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []adapterprotocol.RawEntry

	for rows.Next() {
		var role, partsJSON string
		var model sql.NullString
		var createdAt int64

		if err := rows.Scan(&role, &partsJSON, &model, &createdAt); err != nil {
			return nil, fmt.Errorf("scan message row for session %s: %w", sessionID, err)
		}

		ts := time.Unix(createdAt, 0).UTC()
		parsed := parseMessageParts(role, partsJSON, ts)
		entries = append(entries, parsed...)
	}

	return entries, rows.Err()
}

// readMetadata extracts model from the most recent assistant message.
func readMetadata(db *sql.DB, sessionID string) (*adapterprotocol.SessionMetadata, error) {
	var model sql.NullString
	err := db.QueryRow(
		"SELECT model FROM messages WHERE session_id = ? AND model IS NOT NULL AND model != '' ORDER BY created_at DESC LIMIT 1",
		sessionID,
	).Scan(&model)
	if err != nil || !model.Valid {
		return nil, err
	}
	return &adapterprotocol.SessionMetadata{Model: model.String}, nil
}

// parseMessageParts converts OpenCode's parts JSON into ox RawEntry slice.
// A single OpenCode message can produce multiple entries (e.g., text + tool calls).
func parseMessageParts(role, partsJSON string, ts time.Time) []adapterprotocol.RawEntry {
	var parts []ocPartWrapper
	if err := json.Unmarshal([]byte(partsJSON), &parts); err != nil {
		// fallback: treat as plain text
		if role == "user" || role == "assistant" {
			e := makeEntry(role, ts, partsJSON)
			return []adapterprotocol.RawEntry{e}
		}
		return nil
	}

	var entries []adapterprotocol.RawEntry

	for _, p := range parts {
		switch p.Type {
		case "text":
			var tc ocTextContent
			if json.Unmarshal(p.Data, &tc) == nil && tc.Text != "" {
				entries = append(entries, makeEntry(role, ts, tc.Text))
			}

		case "tool_call":
			var tc ocToolCall
			if json.Unmarshal(p.Data, &tc) == nil {
				e := adapterruntime.ToolUseWithID(ts, tc.Name, tc.Input, tc.ID)
				entries = append(entries, e)
			}

		case "tool_result":
			var tr ocToolResult
			if json.Unmarshal(p.Data, &tr) == nil {
				e := adapterruntime.ToolResultWithID(ts, tr.Content, tr.IsError, tr.ToolCallID)
				entries = append(entries, e)
			}

		case "reasoning":
			// reasoning/thinking content — skip, not user-visible

		case "finish":
			// session end marker — skip
		}
	}

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
