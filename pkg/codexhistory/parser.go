package codexhistory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type codexEntry struct {
	Timestamp string        `json:"timestamp"`
	Type      string        `json:"type"`
	Payload   *codexPayload `json:"payload"`
}

type codexPayload struct {
	ID         string              `json:"id"`
	CWD        string              `json:"cwd"`
	CLIVersion string              `json:"cli_version"`
	Model      string              `json:"model"`
	ItemType   string              `json:"type"`
	Role       string              `json:"role"`
	Content    []codexContentBlock `json:"content"`
	Name       string              `json:"name"`
	Namespace  string              `json:"namespace"`
	Arguments  string              `json:"arguments"`
	Input      string              `json:"input"`
	CallID     string              `json:"call_id"`
	Output     json.RawMessage     `json:"output"`
	// event_msg fields
	Message string `json:"message,omitempty"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func ParseLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "response_item":
		return parseResponseItem(raw.Payload, raw.Timestamp)
	case "event_msg":
		return parseEventMsg(raw.Payload, raw.Timestamp)
	}

	return nil, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseResponseItem(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, fmt.Errorf("missing record payload")
	}

	switch p.ItemType {
	case "message":
		return parseCodexMessage(p, ts)
	case "function_call", "custom_tool_call":
		if p.Name == "" || p.CallID == "" {
			return nil, fmt.Errorf("tool call lacks name or call identity")
		}
		input := p.Arguments
		if p.ItemType == "custom_tool_call" {
			input = p.Input
		}
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolUseWithID(parseTS(ts), p.Name, input, p.CallID),
		}, nil
	case "function_call_output", "custom_tool_call_output":
		if (p.CallID == "" && (p.ID == "" || p.Name == "")) || len(p.Output) == 0 {
			return nil, fmt.Errorf("tool result lacks call identity or output")
		}
		var output string
		if len(p.Output) > 0 {
			if err := json.Unmarshal(p.Output, &output); err != nil {
				// Codex also writes tool output as content blocks. Preserve text
				// and leave image payloads out of the session's text representation.
				var blocks []codexContentBlock
				if err := json.Unmarshal(p.Output, &blocks); err != nil {
					return nil, fmt.Errorf("parse tool output: %w", err)
				}
				var parts []string
				for _, block := range blocks {
					if block.Text != "" {
						parts = append(parts, block.Text)
					}
				}
				output = strings.Join(parts, "\n")
			}
		}
		isErr := IsToolError(output)
		entry := adapterruntime.ToolResultWithID(parseTS(ts), output, isErr, p.CallID)
		// Codex host-injected completions (for example automation_update)
		// carry an item ID and tool name, but no originating call_id. Keep
		// them as standalone results; never invent a matching tool call.
		if p.CallID == "" {
			entry.ToolName = p.Name
			if p.Namespace != "" {
				entry.ToolName = p.Namespace + "." + p.Name
			}
		}
		return []adapterprotocol.RawEntry{entry}, nil
	}

	return nil, nil
}

func parseEventMsg(p *codexPayload, _ string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, fmt.Errorf("missing record payload")
	}

	// user_message events are skipped — response_item/user already captures the
	// same text with richer context (system instructions, content blocks).
	// event_msg types we could extract in the future: task_started (turn
	// boundaries), token_count (usage telemetry).

	return nil, nil
}

func parseCodexMessage(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	t := parseTS(ts)
	switch p.Role {
	case "user":
		text, isSystem := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		if isSystem {
			return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(t, text)}, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.UserEntry(t, text)}, nil

	case "system", "developer":
		text, _ := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(t, text)}, nil

	case "assistant":
		var parts []string
		for _, block := range p.Content {
			if block.Type == "output_text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.AssistantEntry(t, strings.Join(parts, "\n"))}, nil
	default:
		return nil, fmt.Errorf("unsupported message role %q", p.Role)
	}

}

func classifyCodexUserContent(blocks []codexContentBlock) (string, bool) {
	var parts []string
	for _, block := range blocks {
		if block.Type == "input_text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "# AGENTS.md instructions") ||
		strings.HasPrefix(trimmed, "<recommended_plugins>") ||
		strings.HasPrefix(trimmed, "<system-reminder>") ||
		strings.HasPrefix(trimmed, "<permissions instructions>") ||
		strings.HasPrefix(trimmed, "<environment_context>") {
		return text, true
	}
	return text, false
}

// isCodexToolError reports whether a tool result represents a
// failed command. Real exec_command/write_stdin output embeds "Process
// exited with code N" as one line within a multi-line block ("Command:
// ...\nChunk ID: ...\nWall time: ...\nProcess exited with code N\n..."), not
// as a prefix of the whole string. A strict HasPrefix check against the
// entire output therefore never matched a real transcript and silently
// reported every failed command as successful — a real is_error entry
// (case fx_fail1-shaped) surfaced with IsError false.
func IsToolError(output string) bool {
	if output == "" {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		if code, ok := strings.CutPrefix(line, "Process exited with code "); ok {
			return code != "0"
		}
	}
	return false
}
