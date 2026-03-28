package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- formatSize ---

func TestFormatSize(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero bytes", 0, "0 B"},
		{"one byte", 1, "1 B"},
		{"under 1 KB", 1023, "1023 B"},
		{"exactly 1 KB", 1024, "1.0 KB"},
		{"1.5 KB", 1536, "1.5 KB"},
		{"exactly 1 MB", 1024 * 1024, "1.0 MB"},
		{"exactly 1 GB", 1024 * 1024 * 1024, "1.0 GB"},
		{"2.5 GB", int64(2.5 * 1024 * 1024 * 1024), "2.5 GB"},
		{"exactly 1 TB", 1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{"large file", 5 * 1024 * 1024, "5.0 MB"},
		// boundary: just below KB threshold
		{"1023 bytes", 1023, "1023 B"},
		// boundary: exactly at KB
		{"1024 bytes", 1024, "1.0 KB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSize(tt.bytes)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- convertStoredMapEntries ---

func TestConvertStoredMapEntries(t *testing.T) {
	tests := []struct {
		name    string
		stored  []map[string]any
		wantLen int
		check   func(t *testing.T, entries []session.Entry)
	}{
		{
			name:    "empty input",
			stored:  nil,
			wantLen: 0,
		},
		{
			name: "full entry with all fields",
			stored: []map[string]any{
				{
					"type":       "tool_use",
					"content":    "reading file",
					"tool_name":  "Read",
					"tool_input": "/tmp/foo.go",
				},
			},
			wantLen: 1,
			check: func(t *testing.T, entries []session.Entry) {
				assert.Equal(t, session.EntryType("tool_use"), entries[0].Type)
				assert.Equal(t, "reading file", entries[0].Content)
				assert.Equal(t, "Read", entries[0].ToolName)
				assert.Equal(t, "/tmp/foo.go", entries[0].ToolInput)
			},
		},
		{
			name: "missing optional fields",
			stored: []map[string]any{
				{
					"type":    "assistant",
					"content": "hello",
				},
			},
			wantLen: 1,
			check: func(t *testing.T, entries []session.Entry) {
				assert.Equal(t, session.EntryType("assistant"), entries[0].Type)
				assert.Equal(t, "hello", entries[0].Content)
				assert.Equal(t, "", entries[0].ToolName)
				assert.Equal(t, "", entries[0].ToolInput)
			},
		},
		{
			name: "wrong types are ignored gracefully",
			stored: []map[string]any{
				{
					"type":    123,       // not a string
					"content": []int{42}, // not a string
				},
			},
			wantLen: 1,
			check: func(t *testing.T, entries []session.Entry) {
				// type assertion fails, so fields are empty
				assert.Equal(t, session.EntryType(""), entries[0].Type)
				assert.Equal(t, "", entries[0].Content)
			},
		},
		{
			name: "multiple entries preserve order",
			stored: []map[string]any{
				{"type": "user", "content": "first"},
				{"type": "assistant", "content": "second"},
				{"type": "user", "content": "third"},
			},
			wantLen: 3,
			check: func(t *testing.T, entries []session.Entry) {
				assert.Equal(t, "first", entries[0].Content)
				assert.Equal(t, "second", entries[1].Content)
				assert.Equal(t, "third", entries[2].Content)
			},
		},
		{
			name: "empty map produces empty entry",
			stored: []map[string]any{
				{},
			},
			wantLen: 1,
			check: func(t *testing.T, entries []session.Entry) {
				assert.Equal(t, session.EntryType(""), entries[0].Type)
				assert.Equal(t, "", entries[0].Content)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertStoredMapEntries(tt.stored)
			assert.Len(t, got, tt.wantLen)
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// --- containsString ---

func TestContainsString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		val   string
		want  bool
	}{
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty slice", []string{}, "a", false},
		{"nil slice", nil, "a", false},
		{"empty string found", []string{"", "a"}, "", true},
		{"empty string not in non-empty slice", []string{"a", "b"}, "", false},
		{"single element match", []string{"x"}, "x", true},
		{"single element no match", []string{"x"}, "y", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsString(tt.slice, tt.val)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- locationLabel / removedLocationLabel ---

func TestLocationLabel(t *testing.T) {
	tests := []struct {
		name     string
		isLocal  bool
		isLedger bool
		want     string
	}{
		{"local only", true, false, "(local)"},
		{"ledger only", false, true, "(ledger)"},
		{"both", true, true, "(local + ledger)"},
		{"neither defaults to local", false, false, "(local)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sessionMatch{
				isLocal:  tt.isLocal,
				isLedger: tt.isLedger,
			}
			got := locationLabel(m)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRemovedLocationLabel(t *testing.T) {
	tests := []struct {
		name   string
		local  bool
		ledger bool
		want   string
	}{
		{"local only", true, false, "(local)"},
		{"ledger only", false, true, "(ledger)"},
		{"both", true, true, "(local + ledger)"},
		{"neither defaults to local", false, false, "(local)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removedLocationLabel(tt.local, tt.ledger)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- isTimeoutErr ---

func TestIsTimeoutErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"timeout error", errors.New("read: i/o timeout"), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"timeout substring", errors.New("something i/o timeout happened"), true},
		{"empty error", errors.New(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTimeoutErr(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- calculateThanksColumnsWidth ---

func TestCalculateThanksColumnsWidth(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		cols  int
		want  int
	}{
		{"empty names", []string{}, 3, 0},
		{"single short name", []string{"Jo"}, 1, 4},           // maxWidth=2, colWidth=4
		{"single short name 3 cols", []string{"Jo"}, 3, 12},   // maxWidth=2, colWidth=4, *3
		{"long names capped at 20", []string{"abcdefghijklmnopqrstuvwxyz"}, 2, 44}, // capped 20, colWidth=22, *2
		{"mixed lengths", []string{"short", "a-longer-name", "x"}, 2, 30},          // maxWidth=13, colWidth=15, *2
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateThanksColumnsWidth(tt.names, tt.cols)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- endOfDay ---

func TestEndOfDay_Coverage(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want time.Time
	}{
		{
			"normal date",
			time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			"already end of day",
			time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
			time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			"start of day",
			time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC),
		},
		{
			"leap year feb 29",
			time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC),
			time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endOfDay(tt.t)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- endOfMonth ---

func TestEndOfMonth_Coverage(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want time.Time
	}{
		{
			"january",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			"february non-leap",
			time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 2, 28, 23, 59, 59, 0, time.UTC),
		},
		{
			"february leap year",
			time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC),
		},
		{
			"december wraps to next year",
			time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			"april 30 days",
			time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endOfMonth(tt.t)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- isoWeekRange edge cases ---

func TestISOWeekRange_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		year      int
		week      int
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			// week 1 of 2026 starts Mon Dec 29, 2025
			"week 1 crossing year boundary",
			2026, 1,
			time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 4, 23, 59, 59, 0, time.UTC),
		},
		{
			// week 53 of 2020 (2020 has 53 ISO weeks)
			"week 53",
			2020, 53,
			time.Date(2020, 12, 28, 0, 0, 0, 0, time.UTC),
			time.Date(2021, 1, 3, 23, 59, 59, 0, time.UTC),
		},
		{
			"mid year week",
			2026, 26,
			time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 6, 28, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := isoWeekRange(tt.year, tt.week)
			assert.Equal(t, tt.wantStart, start, "start mismatch")
			assert.Equal(t, tt.wantEnd, end, "end mismatch")

			// verify start is always a Monday
			assert.Equal(t, time.Monday, start.Weekday(), "start should be Monday")
			// verify end is always a Sunday
			assert.Equal(t, time.Sunday, end.Weekday(), "end should be Sunday")
		})
	}
}

// --- contentHash ---

func TestContentHash_Deterministic(t *testing.T) {
	h1 := contentHash("hello", "world")
	h2 := contentHash("hello", "world")
	assert.Equal(t, h1, h2, "same inputs should produce same hash")
}

func TestContentHash_DifferentInputs(t *testing.T) {
	h1 := contentHash("hello", "world")
	h2 := contentHash("helloworld")
	// null separator prevents "hello"+"world" == "helloworld"
	assert.NotEqual(t, h1, h2, "separator should prevent collision")
}

func TestContentHash_Length(t *testing.T) {
	h := contentHash("test")
	assert.Len(t, h, 16, "hash should be 16 hex chars (8 bytes)")
}

func TestContentHash_EmptyInput(t *testing.T) {
	h := contentHash()
	assert.Len(t, h, 16, "empty input should still produce a hash")
}

// --- groupObservationsByDay edge cases ---

func TestGroupObservationsByDay_ZeroTimestamps(t *testing.T) {
	observations := []distillObservation{
		{Content: "valid", RecordedAt: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)},
		{Content: "zero timestamp", RecordedAt: time.Time{}},
	}

	groups := groupObservationsByDay(observations)
	// zero timestamp entries should be skipped
	assert.Len(t, groups, 1, "zero timestamp entries should be excluded")
	assert.Len(t, groups["2026-03-15"], 1)
	assert.Equal(t, "valid", groups["2026-03-15"][0].Content)
}

func TestGroupObservationsByDay_Empty(t *testing.T) {
	groups := groupObservationsByDay(nil)
	assert.Empty(t, groups)
}

func TestGroupObservationsByDay_MultipleDays(t *testing.T) {
	observations := []distillObservation{
		{Content: "day1-a", RecordedAt: time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)},
		{Content: "day2-a", RecordedAt: time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)},
		{Content: "day1-b", RecordedAt: time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC)},
	}

	groups := groupObservationsByDay(observations)
	assert.Len(t, groups, 2)
	assert.Len(t, groups["2026-03-15"], 2)
	assert.Len(t, groups["2026-03-16"], 1)
	// verify order within day is preserved
	assert.Equal(t, "day1-a", groups["2026-03-15"][0].Content)
	assert.Equal(t, "day1-b", groups["2026-03-15"][1].Content)
}

// --- buildFinalizeCommands edge cases ---

func TestBuildFinalizeCommands_EmptyMissing(t *testing.T) {
	commands := buildFinalizeCommands("test-session", "agent-123", nil, "/path/to/session")
	assert.Empty(t, commands)
}

func TestBuildFinalizeCommands_AllMissing(t *testing.T) {
	missing := []string{"summary", "session_md", "summary_json"}
	commands := buildFinalizeCommands("test-session", "agent-123", missing, "/path/to/session")

	require.Len(t, commands, 3)
	assert.Contains(t, commands[0], "ox agent agent-123 session summarize")
	assert.Contains(t, commands[1], "ox session export --markdown")
	assert.Contains(t, commands[2], "summary.json missing")
}

func TestBuildFinalizeCommands_UnknownArtifact(t *testing.T) {
	// unknown artifacts should produce no commands (not panic)
	commands := buildFinalizeCommands("test", "agent", []string{"unknown_thing"}, "/path")
	assert.Empty(t, commands)
}

// --- buildNextSteps ---

func TestBuildNextSteps_Empty(t *testing.T) {
	output := &AgentDoctorOutput{}
	steps := buildNextSteps(output)
	assert.Empty(t, steps)
}

func TestBuildNextSteps_CommitAndPush(t *testing.T) {
	output := &AgentDoctorOutput{
		CommitNeeded: true,
		PushNeeded:   true,
	}
	steps := buildNextSteps(output)
	assert.Contains(t, steps, "ox session commit")
	assert.Contains(t, steps, "git push (in ledger)")
}

func TestBuildNextSteps_IncompleteSessionsWithMultipleMissing(t *testing.T) {
	output := &AgentDoctorOutput{
		IncompleteSessions: []IncompleteSessionInfo{
			{
				SessionID: "session-1",
				Missing:   []string{"summary", "summary_json"},
			},
		},
	}
	steps := buildNextSteps(output)
	// should have steps for each missing artifact type
	found := map[string]bool{}
	for _, step := range steps {
		if assert.NotEmpty(t, step) {
			found[step] = true
		}
	}
	assert.True(t, found["Generate summary for session session-1"])
	assert.True(t, found["Generate summary JSON for session session-1"])
}

// --- matchName ---

func TestMatchName(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		filename    string
		want        string
	}{
		{"prefers session name", "my-session", "raw.jsonl", "my-session"},
		{"falls back to filename", "", "session-2026-03.jsonl", "session-2026-03.jsonl"},
		{"both empty returns empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sessionMatch{
				info: session.SessionInfo{
					SessionName: tt.sessionName,
					Filename:    tt.filename,
				},
			}
			got := matchName(m)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- formatDurationHuman edge cases ---

func TestFormatDurationHuman_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"zero", 0, "0 seconds"},
		{"exactly one second", time.Second, "1 second"},
		{"exactly one minute", time.Minute, "1 minute"},
		{"exactly one hour", time.Hour, "1 hour"},
		{"1 hour 1 minute", time.Hour + time.Minute, "1 hour 1 minutes"},
		{"sub-second rounds to 0", 500 * time.Millisecond, "0 seconds"},
		{"59 seconds", 59 * time.Second, "59 seconds"},
		{"59 minutes", 59 * time.Minute, "59 minutes"},
		{"2 hours exact", 2 * time.Hour, "2 hours"},
		{"2 hours 30 min", 2*time.Hour + 30*time.Minute, "2 hours 30 minutes"},
		{"24 hours", 24 * time.Hour, "24 hours"},
		{"48 hours 15 min", 48*time.Hour + 15*time.Minute, "48 hours 15 minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationHuman(tt.duration)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- escapeXML ---

func TestEscapeXML_DoubleEscapeProtection(t *testing.T) {
	// verifies & is escaped before other replacements would cause double-escaping
	// "&lt;" should become "&amp;lt;" (escape the & but don't double-process)
	assert.Equal(t, "&amp;lt;", escapeXML("&lt;"))
}

func TestEscapeXML_NoSpecialCharsPassthrough(t *testing.T) {
	input := "plain text 123"
	assert.Equal(t, input, escapeXML(input))
}

func TestEscapeXML_EmptyStringPassthrough(t *testing.T) {
	assert.Equal(t, "", escapeXML(""))
}

func TestEscapeXML_MixedContent(t *testing.T) {
	// realistic agent output with mixed XML-sensitive characters
	input := `User said: "use <ox agent prime> & run 'ox doctor'"`
	got := escapeXML(input)
	assert.NotContains(t, got, "<")
	assert.NotContains(t, got, ">")
	assert.NotContains(t, got, "&o") // no raw & except in escape sequences
	assert.Contains(t, got, "&lt;")
	assert.Contains(t, got, "&gt;")
	assert.Contains(t, got, "&amp;")
	assert.Contains(t, got, "&quot;")
	assert.Contains(t, got, "&apos;")
}

// --- checkSessionCompleteness ---

func TestCheckSessionCompleteness_NoRawFile(t *testing.T) {
	// no raw.jsonl means no session data; should return nil (not list as missing)
	dir := t.TempDir()
	missing := checkSessionCompleteness(dir)
	assert.Nil(t, missing)
}

func TestCheckSessionCompleteness_AllPresent(t *testing.T) {
	dir := t.TempDir()
	// create all expected files
	files := []string{"raw.jsonl", "summary.md", "session.md", "summary.json"}
	for _, f := range files {
		require.NoError(t, writeTestFile(t, dir, f, "content"))
	}

	missing := checkSessionCompleteness(dir)
	assert.Empty(t, missing)
}

func TestCheckSessionCompleteness_SomeMissing(t *testing.T) {
	dir := t.TempDir()
	// only raw.jsonl present - everything else missing
	require.NoError(t, writeTestFile(t, dir, "raw.jsonl", "{}"))

	missing := checkSessionCompleteness(dir)
	assert.NotEmpty(t, missing)
	assert.True(t, containsString(missing, "summary"))
	assert.True(t, containsString(missing, "session_md"))
	assert.True(t, containsString(missing, "summary_json"))
}

// --- resolvePhase ---

func TestResolvePhase_UnknownAgent(t *testing.T) {
	// unknown agent type should try all maps as fallback
	phase := resolvePhase("totally-unknown-agent", "session_start")
	// session_start maps to "start" for claude-code; fallback should find it
	// (or return empty if event doesn't match any agent)
	// the point: it shouldn't panic
	_ = phase
}

func TestResolvePhase_UnknownEvent(t *testing.T) {
	phase := resolvePhase("claude-code", "nonexistent_event")
	assert.Equal(t, "", phase, "unknown event should return empty phase")
}

// --- activePhaseBehavior coverage ---

func TestActivePhaseBehavior_KnownPhases(t *testing.T) {
	// verify the phases that should have behavior
	assert.True(t, activePhaseBehavior[phaseStart])
	assert.True(t, activePhaseBehavior[phaseCompact])
	assert.True(t, activePhaseBehavior[phaseAfterTool])
	assert.True(t, activePhaseBehavior[phaseStop])

	// phases without behavior
	assert.False(t, activePhaseBehavior[phasePrompt])
	assert.False(t, activePhaseBehavior[phaseBeforeTool])
	assert.False(t, activePhaseBehavior[phaseEnd])
}

// --- dispatchPhase noop default ---

func TestDispatchPhase_UnknownPhaseReturnsNil(t *testing.T) {
	ctx := &HookContext{Phase: "unknown-phase"}
	err := dispatchPhase(ctx)
	assert.NoError(t, err)
}

// helper to write test files
func writeTestFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
}
