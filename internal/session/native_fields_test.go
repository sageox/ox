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

// TestStampRawHeader rewrites only the first line: existing header keys and
// every entry byte survive, both header dialects work, and the stamped values
// read back through the production parser. Failure prevented: the SessionEnd
// hook clears .recording.json, the daemon finalizes, and meta.json ends up
// with no native session ids and no stop time because nothing carried them.
func TestStampRawHeader(t *testing.T) {
	stoppedAt := time.Date(2026, 9, 21, 12, 30, 0, 123456000, time.UTC)
	sessions := []lfs.NativeSession{
		{ID: "sess-a", Source: "startup", FirstSeen: stoppedAt.Add(-time.Hour), LastSeen: stoppedAt.Add(-time.Hour)},
		{ID: "sess-b", Source: "clear", FirstSeen: stoppedAt.Add(-time.Minute), LastSeen: stoppedAt.Add(-time.Minute)},
	}
	const body = "{\"type\":\"user\",\"content\":\"hello\",\"seq\":0}\n{\"type\":\"tool\",\"tool_name\":\"Bash\",\"call_id\":\"toolu_1\",\"seq\":1}\n"

	tests := []struct {
		name   string
		header string
	}{
		{name: "native header", header: `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T11:00:00Z","agent_id":"Ox1234","session_id":"ses_019d0000-0000-7000-8000-000000000001","custom_key":"kept"}}`},
		{name: "import dialect", header: `{"_meta":{"schema_version":"1","agent_type":"codex","recovered":true}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, []byte(tt.header+"\n"+body), 0o600))

			require.NoError(t, StampRawHeader(rawPath, HeaderStamp{NativeSessions: sessions, StoppedAt: stoppedAt}))

			data, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			first, rest, _ := strings.Cut(string(data), "\n")
			assert.Equal(t, body, rest, "entry bytes must be untouched")

			var header map[string]any
			require.NoError(t, json.Unmarshal([]byte(first), &header))
			meta, _ := headerMetadata(header)
			require.NotNil(t, meta)
			if tt.name == "native header" {
				assert.Equal(t, "kept", meta["custom_key"], "unknown header keys must survive")
				assert.Equal(t, "Ox1234", meta["agent_id"])
			} else {
				assert.Equal(t, true, meta["recovered"], "unknown header keys must survive")
			}

			stored, err := ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.NotNil(t, stored.Meta)
			require.NotNil(t, stored.Meta.StoppedAt)
			assert.True(t, stored.Meta.StoppedAt.Equal(stoppedAt), "got %s", stored.Meta.StoppedAt)
			require.Len(t, stored.Meta.NativeSessions, 2)
			assert.Equal(t, "sess-a", stored.Meta.NativeSessions[0].ID)
			assert.Equal(t, "startup", stored.Meta.NativeSessions[0].Source)
			assert.Equal(t, "sess-b", stored.Meta.NativeSessions[1].ID)
			assert.Equal(t, "clear", stored.Meta.NativeSessions[1].Source)
			assert.Len(t, stored.Entries, 2)
		})
	}

	t.Run("partial stamp leaves the other field alone", func(t *testing.T) {
		rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(rawPath, []byte(tests[0].header+"\n"+body), 0o600))
		require.NoError(t, StampRawHeader(rawPath, HeaderStamp{NativeSessions: sessions}))
		require.NoError(t, StampRawHeader(rawPath, HeaderStamp{StoppedAt: stoppedAt}))
		stored, err := ReadSessionFromPath(rawPath)
		require.NoError(t, err)
		require.Len(t, stored.Meta.NativeSessions, 2, "a stop-time-only stamp must not erase the ids")
		require.NotNil(t, stored.Meta.StoppedAt)
	})

	t.Run("refuses non-headers and missing files", func(t *testing.T) {
		dir := t.TempDir()
		assert.Error(t, StampRawHeader(filepath.Join(dir, "missing.jsonl"), HeaderStamp{StoppedAt: stoppedAt}))
		noHeader := filepath.Join(dir, "raw.jsonl")
		require.NoError(t, os.WriteFile(noHeader, []byte(body), 0o600))
		assert.Error(t, StampRawHeader(noHeader, HeaderStamp{StoppedAt: stoppedAt}))
		data, err := os.ReadFile(noHeader)
		require.NoError(t, err)
		assert.Equal(t, body, string(data), "a refused stamp must write nothing")
		pointer := filepath.Join(dir, "pointer.jsonl")
		require.NoError(t, os.WriteFile(pointer, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n"), 0o600))
		assert.Error(t, StampRawHeader(pointer, HeaderStamp{StoppedAt: stoppedAt}), "never rewrite an LFS pointer")
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
