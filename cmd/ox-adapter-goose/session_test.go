package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// newFixtureDB builds a throwaway sessions.db matching Goose's real schema.
// Only the columns this adapter reads are declared; Goose has many more.
func newFixtureDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema := `
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	working_dir TEXT NOT NULL,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	provider_name TEXT,
	model_config_json TEXT
);
CREATE TABLE messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id TEXT,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	role TEXT NOT NULL,
	content_json TEXT NOT NULL,
	created_timestamp INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	return db
}

func insertSession(t *testing.T, db *sql.DB, id, workingDir, updatedAt string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO sessions (id, working_dir, updated_at, provider_name, model_config_json)
		 VALUES (?, ?, ?, 'anthropic', '{"model":"claude-opus-5"}')`,
		id, workingDir, updatedAt,
	)
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func insertMessage(t *testing.T, db *sql.DB, sessionID, role, contentJSON string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES (?, ?, ?, ?)`,
		sessionID, role, contentJSON, time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestParseContentBlocks_AllBlockTypes covers every block type Goose emits.
// thinking and image produce no entry by design: reasoning is not part of the
// record, and a binary payload has no useful text form.
func TestParseContentBlocks_AllBlockTypes(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()

	tests := []struct {
		name        string
		role        string
		content     string
		wantEntries int
		wantFirst   string
	}{
		{
			name:        "text block",
			role:        "user",
			content:     `[{"type":"text","text":"hello"}]`,
			wantEntries: 1,
			wantFirst:   "hello",
		},
		{
			name:        "thinking block is dropped",
			role:        "assistant",
			content:     `[{"type":"thinking","thinking":"internal reasoning"}]`,
			wantEntries: 0,
		},
		{
			name:        "image block is dropped",
			role:        "assistant",
			content:     `[{"type":"image","data":"base64..."}]`,
			wantEntries: 0,
		},
		{
			name:        "tool request",
			role:        "assistant",
			content:     `[{"type":"toolRequest","id":"call_1","toolCall":{"status":"success","value":{"name":"developer__shell","arguments":{"command":"ls"}}}}]`,
			wantEntries: 1,
		},
		{
			name:        "tool response",
			role:        "assistant",
			content:     `[{"type":"toolResponse","id":"call_1","toolResult":{"status":"success","value":"file.txt"}}]`,
			wantEntries: 1,
		},
		{
			name:        "text and tool request in one message",
			role:        "assistant",
			content:     `[{"type":"text","text":"running it"},{"type":"toolRequest","id":"c1","toolCall":{"status":"success","value":{"name":"developer__shell","arguments":{}}}}]`,
			wantEntries: 2,
			wantFirst:   "running it",
		},
		{
			name:        "empty text block is dropped",
			role:        "user",
			content:     `[{"type":"text","text":""}]`,
			wantEntries: 0,
		},
		{
			name:        "unknown block type is ignored, not fatal",
			role:        "assistant",
			content:     `[{"type":"someFutureBlock","payload":1},{"type":"text","text":"still here"}]`,
			wantEntries: 1,
			wantFirst:   "still here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseContentBlocks(tt.role, tt.content, ts)
			if len(got) != tt.wantEntries {
				t.Fatalf("got %d entries, want %d: %+v", len(got), tt.wantEntries, got)
			}
			if tt.wantFirst != "" && !strings.Contains(entryText(got[0]), tt.wantFirst) {
				t.Errorf("first entry = %q, want it to contain %q", entryText(got[0]), tt.wantFirst)
			}
		})
	}
}

// TestParseContentBlocks_MalformedKeepsTurn asserts that unparseable content
// still yields an entry. Dropping it would silently lose a turn from the
// recorded transcript, which is worse than recording the raw payload.
func TestParseContentBlocks_MalformedKeepsTurn(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()

	got := parseContentBlocks("user", `this is not json`, ts)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 — a malformed turn must not vanish", len(got))
	}
	if !strings.Contains(entryText(got[0]), "this is not json") {
		t.Errorf("entry = %q, want the raw payload preserved", entryText(got[0]))
	}

	// A system role with malformed content has no user-visible turn to save.
	if got := parseContentBlocks("tool", `not json`, ts); len(got) != 0 {
		t.Errorf("non-conversational role should yield no entry, got %d", len(got))
	}
}

// TestToolRequestFields_FailedCallIsSkipped covers the failure envelope. Goose
// omits `value` when a tool request fails, so a naive reader would emit a
// tool-use entry with an empty name.
func TestToolRequestFields_FailedCallIsSkipped(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()

	content := `[{"type":"toolRequest","id":"c1","toolCall":{"status":"error","error":"tool not found"}}]`
	if got := parseContentBlocks("assistant", content, ts); len(got) != 0 {
		t.Errorf("failed tool request should yield no entry, got %d: %+v", len(got), got)
	}
}

// TestToolResponseFields_ErrorStatus asserts a failed response is flagged as an
// error rather than recorded as a normal result.
func TestToolResponseFields_ErrorStatus(t *testing.T) {
	b := gooseBlock{
		Type:     "toolResponse",
		ToolResp: &gooseToolResp{Status: "error", Error: "permission denied"},
	}
	content, isErr := toolResponseFields(b)
	if !isErr {
		t.Error("non-success status must be reported as an error")
	}
	if content != "permission denied" {
		t.Errorf("content = %q, want the error text", content)
	}

	ok := gooseBlock{
		Type:     "toolResponse",
		ToolResp: &gooseToolResp{Status: "success", Value: []byte(`"done"`)},
	}
	if _, isErr := toolResponseFields(ok); isErr {
		t.Error("success status must not be reported as an error")
	}
}

// TestReadMessages_OffsetIsExclusive is the core incremental-read contract:
// reading from a watermark must return only rows ABOVE it. An off-by-one here
// either duplicates the last entry on every hook or silently drops one.
func TestReadMessages_OffsetIsExclusive(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "s1", "/repo", "2026-07-30 10:00:00")

	id1 := insertMessage(t, db, "s1", "user", `[{"type":"text","text":"first"}]`)
	insertMessage(t, db, "s1", "assistant", `[{"type":"text","text":"second"}]`)

	all, _, err := readMessages(db, "s1", 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("full read = %d entries, want 2", len(all))
	}

	rest, _, err := readMessages(db, "s1", id1)
	if err != nil {
		t.Fatalf("readMessages from offset: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("read from offset %d = %d entries, want 1", id1, len(rest))
	}
	if !strings.Contains(entryText(rest[0]), "second") {
		t.Errorf("entry = %q, want the message after the watermark", entryText(rest[0]))
	}
}

// TestMaxMessageID_EmptySessionIsZero covers a session created but not yet
// written to. MAX() over no rows returns SQL NULL, which must not scan-error.
func TestMaxMessageID_EmptySessionIsZero(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "empty", "/repo", "2026-07-30 10:00:00")

	got, err := maxMessageID(db, "empty")
	if err != nil {
		t.Fatalf("maxMessageID on empty session: %v", err)
	}
	if got != 0 {
		t.Errorf("maxMessageID = %d, want 0", got)
	}
}

// TestMaxMessageID_UsesRowidNotCount is why this adapter does not copy
// OpenCode's COUNT(*) offset: after a delete, a count-based watermark points at
// a row that already streamed, so the next read replays it.
func TestMaxMessageID_UsesRowidNotCount(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "s1", "/repo", "2026-07-30 10:00:00")

	insertMessage(t, db, "s1", "user", `[{"type":"text","text":"a"}]`)
	id2 := insertMessage(t, db, "s1", "user", `[{"type":"text","text":"b"}]`)
	id3 := insertMessage(t, db, "s1", "user", `[{"type":"text","text":"c"}]`)

	if _, err := db.Exec("DELETE FROM messages WHERE id = ?", id2); err != nil {
		t.Fatalf("delete message: %v", err)
	}

	got, err := maxMessageID(db, "s1")
	if err != nil {
		t.Fatalf("maxMessageID: %v", err)
	}
	if got != id3 {
		t.Errorf("maxMessageID = %d, want %d (rowid watermark, not row count)", got, id3)
	}

	// Demonstrate the divergence the rowid watermark avoids. After the delete
	// there are 2 rows, so a COUNT(*)-based watermark would be 2 — and reading
	// past 2 replays message 3, which already streamed. The MAX(id) watermark
	// is 3, and reading past it correctly yields nothing.
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = 's1'").Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 2 {
		t.Fatalf("fixture count = %d, want 2", count)
	}

	if replayed, _, _ := readMessages(db, "s1", count); len(replayed) != 1 {
		t.Errorf("count-based watermark should replay 1 entry (that is the bug), got %d", len(replayed))
	}
	if fresh, _, _ := readMessages(db, "s1", got); len(fresh) != 0 {
		t.Errorf("rowid watermark must replay nothing, got %d", len(fresh))
	}
}

// TestResolveSessionID_PrefersWorkingDir asserts repo correlation actually
// filters. Without it, recording in repo A could attach repo B's transcript.
func TestResolveSessionID_PrefersWorkingDir(t *testing.T) {
	db := newFixtureDB(t)
	// The other repo's session is NEWER, so a "most recent session" strategy
	// with no repo filter would pick the wrong one.
	insertSession(t, db, "mine", "/repo/a", "2026-07-30 10:00:00")
	insertSession(t, db, "theirs", "/repo/b", "2026-07-30 23:00:00")

	got, err := resolveSessionID(db, "", "/repo/a", "")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "mine" {
		t.Errorf("resolveSessionID = %q, want mine — working_dir must win over recency", got)
	}
}

// TestResolveSessionID_MatchesSubdirectory covers Goose being launched from a
// directory below the repo root, which records the subdirectory as working_dir.
func TestResolveSessionID_MatchesSubdirectory(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "sub", filepath.Join("/repo", "internal", "pkg"), "2026-07-30 10:00:00")

	got, err := resolveSessionID(db, "", "/repo", "")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "sub" {
		t.Errorf("resolveSessionID = %q, want sub", got)
	}
}

// TestResolveSessionID_ExplicitIDWins asserts the hook payload's session_id
// beats any heuristic — it is the only signal that is actually authoritative.
func TestResolveSessionID_ExplicitIDWins(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "older", "/repo", "2026-07-30 10:00:00")
	insertSession(t, db, "newer", "/repo", "2026-07-30 23:00:00")

	got, err := resolveSessionID(db, "older", "/repo", "")
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "older" {
		t.Errorf("resolveSessionID = %q, want older", got)
	}

	if _, err := resolveSessionID(db, "does-not-exist", "/repo", ""); err == nil {
		t.Error("an unknown explicit session ID must error, not silently fall back")
	}
}

// TestResolveSessionID_EscapesLikeWildcards — `_` and `%` are LIKE wildcards and
// both are ordinary characters in real repo paths. Unescaped, `/repo/my_app`
// also matches `/repo/myXapp/...`, and another repo's transcript would be
// attributed to this Ledger.
func TestResolveSessionID_EscapesLikeWildcards(t *testing.T) {
	db := newFixtureDB(t)
	// Decoy is NEWER, so if the wildcard leaks it wins on the ORDER BY.
	insertSession(t, db, "decoy", filepath.Join("/repo", "myXapp", "sub"), "2026-07-30 23:00:00")

	_, err := resolveSessionID(db, "", filepath.Join("/repo", "my_app"), "")
	if err == nil {
		t.Fatal("`_` leaked as a LIKE wildcard: matched a different repo's session")
	}

	// The real subdirectory match must still work with the escape in place.
	insertSession(t, db, "mine", filepath.Join("/repo", "my_app", "internal"), "2026-07-30 10:00:00")
	got, err := resolveSessionID(db, "", filepath.Join("/repo", "my_app"), "")
	if err != nil {
		t.Fatalf("escaped prefix broke legitimate subdirectory matching: %v", err)
	}
	if got != "mine" {
		t.Errorf("resolveSessionID = %q, want mine", got)
	}
}

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/repo/plain", "/repo/plain"},
		{"/repo/my_app", `/repo/my\_app`},
		{"/repo/100%", `/repo/100\%`},
		{`/repo/back\slash`, `/repo/back\\slash`},
	}
	for _, tt := range tests {
		if got := escapeLike(tt.in); got != tt.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestReadFromOffset_WatermarkComesFromRowsRead is the durability property.
// Goose writes to this database while ox reads it. If the watermark were a
// separate MAX(id) query, a row inserted between the two queries would not be
// in the returned entries but the watermark would move past it — that turn
// would be dropped from the Ledger permanently.
func TestReadFromOffset_WatermarkComesFromRowsRead(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "s1", "/repo", "2026-07-30 10:00:00")

	id1 := insertMessage(t, db, "s1", "user", `[{"type":"text","text":"first"}]`)

	entries, watermark, err := readMessages(db, "s1", 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if watermark != id1 {
		t.Fatalf("watermark = %d, want %d (the last row actually read)", watermark, id1)
	}

	// Simulate the concurrent write that lands after the SELECT.
	id2 := insertMessage(t, db, "s1", "assistant", `[{"type":"text","text":"second"}]`)

	// A MAX(id) watermark would already be id2 here, skipping the message.
	maxNow, err := maxMessageID(db, "s1")
	if err != nil {
		t.Fatalf("maxMessageID: %v", err)
	}
	if maxNow != id2 {
		t.Fatalf("fixture setup wrong: maxMessageID = %d, want %d", maxNow, id2)
	}

	// Reading from the row-derived watermark still returns the concurrent write.
	rest, _, err := readMessages(db, "s1", watermark)
	if err != nil {
		t.Fatalf("readMessages from watermark: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("concurrent write was lost: got %d entries from watermark %d, want 1", len(rest), watermark)
	}
	if !strings.Contains(entryText(rest[0]), "second") {
		t.Errorf("entry = %q, want the concurrently inserted message", entryText(rest[0]))
	}
}

// TestReadMessages_EmptyReadKeepsWatermark — a drain with nothing new must not
// reset the caller's position to zero.
func TestReadMessages_EmptyReadKeepsWatermark(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "s1", "/repo", "2026-07-30 10:00:00")
	id1 := insertMessage(t, db, "s1", "user", `[{"type":"text","text":"only"}]`)

	entries, watermark, err := readMessages(db, "s1", id1)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0", len(entries))
	}
	if watermark != id1 {
		t.Errorf("watermark = %d, want %d preserved across an empty read", watermark, id1)
	}
}

// TestParseContentBlocks_ToolResponseWithoutPayload — a missing or renamed
// toolResult field would otherwise emit an entry with empty output and no
// CallID, which cannot be correlated back to its request.
func TestParseContentBlocks_ToolResponseWithoutPayload(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()

	got := parseContentBlocks("assistant", `[{"type":"toolResponse","id":"c1"}]`, ts)
	if len(got) != 0 {
		t.Errorf("toolResponse with no payload should yield no entry, got %d: %+v", len(got), got)
	}
}

func TestResolveSessionID_NoMatchErrors(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "elsewhere", "/other/repo", "2026-07-30 10:00:00")

	_, err := resolveSessionID(db, "", "/repo", "")
	if err == nil {
		t.Fatal("expected an error when no session matches the repo")
	}
	if !strings.Contains(err.Error(), "/repo") {
		t.Errorf("error = %q, want it to name the repo it searched", err)
	}
}

func TestReadMetadata_ModelFromConfigJSON(t *testing.T) {
	db := newFixtureDB(t)
	insertSession(t, db, "s1", "/repo", "2026-07-30 10:00:00")

	meta, err := readMetadata(db, "s1")
	if err != nil {
		t.Fatalf("readMetadata: %v", err)
	}
	if meta == nil || meta.Model != "claude-opus-5" {
		t.Errorf("metadata = %+v, want model claude-opus-5", meta)
	}
}

// TestReadMetadata_FallsBackToProvider covers a session whose model_config_json
// is absent or unparseable — the provider name is still better than nothing.
func TestReadMetadata_FallsBackToProvider(t *testing.T) {
	db := newFixtureDB(t)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, working_dir, updated_at, provider_name, model_config_json)
		 VALUES ('s1', '/repo', '2026-07-30 10:00:00', 'ollama', 'not json')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	meta, err := readMetadata(db, "s1")
	if err != nil {
		t.Fatalf("readMetadata: %v", err)
	}
	if meta == nil || meta.Model != "ollama" {
		t.Errorf("metadata = %+v, want fallback to provider name", meta)
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"goose:abc-123", "abc-123"},
		{"goose:", ""},
		{"opencode:abc", ""},
		{"/some/path.jsonl", ""},
		{"", ""},
		{"goosesomething", ""},
	}

	for _, tt := range tests {
		if got := extractSessionID(tt.in); got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// entryText pulls the human-readable payload out of a RawEntry for assertions.
func entryText(e adapterprotocol.RawEntry) string {
	return e.Content
}
