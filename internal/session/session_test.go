package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSessionMeta(t *testing.T) {
	sessionID := "Oxa7b3"
	oxsid := "oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T"
	agentType := "claude-code"
	projectRemote := "github.com/example/repo"

	meta := NewSessionMeta(sessionID, oxsid, agentType, projectRemote, "")

	assert.Equal(t, sessionID, meta.SessionID)
	assert.Equal(t, oxsid, meta.OxSID)
	assert.Equal(t, agentType, meta.AgentType)
	assert.Equal(t, projectRemote, meta.ProjectRemote)
	assert.Equal(t, SessionSchemaVersion, meta.SchemaVersion)
	assert.False(t, meta.StartedAt.IsZero(), "StartedAt should be set")
	assert.True(t, meta.EndedAt.IsZero(), "EndedAt should be zero initially")
}

func TestNewSessionMetaWithVersion(t *testing.T) {
	meta := NewSessionMetaWithVersion("Oxa7b3", "oxsid_test", "cursor", "0.45.1", "github.com/example/repo", "")

	assert.Equal(t, "0.45.1", meta.AgentVersion)
}

func TestSessionMetaClose(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")

	assert.True(t, meta.EndedAt.IsZero(), "EndedAt should be zero before Close")

	// small delay to ensure time difference
	time.Sleep(10 * time.Millisecond)
	meta.Close()

	assert.False(t, meta.EndedAt.IsZero(), "EndedAt should be set after Close")
	assert.True(t, meta.EndedAt.After(meta.StartedAt), "EndedAt should be after StartedAt")
}

func TestSessionMetaDuration(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")

	// duration is zero before close
	assert.Equal(t, time.Duration(0), meta.Duration(), "Duration should be zero before Close")

	time.Sleep(50 * time.Millisecond)
	meta.Close()

	duration := meta.Duration()
	assert.GreaterOrEqual(t, duration, 50*time.Millisecond)
}

func TestNewSessionFooter(t *testing.T) {
	startedAt := time.Now().Add(-10 * time.Minute)
	entryCount := 42

	footer := NewSessionFooter(startedAt, entryCount)

	assert.Equal(t, entryCount, footer.EntryCount)
	assert.True(t, footer.DurationMins >= 9 && footer.DurationMins <= 11, "DurationMins should be ~10")
	assert.False(t, footer.EndedAt.IsZero(), "EndedAt should be set")
}

func TestSessionEntryTypeIsValid(t *testing.T) {
	tests := []struct {
		name      string
		entryType SessionEntryType
		want      bool
	}{
		{"user", SessionEntryTypeUser, true},
		{"assistant", SessionEntryTypeAssistant, true},
		{"system", SessionEntryTypeSystem, true},
		{"tool", SessionEntryTypeTool, true},
		{"invalid", SessionEntryType("invalid"), false},
		{"empty", SessionEntryType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.entryType.IsValid())
		})
	}
}

func TestSessionEntryTypeString(t *testing.T) {
	assert.Equal(t, "user", SessionEntryTypeUser.String())
}

func TestNewSessionEntry(t *testing.T) {
	content := "hello world"
	entry := NewSessionEntry(SessionEntryTypeUser, content)

	assert.Equal(t, SessionEntryTypeUser, entry.Type)
	assert.Equal(t, content, entry.Content)
	assert.False(t, entry.Timestamp.IsZero(), "Timestamp should be set")
}

func TestNewToolSessionEntry(t *testing.T) {
	toolName := "read_file"
	toolInput := `{"path": "/tmp/test.txt"}`
	toolOutput := "file contents"

	entry := NewToolSessionEntry(toolName, toolInput, toolOutput)

	assert.Equal(t, SessionEntryTypeTool, entry.Type)
	assert.Equal(t, toolName, entry.ToolName)
	assert.Equal(t, toolInput, entry.ToolInput)
	assert.Equal(t, toolOutput, entry.ToolOutput)
}

func TestNewUserSessionEntry(t *testing.T) {
	entry := NewUserSessionEntry("user message")
	assert.Equal(t, SessionEntryTypeUser, entry.Type)
}

func TestNewAssistantSessionEntry(t *testing.T) {
	entry := NewAssistantSessionEntry("assistant response")
	assert.Equal(t, SessionEntryTypeAssistant, entry.Type)
}

func TestNewSystemSessionEntry(t *testing.T) {
	entry := NewSystemSessionEntry("system prompt")
	assert.Equal(t, SessionEntryTypeSystem, entry.Type)
}

func TestNewSession(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")
	session := NewSession(meta)

	assert.Equal(t, meta, session.Meta)
	assert.NotNil(t, session.Entries)
	assert.Empty(t, session.Entries)
}

func TestSessionAddEntry(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")
	session := NewSession(meta)

	entry := NewUserSessionEntry("test message")
	session.AddEntry(entry)

	require.Len(t, session.Entries, 1)
	assert.Equal(t, "test message", session.Entries[0].Content)
}

func TestSessionAddUserEntry(t *testing.T) {
	session := NewSession(nil)
	session.AddUserEntry("user input")

	require.Len(t, session.Entries, 1)
	assert.Equal(t, SessionEntryTypeUser, session.Entries[0].Type)
}

func TestSessionAddAssistantEntry(t *testing.T) {
	session := NewSession(nil)
	session.AddAssistantEntry("assistant output")

	require.Len(t, session.Entries, 1)
	assert.Equal(t, SessionEntryTypeAssistant, session.Entries[0].Type)
}

func TestSessionAddSystemEntry(t *testing.T) {
	session := NewSession(nil)
	session.AddSystemEntry("system context")

	require.Len(t, session.Entries, 1)
	assert.Equal(t, SessionEntryTypeSystem, session.Entries[0].Type)
}

func TestSessionAddToolEntry(t *testing.T) {
	session := NewSession(nil)
	session.AddToolEntry("bash", "ls -la", "file1\nfile2")

	require.Len(t, session.Entries, 1)
	assert.Equal(t, SessionEntryTypeTool, session.Entries[0].Type)
	assert.Equal(t, "bash", session.Entries[0].ToolName)
}

func TestSessionEntryCount(t *testing.T) {
	session := NewSession(nil)

	assert.Equal(t, 0, session.EntryCount())

	session.AddUserEntry("message 1")
	session.AddAssistantEntry("response 1")
	session.AddToolEntry("tool", "input", "output")

	assert.Equal(t, 3, session.EntryCount())
}

func TestSessionClose(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")
	session := NewSession(meta)

	assert.True(t, session.Meta.EndedAt.IsZero(), "EndedAt should be zero before Close")

	session.Close()

	assert.False(t, session.Meta.EndedAt.IsZero(), "EndedAt should be set after Close")
}

func TestSessionCloseNilMeta(t *testing.T) {
	session := NewSession(nil)
	// should not panic
	session.Close()
}

func TestSessionFooter(t *testing.T) {
	meta := NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "", "")
	session := NewSession(meta)

	session.AddUserEntry("message 1")
	session.AddAssistantEntry("response 1")

	footer := session.Footer()

	assert.Equal(t, 2, footer.EntryCount)
}

func TestSessionFooterNilMeta(t *testing.T) {
	session := NewSession(nil)
	session.AddUserEntry("message")

	footer := session.Footer()

	assert.Equal(t, 1, footer.EntryCount)
}

func TestSessionConversationFlow(t *testing.T) {
	// simulate a realistic conversation flow
	meta := NewSessionMeta("Oxa7b3", "oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T", "claude-code", "github.com/example/repo", "")
	session := NewSession(meta)

	// user asks a question
	session.AddUserEntry("How do I list files in a directory?")

	// assistant uses a tool
	session.AddToolEntry("bash", "ls -la /tmp", "total 0\ndrwxrwxrwt  12 root  wheel  384 Jan  5 10:00 .")

	// assistant responds
	session.AddAssistantEntry("You can use the `ls -la` command to list files with detailed information.")

	// user follow-up
	session.AddUserEntry("Thanks! Can you show me hidden files too?")

	// assistant responds
	session.AddAssistantEntry("The `-a` flag in `ls -la` already shows hidden files (those starting with `.`).")

	// close the session
	session.Close()

	// verify
	assert.Equal(t, 5, session.EntryCount())

	// verify entry order and types
	expectedTypes := []SessionEntryType{SessionEntryTypeUser, SessionEntryTypeTool, SessionEntryTypeAssistant, SessionEntryTypeUser, SessionEntryTypeAssistant}
	for i, expected := range expectedTypes {
		assert.Equal(t, expected, session.Entries[i].Type, "Entry[%d].Type mismatch", i)
	}

	// verify meta is closed
	assert.False(t, session.Meta.EndedAt.IsZero(), "Meta.EndedAt should be set after Close")
}

func BenchmarkNewSessionEntry(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NewSessionEntry(SessionEntryTypeUser, "benchmark message")
	}
}

func BenchmarkSessionAddEntry(b *testing.B) {
	session := NewSession(nil)
	entry := NewUserSessionEntry("benchmark message")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session.AddEntry(entry)
	}
}

func BenchmarkNewSessionMeta(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NewSessionMeta("Oxa7b3", "oxsid_test", "claude-code", "github.com/example/repo", "")
	}
}
