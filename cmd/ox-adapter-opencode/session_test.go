package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// The DDL below is copied from a real ~/.local/share/opencode/opencode.db
// (sqlite_master). Fixtures that encode a schema we invented are how the
// previous reader shipped querying tables OpenCode never had — so these tests
// build the real thing and read from it.
const fixtureDDL = `
CREATE TABLE session (
	id text PRIMARY KEY,
	project_id text NOT NULL,
	parent_id text,
	directory text NOT NULL,
	title text NOT NULL,
	version text NOT NULL,
	model text,
	time_created integer NOT NULL,
	time_updated integer NOT NULL
);
CREATE TABLE message (
	id text PRIMARY KEY,
	session_id text NOT NULL,
	time_created integer NOT NULL,
	time_updated integer NOT NULL,
	data text NOT NULL
);
CREATE TABLE part (
	id text PRIMARY KEY,
	message_id text NOT NULL,
	session_id text NOT NULL,
	time_created integer NOT NULL,
	time_updated integer NOT NULL,
	data text NOT NULL
);
`

const (
	fixtureSessionID = "ses_096fdea05ffeagb7OGYpJCte6a"
	fixtureCreatedMS = 1784173172218
	fixtureDirectory = "/Users/dev/project"
)

// newFixtureDB builds a database with the real OpenCode schema and one
// two-turn conversation: a user message with text, and an assistant message
// with text plus a completed tool call.
func newFixtureDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(fixtureDDL); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed fixture (%s): %v", query, err)
		}
	}

	exec(`INSERT INTO session (id, project_id, parent_id, directory, title, version, model, time_created, time_updated)
		VALUES (?, 'global', NULL, ?, 'Fixture session', '1.18.15', ?, ?, ?)`,
		fixtureSessionID, fixtureDirectory,
		`{"id":"claude-opus-5","providerID":"anthropic"}`,
		fixtureCreatedMS, fixtureCreatedMS)

	exec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_001", fixtureSessionID, fixtureCreatedMS, fixtureCreatedMS,
		`{"role":"user","time":{"created":1784173172218}}`)
	exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		"prt_001", "msg_001", fixtureSessionID, fixtureCreatedMS, fixtureCreatedMS,
		`{"type":"text","text":"deploy the reporting agent"}`)

	exec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		"msg_002", fixtureSessionID, fixtureCreatedMS+1000, fixtureCreatedMS+1000,
		`{"role":"assistant","time":{"created":1784173173218},"modelID":"claude-opus-5","providerID":"anthropic"}`)
	exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		"prt_002", "msg_002", fixtureSessionID, fixtureCreatedMS+1000, fixtureCreatedMS+1000,
		`{"type":"step-start","snapshot":"abc123"}`)
	exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		"prt_003", "msg_002", fixtureSessionID, fixtureCreatedMS+1000, fixtureCreatedMS+1000,
		`{"type":"text","text":"I'll deploy it now."}`)
	exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		"prt_004", "msg_002", fixtureSessionID, fixtureCreatedMS+1000, fixtureCreatedMS+1000,
		`{"type":"tool","callID":"tooluse_abc","tool":"bash","state":{"status":"completed","input":{"command":"make deploy"},"output":"deployed","title":"Deploy"}}`)
	exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)`,
		"prt_005", "msg_002", fixtureSessionID, fixtureCreatedMS+1000, fixtureCreatedMS+1000,
		`{"type":"reasoning","text":"the user wants a deploy"}`)

	return db
}

// TestReadMessages_RealSchema is the gate: a database shaped like OpenCode's
// must yield the conversation. The previous reader returned a SQL error here.
func TestReadMessages_RealSchema(t *testing.T) {
	db := newFixtureDB(t)

	entries, rowsRead, err := readMessages(db, fixtureSessionID, 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	// 5 joined rows: 1 user part + 4 assistant parts
	if rowsRead != 5 {
		t.Errorf("rowsRead = %d, want 5", rowsRead)
	}

	// step-start and reasoning are dropped; the tool part yields call + result
	want := []struct {
		role    string
		content string
		tool    string
	}{
		{role: "user", content: "deploy the reporting agent"},
		{role: "assistant", content: "I'll deploy it now."},
		{role: "tool", tool: "bash"},
		{role: "tool"},
	}

	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].Role != w.role {
			t.Errorf("entry %d role = %q, want %q", i, entries[i].Role, w.role)
		}
		if w.content != "" && entries[i].Content != w.content {
			t.Errorf("entry %d content = %q, want %q", i, entries[i].Content, w.content)
		}
		if w.tool != "" && entries[i].ToolName != w.tool {
			t.Errorf("entry %d tool = %q, want %q", i, entries[i].ToolName, w.tool)
		}
	}
}

func TestReadMessages_ToolCallAndResultPairing(t *testing.T) {
	db := newFixtureDB(t)

	entries, _, err := readMessages(db, fixtureSessionID, 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	call, result := entries[2], entries[3]

	if call.CallID != "tooluse_abc" || result.CallID != "tooluse_abc" {
		t.Errorf("call/result ids = %q/%q, want both tooluse_abc", call.CallID, result.CallID)
	}
	// input is forwarded verbatim as OpenCode stored it
	var input map[string]any
	if err := json.Unmarshal([]byte(call.ToolInput), &input); err != nil {
		t.Fatalf("tool input is not the raw JSON OpenCode stored: %q", call.ToolInput)
	}
	if input["command"] != "make deploy" {
		t.Errorf("tool input command = %v, want 'make deploy'", input["command"])
	}
	if result.ToolOutput != "deployed" {
		t.Errorf("tool output = %q, want 'deployed'", result.ToolOutput)
	}
	if result.IsError {
		t.Error("completed tool marked as error")
	}
}

func TestReadMessages_TimestampsAreMilliseconds(t *testing.T) {
	db := newFixtureDB(t)

	entries, _, err := readMessages(db, fixtureSessionID, 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}

	ts, err := time.Parse(time.RFC3339, entries[0].Timestamp)
	if err != nil {
		t.Fatalf("parse entry timestamp %q: %v", entries[0].Timestamp, err)
	}
	// RawEntry timestamps are second-precision RFC3339, so compare seconds —
	// treating the millisecond field as seconds lands ~54,000 years out
	if got, want := ts.Unix(), int64(fixtureCreatedMS)/1000; got != want {
		t.Errorf("timestamp = %d (unix s), want %d — millisecond field misread?", got, want)
	}
}

// TestReadMessages_OffsetIsPartGranular pins the incremental contract: a poll
// that resumes at the previous offset must not re-emit what it already read.
func TestReadMessages_OffsetIsPartGranular(t *testing.T) {
	db := newFixtureDB(t)

	total, err := countRows(db, fixtureSessionID)
	if err != nil {
		t.Fatalf("countRows: %v", err)
	}
	if total != 5 {
		t.Fatalf("countRows = %d, want 5", total)
	}

	// resume after the user turn (1 row) — the assistant turn is 4 more rows
	entries, rowsRead, err := readMessages(db, fixtureSessionID, 1)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if rowsRead != 4 {
		t.Errorf("rowsRead = %d, want 4", rowsRead)
	}
	for _, e := range entries {
		if e.Role == "user" {
			t.Errorf("offset read re-emitted the user turn: %+v", e)
		}
	}

	// resuming at the end yields nothing
	entries, rowsRead, err = readMessages(db, fixtureSessionID, total)
	if err != nil {
		t.Fatalf("readMessages at end: %v", err)
	}
	if rowsRead != 0 || len(entries) != 0 {
		t.Errorf("read at end returned %d rows / %d entries, want 0/0", rowsRead, len(entries))
	}
}

func TestReadMetadata_ModelFromSessionRow(t *testing.T) {
	db := newFixtureDB(t)

	meta, err := readMetadata(db, fixtureSessionID)
	if err != nil {
		t.Fatalf("readMetadata: %v", err)
	}
	if meta == nil || meta.Model != "claude-opus-5" {
		t.Fatalf("metadata = %+v, want model claude-opus-5", meta)
	}
}

func TestResolveSessionID_PrefersMatchingDirectory(t *testing.T) {
	db := newFixtureDB(t)

	// a newer session in a different directory must not win when the caller
	// told us which repo it cares about
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, directory, title, version, model, time_created, time_updated)
		VALUES ('ses_other', 'global', NULL, '/Users/dev/elsewhere', 'Other', '1.18.15', NULL, ?, ?)`,
		fixtureCreatedMS+99999, fixtureCreatedMS+99999); err != nil {
		t.Fatalf("seed second session: %v", err)
	}

	got, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: fixtureDirectory})
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != fixtureSessionID {
		t.Errorf("resolved %q, want %q", got, fixtureSessionID)
	}

	// with no repo scope, newest wins
	got, err = resolveSessionID(db, adapterprotocol.FindSessionParams{})
	if err != nil {
		t.Fatalf("resolveSessionID unscoped: %v", err)
	}
	if got != "ses_other" {
		t.Errorf("unscoped resolved %q, want ses_other", got)
	}
}

// TestResolveSessionID_ScopedRepoWithNoSession_DoesNotLeakOtherProject is the
// regression gate for a cross-project data leak: a project-scoped lookup for
// a repo with no session of its own must return "not found", not silently
// fall back to the newest session anywhere in the database. Ledgers are
// shared with teammates, so returning a foreign repo's session here would
// write that repo's conversation content into this repo's Ledger.
//
// Failure prevented: resolveSessionID(RepoRoot: "/repo-a") returning a
// session that belongs to "/repo-b" because "/repo-a" has no session row and
// the code fell through to a global newest-session query.
func TestResolveSessionID_ScopedRepoWithNoSession_DoesNotLeakOtherProject(t *testing.T) {
	db := newFixtureDB(t)

	_, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: "/Users/dev/never-used"})
	if err == nil {
		t.Fatal("expected a not-found error — the fixture session belongs to a different repo and must never be attributed to this one")
	}
}

// TestResolveSessionID_MatchesSubdirectory verifies a session started in a
// subdirectory of the repo still resolves for a repo-root-scoped query.
// Failure prevented: a repo scoped by its root never finding sessions
// OpenCode recorded from a subdirectory, because "directory = ?" required
// byte-for-byte equality.
func TestResolveSessionID_MatchesSubdirectory(t *testing.T) {
	db := newFixtureDB(t)

	subdir := filepath.Join(fixtureDirectory, "services", "api")
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, directory, title, version, model, time_created, time_updated)
		VALUES ('ses_subdir', 'global', NULL, ?, 'Subdir', '1.18.15', NULL, ?, ?)`,
		subdir, fixtureCreatedMS+99999, fixtureCreatedMS+99999); err != nil {
		t.Fatalf("seed subdirectory session: %v", err)
	}

	got, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: fixtureDirectory})
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "ses_subdir" {
		t.Errorf("resolved %q, want the newer subdirectory session %q", got, "ses_subdir")
	}
}

// TestResolveSessionID_TrailingSeparatorNormalized verifies a RepoRoot with a
// trailing separator still matches a session directory without one.
// Failure prevented: a trailing slash on the caller's RepoRoot silently
// turning a real match into a false "no sessions found."
func TestResolveSessionID_TrailingSeparatorNormalized(t *testing.T) {
	db := newFixtureDB(t)

	got, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: fixtureDirectory + "/"})
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != fixtureSessionID {
		t.Errorf("resolved %q, want %q", got, fixtureSessionID)
	}
}

// TestResolveSessionID_ResolvesSymlinkedRepoRoot verifies a RepoRoot reached
// through a symlink still matches a session whose recorded directory is the
// canonical (symlink-resolved) path — the concrete case CodeRabbit called
// out: /Users/... vs /private/... on macOS never compared equal.
// Failure prevented: a repo whose path traverses a symlink (a symlinked
// $HOME, /tmp vs /private/tmp, ...) never finding its own sessions.
func TestResolveSessionID_ResolvesSymlinkedRepoRoot(t *testing.T) {
	db := newFixtureDB(t)

	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}

	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// OpenCode recorded the canonical, symlink-resolved directory...
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, directory, title, version, model, time_created, time_updated)
		VALUES ('ses_symlinked', 'global', NULL, ?, 'Symlinked', '1.18.15', NULL, ?, ?)`,
		resolvedReal, fixtureCreatedMS, fixtureCreatedMS); err != nil {
		t.Fatalf("seed symlinked session: %v", err)
	}

	// ...but the caller passes the symlinked, unresolved path.
	got, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: link})
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != "ses_symlinked" {
		t.Errorf("resolved %q, want %q", got, "ses_symlinked")
	}
}

func TestResolveSessionID_SkipsChildSessions(t *testing.T) {
	db := newFixtureDB(t)

	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, directory, title, version, model, time_created, time_updated)
		VALUES ('ses_child', 'global', ?, ?, 'Child', '1.18.15', NULL, ?, ?)`,
		fixtureSessionID, fixtureDirectory, fixtureCreatedMS+50000, fixtureCreatedMS+50000); err != nil {
		t.Fatalf("seed child session: %v", err)
	}

	got, err := resolveSessionID(db, adapterprotocol.FindSessionParams{RepoRoot: fixtureDirectory})
	if err != nil {
		t.Fatalf("resolveSessionID: %v", err)
	}
	if got != fixtureSessionID {
		t.Errorf("resolved %q, want the root session %q", got, fixtureSessionID)
	}
}

func TestParsePart_ErroredTool(t *testing.T) {
	ts := time.Unix(0, 0).UTC()
	entries := parsePart("assistant",
		`{"type":"tool","callID":"c1","tool":"bash","state":{"status":"error","error":"exit 1"}}`, ts)

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want call + result", len(entries))
	}
	if !entries[1].IsError || entries[1].ToolOutput != "exit 1" {
		t.Errorf("result = %+v, want IsError with 'exit 1'", entries[1])
	}
}

func TestParsePart_RunningToolHasNoResultYet(t *testing.T) {
	ts := time.Unix(0, 0).UTC()
	entries := parsePart("assistant",
		`{"type":"tool","callID":"c1","tool":"bash","state":{"status":"running","input":{"command":"sleep 1"}}}`, ts)

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want only the call: %+v", len(entries), entries)
	}
}

func TestParsePart_SkippedTypes(t *testing.T) {
	ts := time.Unix(0, 0).UTC()
	for _, raw := range []string{
		`{"type":"step-start","snapshot":"x"}`,
		`{"type":"step-finish","reason":"tool-calls"}`,
		`{"type":"patch","hash":"x","files":[]}`,
		`{"type":"reasoning","text":"thinking"}`,
		`{"type":"text","text":""}`,
		`not json`,
	} {
		if entries := parsePart("assistant", raw, ts); len(entries) != 0 {
			t.Errorf("parsePart(%s) = %+v, want no entries", raw, entries)
		}
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"opencode:abc123", "abc123"},
		{"opencode:", ""},
		{"other:abc123", ""},
		{"", ""},
		{"opencode:" + fixtureSessionID, fixtureSessionID},
	}
	for _, tt := range tests {
		if got := extractSessionID(tt.in); got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
