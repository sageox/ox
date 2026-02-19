// Package session provides shared entry parsing utilities for session recordings.
// These functions handle the multiple JSONL formats that session entries can take:
// imported format (root-level fields), standard format (nested data.*), and legacy format.
package session

import (
	"time"
)

// MapEntryType normalizes raw entry type strings to canonical CSS/display types.
// Returns one of: "user", "assistant", "system", "tool", "tool_result", "info".
func MapEntryType(raw string) string {
	switch raw {
	case "user", "human":
		return "user"
	case "assistant", "message", "ai", "model":
		return "assistant"
	case "tool", "tool_use", "tool_call":
		return "tool"
	case "tool_result", "tool_output":
		return "tool_result"
	case "system":
		return "system"
	default:
		return "info"
	}
}

// MapRoleToType converts a message role (from data.role) to a display type.
func MapRoleToType(role string) string {
	switch role {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return "info"
	}
}

// ParseTimestamp parses a timestamp string in RFC3339 or RFC3339Nano format.
// Returns zero time and false if parsing fails.
func ParseTimestamp(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ExtractEntryTimestamp gets the timestamp from an entry, checking both
// "timestamp" and "ts" field names.
func ExtractEntryTimestamp(entry map[string]any) (time.Time, bool) {
	if ts, ok := entry["timestamp"].(string); ok {
		return ParseTimestamp(ts)
	}
	if ts, ok := entry["ts"].(string); ok {
		return ParseTimestamp(ts)
	}
	return time.Time{}, false
}

// ExtractEntryType gets the type field from an entry.
func ExtractEntryType(entry map[string]any) string {
	if t, ok := entry["type"].(string); ok {
		return t
	}
	return "unknown"
}

// ExtractContent gets content from various field locations in an entry.
// Checks: content, data.content, data.message, message, text, result.
func ExtractContent(entry map[string]any) string {
	// try direct content field
	if content, ok := entry["content"].(string); ok {
		return content
	}

	// try nested data.content
	if data, ok := entry["data"].(map[string]any); ok {
		if content, ok := data["content"].(string); ok {
			return content
		}
		if message, ok := data["message"].(string); ok {
			return message
		}
	}

	// try message field
	if message, ok := entry["message"].(string); ok {
		return message
	}

	// try text field
	if text, ok := entry["text"].(string); ok {
		return text
	}

	// try result field (tool results in imported format)
	if result, ok := entry["result"].(string); ok {
		return result
	}

	return ""
}
