// session.go handles Goose session reading from SQLite.
//
// Goose stores sessions in ~/.local/share/goose/sessions/sessions.db with two
// tables that matter here:
//
//	sessions(id TEXT PK, name, working_dir TEXT NOT NULL, created_at, updated_at,
//	         provider_name, model_config_json, ...)
//	messages(id INTEGER PK AUTOINCREMENT, session_id, role, content_json,
//	         created_timestamp INTEGER, ...)
//
// content_json is a JSON array of typed blocks: text, thinking, toolRequest,
// toolResponse, image.
//
// Two things here deliberately differ from the OpenCode adapter this is modeled
// on, because Goose's schema supports better:
//
//  1. The incremental offset is MAX(messages.id), not COUNT(*). Goose's
//     AUTOINCREMENT id is a real watermark; a count-based offset silently
//     re-reads or skips rows if any message is ever deleted.
//  2. Sessions are correlated to a repo via sessions.working_dir, an actual
//     column, rather than by guessing "most recent session, no repo filter."
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite; Goose stores sessions in SQLite

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// sessionFilePrefix marks the virtual session handle. Goose sessions are rows,
// not files, so there is no real path to hand back to the protocol.
const sessionFilePrefix = "goose:"

// --- Goose message schema ---

// gooseBlock is one element of a message's content_json array. Only the fields
// ox consumes are decoded; Goose adds block fields over time and unknown ones
// must not break parsing.
type gooseBlock struct {
	Type string `json:"type"`

	// type == "text"
	Text string `json:"text"`

	// type == "toolRequest"
	ID       string          `json:"id"`
	ToolCall *gooseToolCall  `json:"toolCall"`
	ToolResp *gooseToolResp  `json:"toolResult"`
	Raw      json.RawMessage `json:"-"`
}

// gooseToolCall wraps the request in a status/value envelope. On failure Goose
// sets status to something other than "success" and value is absent, so Value
// must be treated as optional.
type gooseToolCall struct {
	Status string             `json:"status"`
	Value  *gooseToolCallBody `json:"value"`
	Error  string             `json:"error"`
}

type gooseToolCallBody struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// gooseToolResp mirrors gooseToolCall for the response side.
type gooseToolResp struct {
	Status string          `json:"status"`
	Value  json.RawMessage `json:"value"`
	Error  string          `json:"error"`
}

// --- database helpers ---

func openDB() (*sql.DB, error) {
	dbPath := gooseDBPath()
	if dbPath == "" {
		return nil, fmt.Errorf("goose data directory not found")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("goose sessions.db not found at %s", dbPath)
	}
	// Read-only with WAL so an in-flight Goose session is never blocked or
	// corrupted by our reads.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open goose sessions.db: %w", err)
	}
	return db, nil
}

// --- session discovery ---

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	sessionID, err := resolveSessionID(db, p.AgentSessionID, p.RepoRoot, p.Since)
	if err != nil {
		return nil, err
	}

	offset, err := maxMessageID(db, sessionID)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.FindSessionResult{
		SessionFile: sessionFilePrefix + sessionID,
		Offset:      offset,
	}, nil
}

// resolveSessionID finds the Goose session to record. A session ID supplied by
// the hook payload always wins; otherwise fall back to the most recently updated
// session whose working_dir matches the repo.
func resolveSessionID(db *sql.DB, agentSessionID, repoRoot, since string) (string, error) {
	if agentSessionID != "" {
		var id string
		err := db.QueryRow("SELECT id FROM sessions WHERE id = ? LIMIT 1", agentSessionID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("session %s not found", agentSessionID)
		}
		if err != nil {
			return "", fmt.Errorf("query session: %w", err)
		}
		return id, nil
	}

	where := []string{"1=1"}
	args := []any{}

	if repoRoot != "" {
		// Exact working_dir first, then any subdirectory of the repo — Goose
		// records the cwd it was launched from, which may be below the root.
		//
		// The prefix MUST be escaped: `_` and `%` are LIKE wildcards and both
		// are ordinary characters in real paths. Unescaped, `/home/u/my_repo`
		// also matches `/home/u/myXrepo/...`, which would attribute another
		// repo's transcript to this Ledger.
		where = append(where, `(working_dir = ? OR working_dir LIKE ? ESCAPE '\')`)
		args = append(args, repoRoot, escapeLike(repoRoot)+string(os.PathSeparator)+"%")
	}

	if since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return "", fmt.Errorf("invalid since %q: %w", since, err)
		}
		where = append(where, "updated_at >= ?")
		args = append(args, t.UTC().Format("2006-01-02 15:04:05"))
	}

	query := "SELECT id FROM sessions WHERE " + strings.Join(where, " AND ") +
		" ORDER BY updated_at DESC LIMIT 1"

	var id string
	err := db.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if repoRoot != "" {
			return "", fmt.Errorf("no goose sessions found for %s", repoRoot)
		}
		return "", fmt.Errorf("no goose sessions found")
	}
	if err != nil {
		return "", fmt.Errorf("query sessions: %w", err)
	}
	return id, nil
}

// escapeLike escapes the SQL LIKE wildcards so a literal path can be used as a
// prefix pattern. Backslash is the ESCAPE character, so it is escaped first.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// maxMessageID returns the highest message rowid for the session, which is the
// incremental-read watermark. A session with no messages yet yields 0.
func maxMessageID(db *sql.DB, sessionID string) (int64, error) {
	var maxID sql.NullInt64
	if err := db.QueryRow("SELECT MAX(id) FROM messages WHERE session_id = ?", sessionID).Scan(&maxID); err != nil {
		return 0, fmt.Errorf("max message id for session %s: %w", sessionID, err)
	}
	if !maxID.Valid {
		return 0, nil
	}
	return maxID.Int64, nil
}

// --- reads ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	sessionID := extractSessionID(p.SessionFile)
	if sessionID == "" {
		return nil, fmt.Errorf("invalid session file: %s (expected %s<session-id>)", p.SessionFile, sessionFilePrefix)
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
	return &adapterprotocol.ReadMetadataResult{Model: meta.Model}, nil
}

// handleReadFromOffset reads messages whose rowid is strictly greater than the
// caller's watermark.
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

	// The watermark is the highest rowid actually read, NOT a fresh MAX(id).
	// Goose writes concurrently; a row inserted between the two queries would
	// otherwise be skipped past and lost from the Ledger forever.
	entries, newOffset, err := readMessages(db, sessionID, p.Offset)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.ReadFromOffsetResult{
		Entries:   entries,
		NewOffset: newOffset,
	}, nil
}

func handleCapturePrior(p adapterprotocol.CapturePriorParams) (*adapterprotocol.CapturePriorResult, error) {
	db, err := openDB()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	sessionID, err := resolveSessionID(db, p.SessionID, p.RepoRoot, "")
	if err != nil {
		return nil, err
	}

	entries, _, err := readMessages(db, sessionID, 0)
	if err != nil {
		return nil, err
	}

	meta, _ := readMetadata(db, sessionID)

	return &adapterprotocol.CapturePriorResult{
		Entries:   entries,
		Metadata:  meta,
		AgentType: adapterName,
		SessionID: sessionID,
	}, nil
}

// --- parsing ---

// readMessages reads messages with rowid > afterID, oldest first. It also
// returns the highest rowid it actually read.
//
// The caller MUST use that returned rowid as the next watermark rather than
// re-querying MAX(id). Goose writes to this database while ox reads it, so a
// row inserted between the SELECT and a separate MAX(id) query would not be in
// entries, yet the watermark would move past it — that turn would be dropped
// from the Ledger permanently.
func readMessages(db *sql.DB, sessionID string, afterID int64) ([]adapterprotocol.RawEntry, int64, error) {
	rows, err := db.Query(
		"SELECT id, role, content_json, created_timestamp FROM messages WHERE session_id = ? AND id > ? ORDER BY id ASC",
		sessionID, afterID,
	)
	if err != nil {
		return nil, afterID, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []adapterprotocol.RawEntry
	lastID := afterID

	for rows.Next() {
		var rowID, createdTS int64
		var role, contentJSON string

		if err := rows.Scan(&rowID, &role, &contentJSON, &createdTS); err != nil {
			return nil, afterID, fmt.Errorf("scan message row for session %s: %w", sessionID, err)
		}

		// Goose migrated from Unix seconds to milliseconds; detect by magnitude.
		var ts time.Time
		if createdTS > 10_000_000_000 {
			ts = time.UnixMilli(createdTS).UTC()
		} else {
			ts = time.Unix(createdTS, 0).UTC()
		}
		entries = append(entries, parseContentBlocks(role, contentJSON, ts)...)
		lastID = rowID
	}

	if err := rows.Err(); err != nil {
		return nil, afterID, err
	}
	return entries, lastID, nil
}

// readMetadata extracts the model from the session row. Goose stores it as a
// JSON blob rather than a plain column.
func readMetadata(db *sql.DB, sessionID string) (*adapterprotocol.SessionMetadata, error) {
	var provider, modelJSON sql.NullString
	err := db.QueryRow(
		"SELECT provider_name, model_config_json FROM sessions WHERE id = ? LIMIT 1",
		sessionID,
	).Scan(&provider, &modelJSON)
	if err != nil {
		return nil, err
	}

	meta := &adapterprotocol.SessionMetadata{}

	if modelJSON.Valid && modelJSON.String != "" {
		var cfg struct {
			Model     string `json:"model"`
			ModelName string `json:"model_name"`
		}
		if json.Unmarshal([]byte(modelJSON.String), &cfg) == nil {
			if cfg.Model != "" {
				meta.Model = cfg.Model
			} else {
				meta.Model = cfg.ModelName
			}
		}
	}

	if meta.Model == "" && provider.Valid {
		meta.Model = provider.String
	}
	if meta.Model == "" {
		return nil, nil
	}
	return meta, nil
}

// parseContentBlocks converts one Goose message's content_json into ox entries.
// A single message can yield several entries (text plus tool calls).
func parseContentBlocks(role, contentJSON string, ts time.Time) []adapterprotocol.RawEntry {
	var blocks []gooseBlock
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		// Malformed or unexpected shape: keep the turn rather than dropping it.
		if role == "user" || role == "assistant" {
			return []adapterprotocol.RawEntry{makeEntry(role, ts, contentJSON)}
		}
		return nil
	}

	var entries []adapterprotocol.RawEntry

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				entries = append(entries, makeEntry(role, ts, b.Text))
			}

		case "toolRequest":
			name, args := toolRequestFields(b)
			if name == "" {
				continue
			}
			entries = append(entries, adapterruntime.ToolUseWithID(ts, name, args, b.ID))

		case "toolResponse":
			if b.ToolResp == nil {
				// No payload — a renamed or missing field in a future Goose
				// version. Emitting an entry here would produce empty output
				// with no CallID, uncorrelatable to its request.
				continue
			}
			content, isErr := toolResponseFields(b)
			entries = append(entries, adapterruntime.ToolResultWithID(ts, content, isErr, b.ID))

		case "thinking":
			// Reasoning content — not user-visible, and not part of the record.

		case "image":
			// Binary payload; nothing useful to put in a text transcript.
		}
	}

	return entries
}

// toolRequestFields pulls the tool name and arguments out of the status/value
// envelope. A failed request carries no value, so both come back empty and the
// caller skips the block.
func toolRequestFields(b gooseBlock) (name, args string) {
	if b.ToolCall == nil || b.ToolCall.Value == nil {
		return "", ""
	}
	return b.ToolCall.Value.Name, string(b.ToolCall.Value.Arguments)
}

// toolResponseFields returns the response body and whether it represents a
// failure. Goose marks failure with a non-"success" status.
func toolResponseFields(b gooseBlock) (content string, isError bool) {
	if b.ToolResp == nil {
		return "", false
	}
	if b.ToolResp.Status != "" && b.ToolResp.Status != "success" {
		if b.ToolResp.Error != "" {
			return b.ToolResp.Error, true
		}
		return string(b.ToolResp.Value), true
	}
	return string(b.ToolResp.Value), false
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

// extractSessionID parses the session ID out of the virtual handle
// "goose:<session-id>". Returns empty for anything else.
func extractSessionID(sessionFile string) string {
	if !strings.HasPrefix(sessionFile, sessionFilePrefix) {
		return ""
	}
	return strings.TrimPrefix(sessionFile, sessionFilePrefix)
}
