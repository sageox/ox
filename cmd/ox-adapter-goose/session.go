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
//
// Value's shape varies: for MCP-style tool results it is a
// gooseToolResultValue object; for simpler built-in tools it can be a bare
// JSON scalar. It stays json.RawMessage here and is parsed on demand in
// toolResponseFields, which tries the structured shape first and falls back
// to treating Value as an opaque blob.
type gooseToolResp struct {
	Status string          `json:"status"`
	Value  json.RawMessage `json:"value"`
	Error  string          `json:"error"`
}

// gooseToolResultValue is the nested envelope Goose puts inside
// toolResult.value for MCP-style tool responses. Its isError is the real
// failure signal: Goose keeps the outer toolResult.status as "success" even
// when the underlying command failed, so status alone cannot be trusted.
type gooseToolResultValue struct {
	Content []gooseTextBlock `json:"content"`
	IsError bool             `json:"isError"`
}

// gooseTextBlock is one element of gooseToolResultValue.Content. Only the
// text type contributes to the human-readable tool output; other content
// block types (e.g. images) are not represented here and are skipped.
type gooseTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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

	entries, _, skipped, err := readMessagesWithStats(db, sessionID, 0)
	if err != nil {
		return nil, err
	}

	meta, _ := readMetadata(db, sessionID)

	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta, Skipped: skipped}, nil
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
// readMessages is readMessagesWithStats without the skip count, for the many
// callers that do not need it.
func readMessages(db *sql.DB, sessionID string, afterID int64) ([]adapterprotocol.RawEntry, int64, error) {
	entries, lastID, _, err := readMessagesWithStats(db, sessionID, afterID)
	return entries, lastID, err
}

// readMessagesWithStats also reports how many source blocks were understood and
// deliberately not emitted, so a session that records nothing can be told apart
// from a parser that matches nothing.
func readMessagesWithStats(db *sql.DB, sessionID string, afterID int64) ([]adapterprotocol.RawEntry, int64, int, error) {
	rows, err := db.Query(
		"SELECT id, role, content_json, created_timestamp FROM messages WHERE session_id = ? AND id > ? ORDER BY id ASC",
		sessionID, afterID,
	)
	if err != nil {
		return nil, afterID, 0, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []adapterprotocol.RawEntry
	lastID := afterID
	skipped := 0

	for rows.Next() {
		var rowID, createdTS int64
		var role, contentJSON string

		if err := rows.Scan(&rowID, &role, &contentJSON, &createdTS); err != nil {
			return nil, afterID, 0, fmt.Errorf("scan message row for session %s: %w", sessionID, err)
		}

		// Goose migrated from Unix seconds to milliseconds; detect by magnitude.
		var ts time.Time
		if createdTS > 10_000_000_000 {
			ts = time.UnixMilli(createdTS).UTC()
		} else {
			ts = time.Unix(createdTS, 0).UTC()
		}
		parsed, dropped := parseContentBlocks(role, contentJSON, ts)
		entries = append(entries, parsed...)
		skipped += dropped
		lastID = rowID
	}

	if err := rows.Err(); err != nil {
		return nil, afterID, 0, err
	}
	return entries, lastID, skipped, nil
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
func parseContentBlocks(role, contentJSON string, ts time.Time) ([]adapterprotocol.RawEntry, int) {
	var blocks []gooseBlock
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		// Malformed or unexpected shape: keep the turn rather than dropping it.
		if role == "user" || role == "assistant" {
			return []adapterprotocol.RawEntry{makeEntry(role, ts, contentJSON)}, 0
		}
		return nil, 0
	}

	var entries []adapterprotocol.RawEntry
	skipped := 0

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				skipped++
				continue
			}
			entries = append(entries, makeEntry(role, ts, b.Text))

		case "toolRequest":
			name, args := toolRequestFields(b)
			if name == "" {
				skipped++
				continue
			}
			entries = append(entries, adapterruntime.ToolUseWithID(ts, name, args, b.ID))

		case "toolResponse":
			if b.ToolResp == nil {
				// No payload — a renamed or missing field in a future Goose
				// version. Emitting an entry here would produce empty output
				// with no CallID, uncorrelatable to its request.
				skipped++
				continue
			}
			content, isErr := toolResponseFields(b)
			entries = append(entries, adapterruntime.ToolResultWithID(ts, content, isErr, b.ID))

		case "thinking":
			// Reasoning content is deliberately never recorded, for any agent.
			// It is where a model quotes its own system prompt back, and a
			// Ledger is shared with teammates. Counted, not emitted.
			skipped++

		case "image":
			// Binary payload; nothing useful to put in a text transcript.
			skipped++

		default:
			// a block type this adapter does not model — count it so a future
			// Goose format change shows up as a rising skip count rather than
			// a quietly shrinking transcript
			skipped++
		}
	}

	return entries, skipped
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

// toolResponseFields returns the human-readable response text and whether it
// represents a failure.
//
// value.isError is the authoritative failure signal: in every observed real
// failed-command response, Goose leaves the outer toolResult.status as
// "success" and nests the actual failure at toolResult.value.isError instead.
// The outer status is only consulted as a fallback, for responses whose value
// is absent, `null`, does not parse as the structured envelope (e.g. a bare
// JSON scalar from a simpler built-in tool), or parses as an object carrying
// neither "content" nor "isError" — none of those are the envelope, and
// treating them as one would silently read a failed call as isError:false.
func toolResponseFields(b gooseBlock) (content string, isError bool) {
	if b.ToolResp == nil {
		return "", false
	}

	if v, ok := parseGooseToolResultValue(b.ToolResp.Value); ok {
		text := joinTextBlocks(v.Content)
		if text == "" {
			// No extractable text (e.g. a content-less or non-text
			// payload) — preserve the raw JSON rather than lose it.
			text = string(b.ToolResp.Value)
		}
		return text, v.IsError
	}

	if b.ToolResp.Status != "" && b.ToolResp.Status != "success" {
		if b.ToolResp.Error != "" {
			return b.ToolResp.Error, true
		}
		return string(b.ToolResp.Value), true
	}
	return string(b.ToolResp.Value), false
}

// parseGooseToolResultValue reports whether raw is genuinely the structured
// envelope Goose writes for MCP-style tool responses — a JSON object
// carrying at least a "content" or "isError" key.
//
// json.Unmarshal into a struct succeeds without error for `null` and for any
// object missing all of the struct's fields, in both cases leaving the
// struct at its zero value (IsError: false). Treating that success as "is
// the envelope" is the bug: a `null` value or a non-envelope object would be
// read as a successful response even when the outer toolResult carries a
// real failure. Requiring at least one recognized key before trusting the
// unmarshal is what makes the envelope check authoritative only when the
// envelope is actually present.
func parseGooseToolResultValue(raw json.RawMessage) (gooseToolResultValue, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return gooseToolResultValue{}, false
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return gooseToolResultValue{}, false
	}
	if _, hasContent := probe["content"]; !hasContent {
		if _, hasIsError := probe["isError"]; !hasIsError {
			return gooseToolResultValue{}, false
		}
	}

	var v gooseToolResultValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return gooseToolResultValue{}, false
	}
	return v, true
}

// joinTextBlocks concatenates the text of every "text"-typed content block,
// in order. Goose sometimes splits one response into several blocks (e.g. a
// command's output plus a separate truncation notice); joining preserves
// both rather than keeping only the first.
func joinTextBlocks(blocks []gooseTextBlock) string {
	var texts []string
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			texts = append(texts, blk.Text)
		}
	}
	return strings.Join(texts, "\n\n")
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
