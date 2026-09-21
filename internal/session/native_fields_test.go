package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three recording fields added for the trace pilot (epic ox-bfeb.1):
// call_id on tool entries, the native session id list, and the stop time.
//
// Customer failure each guards against: a Claude Code trace (labeled with
// tool_use ids and the agent's session id) that cannot be joined to the
// recording it describes, and a recording with a start but no end.

// TestConvertRawEntries_CarriesCallID: the adapter already extracts the id;
// the converter used to drop it. Both the call and its result must carry
// the same value, and entries without one must omit the field entirely.
func TestConvertRawEntries_CarriesCallID(t *testing.T) {
	ts := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		raw    []adapters.RawEntry
		wantID []string
	}{
		{
			name: "tool call and result share the id",
			raw: []adapters.RawEntry{
				{Timestamp: ts, Role: "tool", ToolName: "Bash", ToolInput: `{"command":"ls"}`, CallID: "toolu_01AbC"},
				{Timestamp: ts.Add(time.Second), Role: "tool", ToolOutput: "file.go", CallID: "toolu_01AbC"},
			},
			wantID: []string{"toolu_01AbC", "toolu_01AbC"},
		},
		{
			name: "entries without an id omit the field",
			raw: []adapters.RawEntry{
				{Timestamp: ts, Role: "user", Content: "hello"},
				{Timestamp: ts, Role: "tool", ToolName: "Read", ToolInput: "x"},
			},
			wantID: []string{"", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := ConvertRawEntries(tt.raw)
			require.Len(t, entries, len(tt.wantID))
			for i, want := range tt.wantID {
				assert.Equal(t, want, entries[i].CallID, "entry %d", i)
			}

			// and the on-disk shape RawWriter produces
			rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
			w, err := NewRawWriter(rawPath, "")
			require.NoError(t, err)
			require.NoError(t, w.WriteEntries(entries))
			require.NoError(t, w.CloseAndSync())

			lines := readRawLines(t, rawPath)
			require.Len(t, lines, len(tt.wantID))
			for i, want := range tt.wantID {
				got, present := lines[i]["call_id"]
				if want == "" {
					assert.False(t, present, "entry %d must omit call_id, got %v", i, got)
				} else {
					assert.Equal(t, want, got, "entry %d", i)
				}
			}
		})
	}
}

// TestClaudeCodeToolPair_YieldsMatchingCallIDs is the customer-facing claim
// from the epic: a Claude Code tool call and its result reach raw.jsonl as two
// entries carrying the same call_id. Built the way the external adapter
// builds them — the protocol entries the ox-adapter-claude-code binary emits
// for a tool_use block and its tool_result block — converted through the
// same functions production uses.
func TestClaudeCodeToolPair_YieldsMatchingCallIDs(t *testing.T) {
	ts := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	// what cmd/ox-adapter-claude-code emits for a tool_use / tool_result pair
	// (adapterruntime.ToolUseWithID / ToolResultWithID), after the external
	// adapter's protocol→internal conversion
	raw := []adapters.RawEntry{
		{Timestamp: ts, Role: "tool", ToolName: "Bash", ToolInput: `{"command":"go test ./..."}`, CallID: "toolu_01XyZ"},
		{Timestamp: ts.Add(2 * time.Second), Role: "tool", ToolOutput: "ok", CallID: "toolu_01XyZ"},
	}
	entries := ConvertRawEntries(raw)
	require.Len(t, entries, 2)
	assert.Equal(t, entries[0].CallID, entries[1].CallID)
	assert.Equal(t, "toolu_01XyZ", entries[0].CallID)
	assert.Equal(t, "Bash", entries[0].ToolName)
	assert.Equal(t, "ok", entries[1].ToolOutput)
}

// TestStampRawCarrier appends one footer record and touches nothing else:
// the bytes already in the file are a byte-for-byte prefix afterwards, the
// reader folds the footer's fields into the metadata (a later footer wins),
// a torn last line is closed before the footer, and a missing file or an
// LFS pointer is refused. Customer failure it guards: a daemon-side finalize
// that cannot learn the native ids or the stop time once .recording.json is
// gone — or, worse, a stamp that corrupts or truncates the recording.
func TestStampRawCarrier(t *testing.T) {
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)
	sessions := []lfs.NativeSession{
		{ID: "cc-1", Source: "startup", FirstSeen: at.Add(-time.Hour), LastSeen: at.Add(-time.Hour)},
		{ID: "cc-2", Source: "clear", FirstSeen: at.Add(-time.Minute), LastSeen: at.Add(-time.Minute)},
	}
	native := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T09:00:00Z","agent_id":"Ox1","session_id":"ses_01950000-0000-7000-8000-000000000001"}}` + "\n"
	imported := `{"_meta":{"schema_version":"1","agent_type":"codex","started_at":"2026-09-21T09:00:00Z"}}` + "\n"
	body := `{"type":"user","content":"hello"}` + "\n" + `{"type":"assistant","content":"hi"}` + "\n"

	for _, tc := range []struct {
		name    string
		before  string
		entries int // parseable entries; a torn line is skipped by the reader
	}{
		{name: "native header", before: native + body, entries: 2},
		{name: "import header", before: imported + body, entries: 2},
		{name: "torn last line", before: native + `{"type":"user","content":"partial"`, entries: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, []byte(tc.before), 0o600))

			require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{NativeSessions: sessions, StoppedAt: at}))

			data, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(data), tc.before), "existing bytes must be untouched")
			rest := strings.TrimPrefix(string(data), tc.before)
			if !strings.HasSuffix(tc.before, "\n") {
				require.True(t, strings.HasPrefix(rest, "\n"), "a torn last line must be closed before the footer")
				rest = strings.TrimPrefix(rest, "\n")
			}
			var footer map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(rest)), &footer), "exactly one JSON line is appended: %q", rest)
			assert.Equal(t, "footer", footer["type"])
			assert.Equal(t, at.Format(time.RFC3339Nano), footer["stopped_at"])
			assert.Equal(t, at.Format(time.RFC3339Nano), footer["closed_at"])

			stored, err := ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.NotNil(t, stored.Meta)
			require.NotNil(t, stored.Meta.StoppedAt)
			assert.True(t, stored.Meta.StoppedAt.Equal(at))
			require.Len(t, stored.Meta.NativeSessions, 2)
			assert.Equal(t, "cc-2", stored.Meta.NativeSessions[1].ID)
			assert.Equal(t, "clear", stored.Meta.NativeSessions[1].Source)
			assert.Len(t, stored.Entries, tc.entries, "footer records are framing, never entries")
			if tc.name == "native header" {
				assert.Equal(t, "Ox1", stored.Meta.AgentID, "header fields survive")
				assert.Equal(t, "ses_01950000-0000-7000-8000-000000000001", stored.Meta.SessionID)
			}

			// a later door with a newer stop time wins; ids are kept when it has none
			require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{StoppedAt: later}))
			stored, err = ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			assert.True(t, stored.Meta.StoppedAt.Equal(later), "the last footer wins per field")
			assert.Len(t, stored.Meta.NativeSessions, 2, "a footer without ids leaves the earlier ids in place")
		})
	}

	t.Run("footers merge per field", func(t *testing.T) {
		rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
		closeFooter := `{"type":"footer","closed_at":"2026-09-21T11:00:00Z","entry_count":2,"exit_reason":"interrupted"}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(native+body+closeFooter), 0o600))
		require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{NativeSessions: sessions, StoppedAt: at}))
		stored, err := ReadSessionFromPath(rawPath)
		require.NoError(t, err)
		assert.Equal(t, "interrupted", stored.Footer["exit_reason"], "fields of the earlier footer survive the carrier")
		assert.Equal(t, float64(2), stored.Footer["entry_count"])
		assert.Equal(t, at.Format(time.RFC3339Nano), stored.Footer["closed_at"], "the later footer wins the fields it carries")
		require.NotNil(t, stored.Meta.StoppedAt)
		assert.True(t, stored.Meta.StoppedAt.Equal(at))
	})

	t.Run("a present empty list overrides, an absent one does not", func(t *testing.T) {
		rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
		headerWithIDs := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T09:00:00Z","native_sessions":[{"id":"cc-old","first_seen":"2026-09-21T09:00:00Z","last_seen":"2026-09-21T09:00:00Z"}]}}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(headerWithIDs+body), 0o600))

		// a door with no ids of its own writes no list at all ...
		require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{NativeSessions: []lfs.NativeSession{}, StoppedAt: at}))
		stored, err := ReadSessionFromPath(rawPath)
		require.NoError(t, err)
		require.Len(t, stored.Meta.NativeSessions, 1, "an omitted list leaves the header's ids alone")
		assert.Equal(t, "cc-old", stored.Meta.NativeSessions[0].ID)

		// ... whereas an explicit empty list on disk is a deliberate override
		f, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(`{"type":"footer","native_sessions":[]}` + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		stored, err = ReadSessionFromPath(rawPath)
		require.NoError(t, err)
		assert.Empty(t, stored.Meta.NativeSessions, "a present empty list overrides the earlier ids")
	})

	t.Run("nothing to carry writes nothing", func(t *testing.T) {
		rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(rawPath, []byte(native+body), 0o600))
		require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{}))
		data, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		assert.Equal(t, native+body, string(data))
	})

	t.Run("missing file is an error", func(t *testing.T) {
		err := StampRawCarrier(filepath.Join(t.TempDir(), "raw.jsonl"), CarrierStamp{StoppedAt: at})
		require.Error(t, err)
	})

	t.Run("LFS pointer is refused", func(t *testing.T) {
		rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(rawPath, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n"), 0o600))
		require.Error(t, StampRawCarrier(rawPath, CarrierStamp{StoppedAt: at}))
		data, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "footer", "a pointer must never be appended to")
	})
}

// TestResolveStoppedAt pins the precedence every finalize door shares:
// requested time > header stamp > last entry timestamp > fallback.
func TestResolveStoppedAt(t *testing.T) {
	requested := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	stamped := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	lastEntry := time.Date(2026, 9, 21, 10, 30, 0, 0, time.UTC)
	fallback := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 9, 21, 10, 45, 0, 0, time.UTC)

	withEntries := "{\"type\":\"user\",\"content\":\"a\",\"timestamp\":\"2026-09-21T10:00:00Z\"}\n{\"type\":\"assistant\",\"content\":\"b\",\"timestamp\":\"2026-09-21T10:30:00Z\"}\n"
	plainHeader := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T09:00:00Z"}}` + "\n"
	stampedHeader := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T09:00:00Z","stopped_at":"2026-09-21T11:00:00Z"}}` + "\n"

	tests := []struct {
		name      string
		requested *time.Time
		raw       string
		want      time.Time
	}{
		{name: "requested wins over everything", requested: &requested, raw: stampedHeader + withEntries, want: requested},
		{name: "header stamp beats last entry", raw: stampedHeader + withEntries, want: stamped},
		{name: "last entry beats fallback", raw: plainHeader + withEntries, want: lastEntry},
		{name: "header only resolves to the file's last write, not the fallback", raw: plainHeader, want: modified},
		{name: "no file falls back", raw: "", want: fallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawPath := ""
			if tt.raw != "" {
				rawPath = filepath.Join(t.TempDir(), "raw.jsonl")
				require.NoError(t, os.WriteFile(rawPath, []byte(tt.raw), 0o600))
				require.NoError(t, os.Chtimes(rawPath, modified, modified))
			}
			got := ResolveStoppedAt(tt.requested, rawPath, fallback)
			assert.True(t, got.Equal(tt.want), "got %s want %s", got, tt.want)
			// a retried upload must land on the same instant, or meta.json
			// changes between attempts and every retry needs a fresh commit
			again := ResolveStoppedAt(tt.requested, rawPath, fallback.Add(time.Hour))
			if tt.raw != "" {
				assert.True(t, again.Equal(got), "retry drifted: first %s then %s", got, again)
			}
		})
	}
}

// TestRecordingState_RecordNativeSession: appends new ids, dedups repeats
// (advancing last_seen, filling an empty source), keeps AgentSessionID on the
// newest id, and ignores empty ids.
func TestRecordingState_RecordNativeSession(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	var state RecordingState

	state.RecordNativeSession("", "startup", t0)
	assert.Empty(t, state.NativeSessions, "agents without an id record an empty list")
	assert.Empty(t, state.AgentSessionID)

	state.RecordNativeSession("sess-a", "", t0)
	state.RecordNativeSession("sess-a", "startup", t0.Add(time.Minute)) // hook safety-net re-report
	state.RecordNativeSession("sess-b", "clear", t0.Add(time.Hour))
	state.RecordNativeSession("sess-b", "compact", t0.Add(2*time.Hour))

	require.Len(t, state.NativeSessions, 2)
	a, b := state.NativeSessions[0], state.NativeSessions[1]
	assert.Equal(t, "sess-a", a.ID)
	assert.Equal(t, "startup", a.Source, "an empty source is filled by the next sighting")
	assert.True(t, a.FirstSeen.Equal(t0))
	assert.True(t, a.LastSeen.Equal(t0.Add(time.Minute)))
	assert.Equal(t, "sess-b", b.ID)
	assert.Equal(t, "clear", b.Source, "the first reason is kept; compact does not overwrite it")
	assert.True(t, b.LastSeen.Equal(t0.Add(2*time.Hour)), "a repeat sighting advances last_seen")
	assert.Equal(t, "sess-b", state.AgentSessionID, "adapter lookups follow the newest id")
}

// TestStartRecording_SeedsNativeSessions: the id that started a recording is
// its first sighting, with the source the caller had; no id means no list.
func TestStartRecording_SeedsNativeSessions(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_native_seed"}`), 0o644))

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxSeed", AdapterName: "claude-code", AgentSessionID: "sess-first", AgentSessionSource: "startup",
	})
	require.NoError(t, err)
	require.Len(t, state.NativeSessions, 1)
	assert.Equal(t, "sess-first", state.NativeSessions[0].ID)
	assert.Equal(t, "startup", state.NativeSessions[0].Source)
	assert.True(t, state.NativeSessions[0].FirstSeen.Equal(state.StartedAt))

	loaded, err := LoadRecordingStateForAgent(projectRoot, "OxSeed")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, loaded.NativeSessions, 1, "the list must round-trip through .recording.json")
	require.NoError(t, ClearRecordingStateForAgent(projectRoot, "OxSeed"))

	noID, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: "OxNoID", AdapterName: "generic"})
	require.NoError(t, err)
	assert.Empty(t, noID.NativeSessions, "no native id is an empty list, not an error")
}

// TestParseStoreMeta_NativeFieldsRoundTrip: the header carrier reads back what
// WriteHeader wrote, and tolerates a malformed list without losing the rest.
func TestParseStoreMeta_NativeFieldsRoundTrip(t *testing.T) {
	stoppedAt := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	meta := &StoreMeta{
		Version: "1.0", CreatedAt: stoppedAt.Add(-time.Hour), AgentID: "Ox1",
		NativeSessions: []lfs.NativeSession{{ID: "s1", Source: "startup", FirstSeen: stoppedAt.Add(-time.Hour), LastSeen: stoppedAt}},
		StoppedAt:      &stoppedAt,
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))

	parsed := ParseStoreMeta(m)
	require.NotNil(t, parsed.StoppedAt)
	assert.True(t, parsed.StoppedAt.Equal(stoppedAt))
	require.Len(t, parsed.NativeSessions, 1)
	assert.Equal(t, "s1", parsed.NativeSessions[0].ID)
	assert.True(t, parsed.NativeSessions[0].LastSeen.Equal(stoppedAt))

	m["native_sessions"] = "not-a-list"
	parsed = ParseStoreMeta(m)
	assert.Empty(t, parsed.NativeSessions)
	assert.Equal(t, "Ox1", parsed.AgentID, "a bad list must not take the rest of the header down")
}

func readRawLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "line: %s", line)
		lines = append(lines, m)
	}
	return lines
}

// TestStampRawCarrier_KeepsEntriesFromAnOpenAppender is the customer failure
// Greptile's T-Rex harness demonstrated on PR #1025: the daemon's tail watcher
// keeps one O_APPEND descriptor on raw.jsonl for the life of a session, and a
// parallel PostToolUse hook may hold another. A carrier stamp that replaces
// the file by rename leaves those writers appending to the unlinked inode, so
// every entry they accept after the stamp is silently lost. The stamp must
// therefore append, never rewrite: an entry written through a descriptor that
// was opened BEFORE the stamp still has to be in the named file afterwards.
func TestStampRawCarrier_KeepsEntriesFromAnOpenAppender(t *testing.T) {
	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T09:00:00Z"}}`+"\n"+
		`{"type":"user","content":"before the stamp"}`+"\n"), 0o600))

	// the watcher's long-lived descriptor, opened before the stamp
	appender, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer appender.Close()

	stoppedAt := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	require.NoError(t, StampRawCarrier(rawPath, CarrierStamp{
		NativeSessions: []lfs.NativeSession{{ID: "cc-race-1", Source: "startup", FirstSeen: stoppedAt, LastSeen: stoppedAt}},
		StoppedAt:      stoppedAt,
	}))

	// an entry the watcher accepted after the stamp
	_, err = appender.WriteString(`{"type":"assistant","content":"after the stamp"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, appender.Sync())

	stored, err := ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	contents := make([]string, 0, len(stored.Entries))
	for _, e := range stored.Entries {
		contents = append(contents, e["content"].(string))
	}
	assert.Equal(t, []string{"before the stamp", "after the stamp"}, contents,
		"an entry appended through a descriptor opened before the stamp must survive the stamp")
	require.NotNil(t, stored.Meta)
	require.NotNil(t, stored.Meta.StoppedAt, "the reader must surface the stamped stop time")
	assert.True(t, stored.Meta.StoppedAt.Equal(stoppedAt))
	require.Len(t, stored.Meta.NativeSessions, 1, "the reader must surface the stamped native ids")
	assert.Equal(t, "cc-race-1", stored.Meta.NativeSessions[0].ID)
}

// TestReadRecordingStateFile: the daemon's finalize reads .recording.json by
// directory, without searching. Missing is nil/nil (the normal case once a
// hook door cleared it), malformed is an error (never a silently empty
// state), present round-trips the carrier fields.
func TestReadRecordingStateFile(t *testing.T) {
	dir := t.TempDir()

	state, err := ReadRecordingStateFile(dir)
	require.NoError(t, err)
	assert.Nil(t, state, "a cleared state file is not an error")

	require.NoError(t, os.WriteFile(filepath.Join(dir, recordingFile), []byte("{not json"), 0o600))
	_, err = ReadRecordingStateFile(dir)
	require.Error(t, err, "a malformed state file must not read as empty")

	// a read failure that is not "missing" (a directory where the file
	// belongs — portable, unlike chmod) is reported, never read as empty
	unreadable := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(unreadable, recordingFile), 0o700))
	_, err = ReadRecordingStateFile(unreadable)
	require.Error(t, err, "an unreadable state file must not read as empty")

	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	want := &RecordingState{AgentID: "OxRead", StoppedAt: &at,
		NativeSessions: []NativeSession{{ID: "cc-read", Source: "startup", FirstSeen: at, LastSeen: at}}}
	data, err := json.Marshal(want)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recordingFile), data, 0o600))
	state, err = ReadRecordingStateFile(dir)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "OxRead", state.AgentID)
	require.NotNil(t, state.StoppedAt)
	assert.True(t, state.StoppedAt.Equal(at))
	require.Len(t, state.NativeSessions, 1)
	assert.Equal(t, "cc-read", state.NativeSessions[0].ID)
}

// TestCapturePriorHistory_KeepsCallID: a planning session imported through
// capture-prior must keep the correlation between a tool call and its
// result across the protocol conversion, the prior-history JSONL round trip,
// and the history <-> session entry conversions. Without it an imported
// session carries tool fields but no way to join a call to its result.
func TestCapturePriorHistory_KeepsCallID(t *testing.T) {
	const callID = "call_prior_01"
	ts := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	history := ConvertProtocolEntriesToHistory([]adapterprotocol.RawEntry{
		{Timestamp: ts, Role: adapterprotocol.RoleUser, Content: "run the tests"},
		{Timestamp: ts, Role: adapterprotocol.RoleTool, ToolName: "bash", ToolInput: "go test ./...", CallID: callID},
		{Timestamp: ts, Role: adapterprotocol.RoleTool, ToolOutput: "ok", CallID: callID},
	}, "OxPrior", "claude-code")
	require.Len(t, history.Entries, 3)
	assert.Empty(t, history.Entries[0].CallID, "a message has no call id")
	assert.Equal(t, callID, history.Entries[1].CallID, "the call keeps its id")
	assert.Equal(t, callID, history.Entries[2].CallID, "the result keeps the same id")

	path := filepath.Join(t.TempDir(), historyFilename)
	require.NoError(t, WriteHistoryJSONL(path, history))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(raw), `"call_id":"`+callID+`"`), "both tool lines carry call_id on disk")
	assert.NotContains(t, string(raw), `"call_id":""`, "entries without an id omit the key")

	reloaded, err := ParseHistoryFile(path)
	require.NoError(t, err)
	require.Len(t, reloaded.Entries, 3)
	assert.Equal(t, callID, reloaded.Entries[1].CallID)
	assert.Equal(t, callID, reloaded.Entries[2].CallID)

	sessionEntry := reloaded.Entries[1].ToSessionEntry()
	assert.Equal(t, callID, sessionEntry.CallID, "history -> session keeps the id")
	back := HistoryEntryFromSessionEntry(sessionEntry, 7, HistorySourceAdapterImport)
	assert.Equal(t, callID, back.CallID, "session -> history keeps the id")
}

// TestStampRawCarrier_Guards pins the refusals that keep the stamp from
// ever touching the wrong thing: no path, a path that is not a regular file,
// and an empty file (nothing to close before the footer).
func TestStampRawCarrier_Guards(t *testing.T) {
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	require.Error(t, StampRawCarrier("", CarrierStamp{StoppedAt: at}), "an empty path is refused")

	dir := t.TempDir()
	require.Error(t, StampRawCarrier(dir, CarrierStamp{StoppedAt: at}), "a directory is not a recording")

	empty := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	require.NoError(t, StampRawCarrier(empty, CarrierStamp{StoppedAt: at}))
	data, err := os.ReadFile(empty)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), `{"`), "an empty file gets the footer as its only line: %q", data)
	assert.Contains(t, string(data), `"type":"footer"`)
	assert.Equal(t, 1, strings.Count(string(data), "\n"), "no stray newline is inserted before the footer of an empty file")
}

// TestRecordingState_RecordNativeSession_NilReceiver: the hook calls this
// through a state it may not have; a nil receiver must be a no-op, not a
// panic inside a SessionStart hook.
func TestRecordingState_RecordNativeSession_NilReceiver(t *testing.T) {
	var state *RecordingState
	state.RecordNativeSession("cc-1", "startup", time.Now())
	assert.Nil(t, state)
}

// TestParseStoreMeta_MalformedNativeSessionsIsDropped: a header whose
// native_sessions is not a list must not take the rest of the metadata
// down with it — the field is dropped and everything else still parses.
func TestParseStoreMeta_MalformedNativeSessionsIsDropped(t *testing.T) {
	meta := ParseStoreMeta(map[string]any{
		"version":         "1.0",
		"created_at":      "2026-09-21T09:00:00Z",
		"agent_id":        "OxBad",
		"native_sessions": "not a list",
		"stopped_at":      "2026-09-21T10:00:00Z", // second precision, no fraction
	})
	require.NotNil(t, meta)
	assert.Equal(t, "OxBad", meta.AgentID)
	assert.Empty(t, meta.NativeSessions, "a malformed list is dropped, not guessed at")
	require.NotNil(t, meta.StoppedAt, "the other carrier field still parses")
	assert.True(t, meta.StoppedAt.Equal(time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)))
	assert.Nil(t, ParseStoreMeta(map[string]any{"version": "1.0", "stopped_at": "yesterday"}).StoppedAt, "an unparseable stop time is dropped")

	meta = ParseStoreMeta(map[string]any{
		"version":         "1.0",
		"created_at":      "2026-09-21T09:00:00Z",
		"native_sessions": []any{map[string]any{"id": 42}},
	})
	require.NotNil(t, meta)
	assert.Empty(t, meta.NativeSessions, "an entry of the wrong shape drops the list")
}
