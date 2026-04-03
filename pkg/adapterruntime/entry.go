package adapterruntime

import (
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// fmtTimestamp formats a time.Time to RFC3339 UTC for the wire protocol.
// Returns empty string for zero time (adapter didn't know the timestamp).
func fmtTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// UserEntry creates a user message entry.
func UserEntry(ts time.Time, content string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp: fmtTimestamp(ts),
		Role:      adapterprotocol.RoleUser,
		Content:   content,
	}
}

// AssistantEntry creates an assistant message entry.
func AssistantEntry(ts time.Time, content string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp: fmtTimestamp(ts),
		Role:      adapterprotocol.RoleAssistant,
		Content:   content,
	}
}

// SystemEntry creates a system/context injection entry.
func SystemEntry(ts time.Time, content string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp: fmtTimestamp(ts),
		Role:      adapterprotocol.RoleSystem,
		Content:   content,
	}
}

// ToolUseEntry creates a tool invocation entry (tool call, no result yet).
func ToolUseEntry(ts time.Time, toolName, toolInput string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp: fmtTimestamp(ts),
		Role:      adapterprotocol.RoleTool,
		ToolName:  toolName,
		ToolInput: toolInput,
	}
}

// ToolResultEntry creates a tool result entry (error output from a tool call).
func ToolResultEntry(ts time.Time, toolOutput string, isError bool) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp:  fmtTimestamp(ts),
		Role:       adapterprotocol.RoleTool,
		ToolOutput: toolOutput,
		IsError:    isError,
	}
}

// ToolUseWithID creates a tool invocation entry with a correlation ID.
// Use the same callID in ToolResultWithID to correlate call and result.
func ToolUseWithID(ts time.Time, toolName, toolInput, callID string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp: fmtTimestamp(ts),
		Role:      adapterprotocol.RoleTool,
		ToolName:  toolName,
		ToolInput: toolInput,
		CallID:    callID,
	}
}

// ToolResultWithID creates a tool result entry with a correlation ID.
func ToolResultWithID(ts time.Time, toolOutput string, isError bool, callID string) adapterprotocol.RawEntry {
	return adapterprotocol.RawEntry{
		Timestamp:  fmtTimestamp(ts),
		Role:       adapterprotocol.RoleTool,
		ToolOutput: toolOutput,
		IsError:    isError,
		CallID:     callID,
	}
}
