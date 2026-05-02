package contexttrace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileName is the canonical name for the context-trace artifact.
const FileName = "context-trace.jsonl"

// EventType distinguishes what kind of context-trace event this is.
type EventType string

const (
	// EventProvided is emitted when context becomes available to the agent.
	EventProvided EventType = "provided"
	// EventInfluenced is emitted when the agent attributes a decision to specific context.
	EventInfluenced EventType = "influenced"
)

// SourceType identifies where the context came from.
type SourceType string

const (
	SourceTeamContext    SourceType = "team-context"
	SourceTeamMemory     SourceType = "team-memory"
	SourceTeamDocs       SourceType = "team-docs"
	SourceProjectConfig  SourceType = "project-config"
	SourceTeamWhisper    SourceType = "team-whisper"
	SourceProjectWhisper SourceType = "project-whisper"
	SourceOnDemand       SourceType = "on-demand"
)

// Event is a single context-trace entry.
type Event struct {
	Type      EventType  `json:"type"`
	Timestamp string     `json:"ts"`
	Source    SourceType `json:"source"`

	// provided: what was loaded
	Team         string   `json:"team,omitempty"`
	Doc          string   `json:"doc,omitempty"`
	Docs         []string `json:"docs,omitempty"`
	Section      string   `json:"section,omitempty"`
	InlineTokens int      `json:"inline_tokens,omitempty"`
	ReadOnDemand bool     `json:"read_on_demand,omitempty"`

	// whisper fields (team-whisper, project-whisper)
	From  string `json:"from,omitempty"`
	Topic string `json:"topic,omitempty"`

	// influenced: what mattered
	Decision string `json:"decision,omitempty"`
	PlanStep int    `json:"plan_step,omitempty"`
}

// Now returns the current time formatted for context-trace timestamps.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// Writer appends events to a context-trace.jsonl file.
type Writer struct {
	path string
}

// NewWriter creates a Writer that appends to context-trace.jsonl in the given session dir.
func NewWriter(sessionDir string) *Writer {
	return &Writer{path: filepath.Join(sessionDir, FileName)}
}

// Path returns the full path to the context-trace.jsonl file.
func (w *Writer) Path() string {
	return w.path
}

// Append marshals the event to JSON and appends it as a single line.
// Creates the file if it doesn't exist. Safe for sequential use; not goroutine-safe.
func (w *Writer) Append(event Event) error {
	if event.Timestamp == "" {
		event.Timestamp = Now()
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal context-trace event: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open context-trace file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write context-trace event: %w", err)
	}
	return nil
}

// ReadEvents reads all events from a context-trace.jsonl file.
// Malformed lines are skipped. Returns nil, nil for a missing file.
func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open context-trace file: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("scan context-trace file: %w", err)
	}
	return events, nil
}
