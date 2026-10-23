package oxactivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/fileutil"
)

// ErrInvalidAgentID is returned by SaveState for an agent ID that is not a
// well-formed ox agent ID, which also keeps it from naming a path outside the
// state directory.
var ErrInvalidAgentID = errors.New("invalid agent ID")

// State is the latest coworker-visible ox activity for one AI coworker. It is
// local-only derived data, rewritten whenever a harness hook classifies a new
// action or receives a whisper.
type State struct {
	LastAction         string    `json:"last_action,omitempty"`
	LastActionAt       time.Time `json:"last_action_at,omitzero"`
	LastWhisperFrom    string    `json:"last_whisper_from,omitempty"`
	LastWhisperTopic   string    `json:"last_whisper_topic,omitempty"`
	LastWhisperContent string    `json:"last_whisper_content,omitempty"`
	LastWhisperAt      time.Time `json:"last_whisper_at,omitzero"`
}

// Segment converts the state into what Render draws. Age is measured from the
// whisper when there is one (it is what the line ends on), otherwise from the
// action.
func (s State) Segment(now time.Time) Segment {
	seg := Segment{
		Action:         s.LastAction,
		WhisperFrom:    s.LastWhisperFrom,
		WhisperTopic:   s.LastWhisperTopic,
		WhisperContent: s.LastWhisperContent,
	}
	at := s.LastActionAt
	if s.LastWhisperFrom != "" || s.LastWhisperContent != "" {
		at = s.LastWhisperAt
	}
	if !at.IsZero() {
		seg.Age = now.Sub(at)
	}
	return seg
}

// StatePath returns <projectRoot>/.sageox/cache/statusline/<agentID>.json, or
// "" when agentID is not a valid ox agent ID.
func StatePath(projectRoot, agentID string) string {
	if !agentinstance.IsValidAgentID(agentID) {
		return ""
	}
	return filepath.Join(projectRoot, ".sageox", "cache", "statusline", agentID+".json")
}

// LoadState reads an AI coworker's activity state. A missing, unreadable, or
// corrupt file, or an invalid agent ID, yields a zero State: the activity line
// is best-effort and must never fail the caller.
func LoadState(projectRoot, agentID string) State {
	var s State
	path := StatePath(projectRoot, agentID)
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is built from a validated agent ID
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// SaveState atomically writes an AI coworker's activity state, creating the
// cache directory if needed.
func SaveState(projectRoot, agentID string, s State) error {
	path := StatePath(projectRoot, agentID)
	if path == "" {
		return fmt.Errorf("save activity state: %w: %q", ErrInvalidAgentID, agentID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(path, s, 0o600); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	return nil
}
