// Package session provides subagent session capture during recording stop.
//
// When a parent session ends, this module scans the session for Task tool calls,
// matches them with completed subagent sessions (by timestamp correlation), and
// enriches the session entries with subagent references.
//
// Output structure for Task tool entries:
//
//	{
//	  "type": "tool",
//	  "tool_name": "Task",
//	  "tool_input": {...},
//	  "tool_output": "...",
//	  "subagent": {
//	    "session_id": "abc123",
//	    "session_path": "sessions/2026-01-06T14-35-history-Oxdef/",
//	    "entry_count": 47
//	  }
//	}
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SubagentReference represents metadata about a subagent session embedded in a Task tool entry.
// This is the structure that gets added to parent session entries.
type SubagentReference struct {
	// SessionID is the subagent's agent ID (e.g., "Oxa7b3")
	SessionID string `json:"session_id"`

	// SessionPath is the relative path to the subagent's session folder
	// Example: "sessions/2026-01-06T14-35-history-Oxdef/"
	SessionPath string `json:"session_path"`

	// EntryCount is the number of entries in the subagent's session
	EntryCount int `json:"entry_count"`

	// DurationMs is how long the subagent ran in milliseconds (optional)
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Summary is a brief description of what the subagent did (optional)
	Summary string `json:"summary,omitempty"`

	// Model is the LLM model the subagent used (optional)
	Model string `json:"model,omitempty"`

	// AgentType is the coding agent type (optional)
	AgentType string `json:"agent_type,omitempty"`
}

// TaskToolCallInfo represents information extracted from a Task tool call entry.
type TaskToolCallInfo struct {
	// EntryIndex is the index of this entry in the session
	EntryIndex int

	// Timestamp is when the Task tool was called
	Timestamp time.Time

	// ToolInput is the raw tool input (the task prompt)
	ToolInput string

	// ToolOutput is the raw tool output (the task result)
	ToolOutput string

	// ParsedInput contains structured task input if parseable
	ParsedInput *TaskInput
}

// TaskInput represents the structured input to a Task tool call.
type TaskInput struct {
	// Description is the task description/prompt
	Description string `json:"description,omitempty"`

	// Prompt is an alternative field name for description
	Prompt string `json:"prompt,omitempty"`

	// AgentType hints at what kind of subagent to spawn
	AgentType string `json:"agent_type,omitempty"`
}

// CaptureSubagentOptions configures subagent session capture.
type CaptureSubagentOptions struct {
	// SessionPath is the parent session's folder path
	SessionPath string

	// SessionsBasePath is the base path where sessions are stored
	SessionsBasePath string

	// RecordingStartTime is when the parent recording started
	RecordingStartTime time.Time

	// RecordingEndTime is when the parent recording ended (optional, defaults to now)
	RecordingEndTime time.Time

	// TimeWindowBuffer is extra time to search for subagent sessions (default: 5 minutes)
	TimeWindowBuffer time.Duration
}

// CaptureSubagentResult contains the results of subagent capture.
type CaptureSubagentResult struct {
	// TaskCallsFound is the number of Task tool calls detected
	TaskCallsFound int

	// SubagentsMatched is the number of subagents successfully matched
	SubagentsMatched int

	// EnrichedEntries contains entry indices that were enriched with subagent refs
	EnrichedEntries []int

	// UnmatchedTasks contains indices of Task calls that could not be matched
	UnmatchedTasks []int
}

// DetectTaskToolCalls scans session entries for Task tool calls.
// Returns a list of TaskToolCallInfo for each detected Task tool invocation.
func DetectTaskToolCalls(entries []map[string]any) []TaskToolCallInfo {
	var taskCalls []TaskToolCallInfo

	for i, entry := range entries {
		// check if this is a tool entry
		entryType, _ := entry["type"].(string)
		if entryType != "tool" {
			continue
		}

		// check if the tool is "Task" (case-insensitive)
		toolName, _ := entry["tool_name"].(string)
		if !strings.EqualFold(toolName, "task") {
			continue
		}

		info := TaskToolCallInfo{
			EntryIndex: i,
		}

		// parse timestamp
		if ts, ok := entry["ts"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				info.Timestamp = t
			}
		}
		if info.Timestamp.IsZero() {
			if ts, ok := entry["timestamp"].(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					info.Timestamp = t
				}
			}
		}
		if info.Timestamp.IsZero() {
			if ts, ok := entry["timestamp"].(time.Time); ok {
				info.Timestamp = ts
			}
		}

		// extract tool input/output
		if input, ok := entry["tool_input"].(string); ok {
			info.ToolInput = input
			// try to parse as JSON
			var parsed TaskInput
			if json.Unmarshal([]byte(input), &parsed) == nil {
				info.ParsedInput = &parsed
			}
		}
		if output, ok := entry["tool_output"].(string); ok {
			info.ToolOutput = output
		}

		taskCalls = append(taskCalls, info)
	}

	return taskCalls
}

// FindSubagentSessions searches for subagent sessions that match the given time window.
// It looks for sessions registered via the subagent registry and also scans for
// session folders that fall within the time window.
func FindSubagentSessions(opts CaptureSubagentOptions) ([]*SubagentSession, error) {
	var found []*SubagentSession

	// first, check the subagent registry for explicitly registered subagents
	if opts.SessionPath != "" {
		registry, err := NewSubagentRegistry(opts.SessionPath)
		if err == nil {
			registered, err := registry.List()
			if err == nil {
				found = append(found, registered...)
			}
		}
	}

	// also scan for session folders within the time window
	if opts.SessionsBasePath != "" {
		scanned, err := scanForSubagentSessions(opts)
		if err == nil {
			// merge, avoiding duplicates
			existingIDs := make(map[string]bool)
			for _, t := range found {
				existingIDs[t.SubagentID] = true
			}
			for _, t := range scanned {
				if !existingIDs[t.SubagentID] {
					found = append(found, t)
					existingIDs[t.SubagentID] = true
				}
			}
		}
	}

	return found, nil
}

// scanForSubagentSessions scans session directories for sessions within the time window.
func scanForSubagentSessions(opts CaptureSubagentOptions) ([]*SubagentSession, error) {
	var sessions []*SubagentSession

	// set defaults
	endTime := opts.RecordingEndTime
	if endTime.IsZero() {
		endTime = time.Now()
	}
	buffer := opts.TimeWindowBuffer
	if buffer == 0 {
		buffer = 5 * time.Minute
	}

	// calculate time window
	windowStart := opts.RecordingStartTime.Add(-buffer)
	windowEnd := endTime.Add(buffer)

	// read sessions directory
	entries, err := os.ReadDir(opts.SessionsBasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	parentSessionName := filepath.Base(opts.SessionPath)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// skip legacy directories
		if name == "raw" || name == "events" {
			continue
		}
		// skip the parent session itself
		if name == parentSessionName {
			continue
		}

		// parse timestamp from session name
		sessionTime := parseFilenameTimestamp(name)
		if sessionTime.IsZero() {
			continue
		}

		// check if within time window
		if sessionTime.Before(windowStart) || sessionTime.After(windowEnd) {
			continue
		}

		// read session metadata to build SubagentSession
		sessionPath := filepath.Join(opts.SessionsBasePath, name)
		session := buildSubagentSessionFromSession(sessionPath, name)
		if session != nil {
			sessions = append(sessions, session)
		}
	}

	return sessions, nil
}

// buildSubagentSessionFromSession creates a SubagentSession from a session folder.
func buildSubagentSessionFromSession(sessionPath, sessionName string) *SubagentSession {
	// check for raw.jsonl
	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	info, err := os.Stat(rawPath)
	if err != nil {
		return nil
	}

	// extract agent ID from session name (format: YYYY-MM-DDTHH-MM-username-agentID)
	parts := strings.Split(sessionName, "-")
	var agentID string
	if len(parts) >= 5 {
		// last part is typically the agent ID
		agentID = parts[len(parts)-1]
	}

	session := &SubagentSession{
		SubagentID:  agentID,
		SessionName: sessionName,
		SessionPath: rawPath,
		CompletedAt: info.ModTime().UTC(),
	}

	// try to read entry count and metadata from raw.jsonl
	if stored, err := ReadSessionFromPath(rawPath); err == nil {
		session.EntryCount = len(stored.Entries)
		if stored.Meta != nil {
			session.Model = stored.Meta.Model
			session.AgentType = stored.Meta.AgentType
		}
	}

	return session
}

// MatchTasksToSubagents attempts to match Task tool calls with subagent sessions.
// Returns a map from entry index to matched SubagentReference.
func MatchTasksToSubagents(
	taskCalls []TaskToolCallInfo,
	subagents []*SubagentSession,
) map[int]*SubagentReference {
	matches := make(map[int]*SubagentReference)

	if len(taskCalls) == 0 || len(subagents) == 0 {
		return matches
	}

	// sort task calls by timestamp
	sortedTasks := make([]TaskToolCallInfo, len(taskCalls))
	copy(sortedTasks, taskCalls)
	sort.Slice(sortedTasks, func(i, j int) bool {
		return sortedTasks[i].Timestamp.Before(sortedTasks[j].Timestamp)
	})

	// sort subagents by completion time
	sortedSubagents := make([]*SubagentSession, len(subagents))
	copy(sortedSubagents, subagents)
	sort.Slice(sortedSubagents, func(i, j int) bool {
		return sortedSubagents[i].CompletedAt.Before(sortedSubagents[j].CompletedAt)
	})

	// match each task to the first subagent that completed after the task started
	usedSubagents := make(map[string]bool)

	for _, task := range sortedTasks {
		for _, sub := range sortedSubagents {
			if usedSubagents[sub.SubagentID] {
				continue
			}

			// subagent must have completed after task started
			// allow some flexibility in timing (subagent could start slightly before task call is logged)
			taskStartWindow := task.Timestamp.Add(-30 * time.Second)
			if sub.CompletedAt.After(taskStartWindow) {
				// match found
				ref := &SubagentReference{
					SessionID:   sub.SubagentID,
					SessionPath: sub.SessionName,
					EntryCount:  sub.EntryCount,
					DurationMs:  sub.DurationMs,
					Summary:     sub.Summary,
					Model:       sub.Model,
					AgentType:   sub.AgentType,
				}
				matches[task.EntryIndex] = ref
				usedSubagents[sub.SubagentID] = true
				break
			}
		}
	}

	return matches
}

// EnrichEntriesWithSubagents adds subagent references to session entries.
// Modifies entries in place, adding "subagent" field to matched Task tool calls.
func EnrichEntriesWithSubagents(entries []map[string]any, matches map[int]*SubagentReference) {
	for idx, ref := range matches {
		if idx >= 0 && idx < len(entries) {
			entries[idx]["subagent"] = map[string]any{
				"session_id":   ref.SessionID,
				"session_path": ref.SessionPath,
				"entry_count":  ref.EntryCount,
			}

			// add optional fields if present
			subagentMap := entries[idx]["subagent"].(map[string]any)
			if ref.DurationMs > 0 {
				subagentMap["duration_ms"] = ref.DurationMs
			}
			if ref.Summary != "" {
				subagentMap["summary"] = ref.Summary
			}
			if ref.Model != "" {
				subagentMap["model"] = ref.Model
			}
			if ref.AgentType != "" {
				subagentMap["agent_type"] = ref.AgentType
			}
		}
	}
}

// CaptureSubagentSessions is the main entry point for subagent capture.
// It detects Task tool calls in the given entries, finds matching subagent sessions,
// and enriches the entries with subagent references.
//
// This should be called during the session stop flow, after reading entries
// but before writing the final session.
func CaptureSubagentSessions(
	entries []map[string]any,
	opts CaptureSubagentOptions,
) (*CaptureSubagentResult, error) {
	result := &CaptureSubagentResult{}

	// detect Task tool calls
	taskCalls := DetectTaskToolCalls(entries)
	result.TaskCallsFound = len(taskCalls)

	if len(taskCalls) == 0 {
		return result, nil
	}

	// find subagent sessions
	subagents, err := FindSubagentSessions(opts)
	if err != nil {
		return result, err
	}

	if len(subagents) == 0 {
		// no subagents found, mark all tasks as unmatched
		for _, task := range taskCalls {
			result.UnmatchedTasks = append(result.UnmatchedTasks, task.EntryIndex)
		}
		return result, nil
	}

	// match tasks to subagents
	matches := MatchTasksToSubagents(taskCalls, subagents)
	result.SubagentsMatched = len(matches)

	// track which tasks were matched/unmatched
	for _, task := range taskCalls {
		if _, matched := matches[task.EntryIndex]; matched {
			result.EnrichedEntries = append(result.EnrichedEntries, task.EntryIndex)
		} else {
			result.UnmatchedTasks = append(result.UnmatchedTasks, task.EntryIndex)
		}
	}

	// enrich entries with subagent references
	EnrichEntriesWithSubagents(entries, matches)

	return result, nil
}

// GetSubagentCaptureOptions creates CaptureSubagentOptions from a RecordingState.
// This is a convenience function for the stop recording flow.
func GetSubagentCaptureOptions(state *RecordingState) CaptureSubagentOptions {
	opts := CaptureSubagentOptions{
		SessionPath:        state.SessionPath,
		RecordingStartTime: state.StartedAt,
		RecordingEndTime:   time.Now(),
		TimeWindowBuffer:   5 * time.Minute,
	}

	// determine sessions base path from session path
	if state.SessionPath != "" {
		opts.SessionsBasePath = filepath.Dir(state.SessionPath)
	}

	return opts
}
