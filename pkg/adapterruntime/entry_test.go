package adapterruntime

import (
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testTime = time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)

func TestUserEntry(t *testing.T) {
	e := UserEntry(testTime, "hello world")
	assert.Equal(t, adapterprotocol.RoleUser, e.Role)
	assert.Equal(t, "hello world", e.Content)
	assert.Equal(t, "2026-03-15T14:30:00Z", e.Timestamp)
	require.NoError(t, e.Validate())
}

func TestAssistantEntry(t *testing.T) {
	e := AssistantEntry(testTime, "I'll help")
	assert.Equal(t, adapterprotocol.RoleAssistant, e.Role)
	assert.Equal(t, "I'll help", e.Content)
	require.NoError(t, e.Validate())
}

func TestSystemEntry(t *testing.T) {
	e := SystemEntry(testTime, "context loaded")
	assert.Equal(t, adapterprotocol.RoleSystem, e.Role)
	assert.Equal(t, "context loaded", e.Content)
	require.NoError(t, e.Validate())
}

func TestToolUseEntry(t *testing.T) {
	e := ToolUseEntry(testTime, "bash", "ls -la")
	assert.Equal(t, adapterprotocol.RoleTool, e.Role)
	assert.Equal(t, "bash", e.ToolName)
	assert.Equal(t, "ls -la", e.ToolInput)
	assert.Empty(t, e.ToolOutput)
	assert.False(t, e.IsError)
	require.NoError(t, e.Validate())
}

func TestToolResultEntry(t *testing.T) {
	e := ToolResultEntry(testTime, "permission denied", true)
	assert.Equal(t, adapterprotocol.RoleTool, e.Role)
	assert.Equal(t, "permission denied", e.ToolOutput)
	assert.True(t, e.IsError)
	assert.Empty(t, e.ToolName)
	require.NoError(t, e.Validate())
}

func TestToolUseWithID(t *testing.T) {
	e := ToolUseWithID(testTime, "bash", "ls", "call-123")
	assert.Equal(t, "call-123", e.CallID)
	assert.Equal(t, "bash", e.ToolName)
	require.NoError(t, e.Validate())
}

func TestToolResultWithID(t *testing.T) {
	e := ToolResultWithID(testTime, "error output", true, "call-123")
	assert.Equal(t, "call-123", e.CallID)
	assert.True(t, e.IsError)
	require.NoError(t, e.Validate())
}

func TestZeroTimestamp(t *testing.T) {
	e := UserEntry(time.Time{}, "no timestamp")
	assert.Empty(t, e.Timestamp, "zero time should produce empty timestamp")
	require.NoError(t, e.Validate())
}

func TestTimestampIsUTC(t *testing.T) {
	// use a fixed offset to avoid DST ambiguity
	fixed := time.FixedZone("UTC-5", -5*60*60)
	local := time.Date(2026, 1, 15, 10, 0, 0, 0, fixed)
	e := UserEntry(local, "test")
	assert.Equal(t, "2026-01-15T15:00:00Z", e.Timestamp, "should convert to UTC")
}

func TestAllConstructors_ProduceValidEntries(t *testing.T) {
	entries := []adapterprotocol.RawEntry{
		UserEntry(testTime, "q"),
		AssistantEntry(testTime, "a"),
		SystemEntry(testTime, "ctx"),
		ToolUseEntry(testTime, "bash", "ls"),
		ToolResultEntry(testTime, "ok", false),
		ToolUseWithID(testTime, "read", "/f", "c1"),
		ToolResultWithID(testTime, "data", false, "c1"),
	}
	issues := adapterprotocol.ValidateEntries(entries)
	assert.Empty(t, issues, "all constructor-built entries should pass validation")
}
