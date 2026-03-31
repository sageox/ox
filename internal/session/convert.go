package session

import "github.com/sageox/ox/internal/session/adapters"

// MapRoleToEntryType converts an adapter role string to a session EntryType.
func MapRoleToEntryType(role string) EntryType {
	switch role {
	case "user":
		return EntryTypeUser
	case "assistant":
		return EntryTypeAssistant
	case "system":
		return EntryTypeSystem
	case "tool":
		return EntryTypeTool
	default:
		return EntryTypeSystem
	}
}

// ConvertRawEntries converts adapter raw entries to session entries.
func ConvertRawEntries(rawEntries []adapters.RawEntry) []Entry {
	entries := make([]Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry := Entry{
			Timestamp:  raw.Timestamp,
			Content:    raw.Content,
			ToolName:   raw.ToolName,
			ToolInput:  raw.ToolInput,
			ToolOutput: raw.ToolOutput,
			IsError:    raw.IsError,
		}
		entry.Type = MapRoleToEntryType(raw.Role)
		entries = append(entries, entry)
	}
	return entries
}
