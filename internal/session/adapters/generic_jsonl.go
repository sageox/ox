package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// GenericJSONLAdapter reads SageOx-compatible JSONL from a file that any coding agent writes to.
// Unlike the Claude Code adapter which reads agent-native files, this adapter reads a
// standardized format that agents write to via `ox agent session log` or direct JSONL writes.
type GenericJSONLAdapter struct{}

func init() {
	Register(&GenericJSONLAdapter{})
}

// Name returns the adapter identifier.
func (a *GenericJSONLAdapter) Name() string { return "generic" }

// Detect always returns false. The generic adapter is never auto-detected --
// it is used via explicit GetAdapter() or alias resolution. Deep adapters
// (e.g. Claude Code) take priority via DetectAdapter().
func (a *GenericJSONLAdapter) Detect() bool {
	return false
}

// FindSessionFile is not supported by the generic adapter. The session file
// path is created by session start and passed through recording state.
func (a *GenericJSONLAdapter) FindSessionFile(_ string, _ time.Time) (string, error) {
	return "", ErrSessionNotFound
}

// Read parses all entries from a SageOx-compatible JSONL session file.
// It streams line-by-line, skipping header/footer lines and malformed JSON.
// Empty files return an empty slice (not an error).
func (a *GenericJSONLAdapter) Read(sessionPath string) ([]RawEntry, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []RawEntry
	scanner := bufio.NewScanner(f)
	// no size limit on the scanner buffer for large tool outputs
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB max line size

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			slog.Warn("skipping malformed entry", "line", lineNum, "error", err)
			continue
		}

		entryType, _ := raw["type"].(string)

		// skip header and footer lines (including duplicate headers from retry concatenation)
		if entryType == "header" || entryType == "footer" {
			continue
		}

		entry := RawEntry{
			Raw: json.RawMessage(append([]byte(nil), line...)),
		}

		// map type -> Role
		entry.Role, _ = raw["type"].(string)

		// map content -> Content
		entry.Content, _ = raw["content"].(string)

		// parse timestamp as RFC3339
		if ts, ok := raw["timestamp"].(string); ok && ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				entry.Timestamp = t
			} else {
				slog.Warn("invalid timestamp format", "line", lineNum, "timestamp", ts, "error", err)
			}
		}

		// map tool fields
		entry.ToolName, _ = raw["tool_name"].(string)
		entry.ToolInput, _ = raw["tool_input"].(string)

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// ReadMetadata extracts session metadata from the first line of a JSONL file.
// If the first line is a header with a metadata object, agent_version and model
// are extracted. Returns nil if no header or the fields are missing.
func (a *GenericJSONLAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	if !scanner.Scan() {
		// empty file or read error
		return nil, scanner.Err()
	}

	line := scanner.Bytes()
	if len(line) == 0 {
		return nil, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, nil
	}

	entryType, _ := raw["type"].(string)
	if entryType != "header" {
		return nil, nil
	}

	metaObj, ok := raw["metadata"].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	agentVersion, _ := metaObj["agent_version"].(string)
	model, _ := metaObj["model"].(string)

	if agentVersion == "" && model == "" {
		return nil, nil
	}

	return &SessionMetadata{
		AgentVersion: agentVersion,
		Model:        model,
	}, nil
}

// Watch is not supported by the generic adapter. Session data is read at
// stop time via Read(), not streamed in real time.
func (a *GenericJSONLAdapter) Watch(_ context.Context, _ string) (<-chan RawEntry, error) {
	return nil, ErrWatchNotSupported
}
