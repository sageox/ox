package agentx

import (
	"encoding/json"
	"io"
	"os"
)

// maxHookInputSize is the maximum stdin payload size for hook input (256KB).
// Tool payloads (bash output, file contents) can be large.
const maxHookInputSize = 256 * 1024

// HookInput is the generalized stdin JSON payload from any coding agent's hook system.
// Agents pipe event context to hook commands via stdin as JSON. This struct captures
// the common fields across agents (Claude Code, Cursor, Windsurf, Cline, etc.).
//
// Not all fields are present for every event — tool fields only appear for tool events,
// Source only for session start/end, etc.
//
// RawBytes preserves the original stdin bytes for faithful passthrough to subprocesses.
// This avoids losing unknown/agent-specific fields during re-serialization.
type HookInput struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	Source        string          `json:"source,omitempty"`         // session start/end source (startup, resume, clear, compact)
	Trigger       string          `json:"trigger,omitempty"`        // compact trigger (manual, auto)
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	ToolError     string          `json:"error,omitempty"`

	// RawBytes is the original stdin payload, preserved for faithful passthrough
	// to subprocesses. Not serialized to JSON — used internally only.
	RawBytes []byte `json:"-"`
}

// ReadHookInput reads and parses hook input JSON from the given reader.
// Returns nil if the reader is empty or doesn't contain valid JSON.
// Use with os.Stdin in production; pass any io.Reader for testing.
//
// Reads up to maxHookInputSize (256KB) to accommodate large tool payloads.
// Uses io.ReadAll (with a limit) to handle fragmented pipe reads correctly.
func ReadHookInput(r io.Reader) *HookInput {
	data, err := io.ReadAll(io.LimitReader(r, maxHookInputSize))
	if err != nil || len(data) == 0 {
		return nil
	}

	var input HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil
	}

	input.RawBytes = data
	return &input
}

// ReadHookInputFromStdin reads hook input from os.Stdin, returning nil if
// stdin is a terminal (not a pipe) or doesn't contain valid JSON.
// This is the standard entry point for CLI hook commands.
//
// Note: On some Windows terminal environments, the pipe detection via
// os.ModeCharDevice may not be reliable. In those cases, this returns nil
// and the hook proceeds without parsed input.
func ReadHookInputFromStdin() *HookInput {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil
	}

	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return nil // stdin is a terminal
	}

	return ReadHookInput(os.Stdin)
}
