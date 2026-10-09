package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

var errCursorSourceFormat = errors.New("unsupported-source-format")

// parseCursorLine preserves the exported block order. Cursor's JSONL does not
// contain the tool results or IDs available in other native stores.
func parseCursorLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var row struct {
		Type    string          `json:"type"`
		Status  string          `json:"status"`
		Role    string          `json:"role"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON record", errCursorSourceFormat)
	}
	if row.Type == "turn_ended" && row.Role == "" && row.Status != "" {
		return nil, nil
	}
	if row.Type != "" || (row.Role != adapterprotocol.RoleUser && row.Role != adapterprotocol.RoleAssistant) {
		return nil, fmt.Errorf("%w: unsupported record", errCursorSourceFormat)
	}
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(row.Message, &message); err != nil {
		return nil, fmt.Errorf("%w: invalid message", errCursorSourceFormat)
	}
	content := bytes.TrimSpace(message.Content)
	if len(content) == 0 || content[0] != '[' {
		return nil, fmt.Errorf("%w: expected content array", errCursorSourceFormat)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, fmt.Errorf("%w: invalid content array", errCursorSourceFormat)
	}
	entries := make([]adapterprotocol.RawEntry, 0, len(blocks))
	for _, raw := range blocks {
		var block struct {
			Type  string          `json:"type"`
			Text  *string         `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, fmt.Errorf("%w: invalid content block", errCursorSourceFormat)
		}
		switch block.Type {
		case "text":
			if block.Text == nil {
				return nil, fmt.Errorf("%w: text must be a string", errCursorSourceFormat)
			}
			entries = append(entries, adapterprotocol.RawEntry{Role: row.Role, Content: *block.Text})
		case "tool_use":
			if row.Role != adapterprotocol.RoleAssistant || strings.TrimSpace(block.Name) == "" || len(block.Input) == 0 {
				return nil, fmt.Errorf("%w: invalid tool use", errCursorSourceFormat)
			}
			var input bytes.Buffer
			if err := json.Compact(&input, block.Input); err != nil {
				return nil, fmt.Errorf("%w: invalid tool input", errCursorSourceFormat)
			}
			entries = append(entries, adapterprotocol.RawEntry{
				Role: adapterprotocol.RoleTool, ToolName: block.Name, ToolInput: input.String(),
			})
		default:
			return nil, fmt.Errorf("%w: unsupported content block", errCursorSourceFormat)
		}
	}
	return entries, nil
}
