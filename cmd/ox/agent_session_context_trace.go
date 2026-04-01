package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// sessionContextTraceOutput is the JSON output for the context-trace command.
type sessionContextTraceOutput struct {
	Success    bool   `json:"success"`
	Type       string `json:"type"` // always "session_context_trace"
	AgentID    string `json:"agent_id"`
	TracePath  string `json:"trace_path"`
	EventCount int    `json:"event_count"`
	Message    string `json:"message,omitempty"`
}

// runAgentSessionContextTrace appends a context-trace event to the current session.
// Reads a JSON event from stdin.
// Usage: echo '{"type":"influenced","source":"team-context","doc":"AGENTS.md","decision":"..."}' | ox agent <id> session context-trace
func runAgentSessionContextTrace(inst *agentinstance.Instance, _ []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// read event JSON from stdin
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return fmt.Errorf("no input provided\nUsage: echo '{\"type\":\"influenced\",...}' | ox agent %s session context-trace", inst.AgentID)
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("empty input\nUsage: echo '{\"type\":\"influenced\",...}' | ox agent %s session context-trace", inst.AgentID)
	}

	// parse the event
	var event contexttrace.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("invalid JSON event: %w", err)
	}

	// validate required fields
	if event.Type == "" {
		return fmt.Errorf("event must have a 'type' field (provided or influenced)")
	}
	if event.Source == "" {
		return fmt.Errorf("event must have a 'source' field")
	}

	// find active session
	if !session.IsRecordingForAgent(projectRoot, inst.AgentID) {
		return fmt.Errorf("no active recording for agent %s\nStart recording first: ox agent %s session start", inst.AgentID, inst.AgentID)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil || state == nil || state.SessionPath == "" {
		return fmt.Errorf("could not load recording state for agent %s", inst.AgentID)
	}

	// append the event
	writer := contexttrace.NewWriter(state.SessionPath)
	if err := writer.Append(event); err != nil {
		return fmt.Errorf("append context-trace event: %w", err)
	}

	// count events for output
	events, _ := contexttrace.ReadEvents(writer.Path())
	eventCount := len(events)

	// output JSON
	output := sessionContextTraceOutput{
		Success:    true,
		Type:       "session_context_trace",
		AgentID:    inst.AgentID,
		TracePath:  writer.Path(),
		EventCount: eventCount,
	}
	jsonOut, _ := json.MarshalIndent(output, "", "  ")
	trackContextBytes(int64(len(jsonOut)))
	fmt.Println(string(jsonOut))
	return nil
}
