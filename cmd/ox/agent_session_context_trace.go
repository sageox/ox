package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
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
	Appended   int    `json:"appended"`
	Message    string `json:"message,omitempty"`
}

// runAgentSessionContextTrace appends context-trace events to the current
// session. Reads one JSON event, or JSONL (one event per line) from stdin; a
// batch is all-or-nothing — one malformed line rejects every line.
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

	events, err := parseContextTraceInput(data)
	if err != nil {
		return err
	}

	// find active session
	if !session.IsRecordingForAgent(projectRoot, inst.AgentID) {
		return fmt.Errorf("no active recording for agent %s\nStart recording first: ox agent %s session start", inst.AgentID, inst.AgentID)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil || state == nil || state.SessionPath == "" {
		return fmt.Errorf("could not load recording state for agent %s", inst.AgentID)
	}

	// append the batch in one write: all lines or none
	writer := contexttrace.NewWriter(state.SessionPath)
	if err := writer.AppendAll(events); err != nil {
		return fmt.Errorf("append context-trace event: %w", err)
	}

	// count events for output
	all, _ := contexttrace.ReadEvents(writer.Path())
	eventCount := len(all)

	// output JSON
	output := sessionContextTraceOutput{
		Success:    true,
		Type:       "session_context_trace",
		AgentID:    inst.AgentID,
		TracePath:  writer.Path(),
		EventCount: eventCount,
		Appended:   len(events),
	}
	jsonOut, _ := cli.MarshalJSONIndent(output)
	trackContextBytes(int64(len(jsonOut)))
	return cli.WriteJSONBytes(os.Stdout, jsonOut)
}

// parseContextTraceInput parses stdin as one JSON event or as JSONL. Every
// line must be a valid event with type and source; any failure rejects the
// whole batch so a self-report is never half-written.
func parseContextTraceInput(data []byte) ([]contexttrace.Event, error) {
	trimmed := bytes.TrimSpace(data)
	// A single (possibly pretty-printed, multi-line) object.
	var single contexttrace.Event
	if err := json.Unmarshal(trimmed, &single); err == nil {
		if err := validateContextTraceEvent(single, 0); err != nil {
			return nil, err
		}
		return []contexttrace.Event{single}, nil
	}

	var events []contexttrace.Event
	for i, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev contexttrace.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("invalid JSON event on line %d (nothing written): %w", i+1, err)
		}
		if err := validateContextTraceEvent(ev, i+1); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("invalid JSON event: no events in input")
	}
	return events, nil
}

func validateContextTraceEvent(ev contexttrace.Event, line int) error {
	where := ""
	if line > 0 {
		where = fmt.Sprintf(" (line %d; nothing written)", line)
	}
	if ev.Type == "" {
		return fmt.Errorf("event must have a 'type' field (provided or influenced)%s", where)
	}
	if ev.Source == "" {
		return fmt.Errorf("event must have a 'source' field%s", where)
	}
	return nil
}
