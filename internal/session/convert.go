package session

import (
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// HistorySourceAdapterImport is the source marker for adapter-imported history.
const HistorySourceAdapterImport = "adapter_import"

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

// ConvertProtocolEntriesToHistory converts adapter protocol RawEntry values into
// a CapturedHistory suitable for storage via the capture-prior pipeline. This
// bridges the adapter protocol (which returns []adapterprotocol.RawEntry) and
// the session history format used by the ledger.
//
// Each RawEntry maps 1:1 to a HistoryEntry. Seq numbers are auto-assigned.
func ConvertProtocolEntriesToHistory(entries []adapterprotocol.RawEntry, agentID, agentType string) *CapturedHistory {
	meta := NewHistoryMeta(agentID, HistorySourceAdapterImport)
	meta.CapturedAt = time.Now()
	meta.AgentType = agentType

	history := &CapturedHistory{
		Meta:    meta,
		Entries: make([]HistoryEntry, 0, len(entries)),
	}

	for i, raw := range entries {
		ts := time.Time{}
		if raw.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
				ts = parsed
			} else if parsed, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
				ts = parsed
			}
		}

		entry := HistoryEntry{
			Seq:        i + 1,
			Type:       mapRoleToHistoryType(raw.Role),
			Content:    raw.Content,
			Timestamp:  ts,
			Source:     HistorySourceAdapterImport,
			ToolName:   raw.ToolName,
			ToolInput:  raw.ToolInput,
			ToolOutput: raw.ToolOutput,
			IsError:    raw.IsError,
		}

		history.Entries = append(history.Entries, entry)
	}

	history.Meta.TimeRange = history.ComputeTimeRange()
	history.Meta.MessageCount = len(history.Entries)

	return history
}

// mapRoleToHistoryType converts an adapter protocol role to a history entry type string.
func mapRoleToHistoryType(role string) string {
	switch role {
	case adapterprotocol.RoleUser:
		return "user"
	case adapterprotocol.RoleAssistant:
		return "assistant"
	case adapterprotocol.RoleSystem:
		return "system"
	case adapterprotocol.RoleTool:
		return "tool"
	default:
		return "system"
	}
}
