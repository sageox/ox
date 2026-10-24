package oxactivity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// StateKey names the state file for one harness session of one AI coworker:
// "<agentID>" alone when sessionID is empty, else "<agentID>.<sessionID>".
// An ox agent ID can outlive a Claude Code or Codex session and be shared by
// several windows in one repo, so state keyed by it alone shows one window's
// "checked murmurs" in another. The harness session ID is what makes the
// line belong to the session the coworker is looking at. A session ID with
// characters unsafe in a file name is replaced by a short hash of itself.
func StateKey(agentID, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentID
	}
	if !sessionIDPattern.MatchString(sessionID) {
		sum := sha256.Sum256([]byte(sessionID))
		sessionID = hex.EncodeToString(sum[:8])
	}
	return agentID + "." + sessionID
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// StatePath returns <projectRoot>/.sageox/cache/statusline/<key>.json for a
// key from StateKey (or a bare agent ID), or "" when the key is malformed.
func StatePath(projectRoot, key string) string {
	agentID, sessionID, dotted := strings.Cut(key, ".")
	if !agentinstance.IsValidAgentID(agentID) {
		return ""
	}
	if dotted && !sessionIDPattern.MatchString(sessionID) {
		return ""
	}
	return filepath.Join(stateDir(projectRoot), key+".json")
}

func stateDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".sageox", "cache", "statusline")
}

// LoadState reads the activity state for key (see StateKey). A missing,
// unreadable, or corrupt file, or a malformed key, yields a zero State: the
// activity line is best-effort and must never fail the caller.
func LoadState(projectRoot, key string) State {
	var s State
	path := StatePath(projectRoot, key)
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is built from a validated key
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// UpdateState applies update to the stored state under a file lock, so two
// hook processes (a tool call and a prompt submit) that overlap cannot each
// restore the other's field from a stale snapshot. update reports whether
// anything changed; false skips the write. A missing or corrupt file starts
// from a zero State, as LoadState does.
func UpdateState(projectRoot, key string, update func(*State) bool) error {
	path := StatePath(projectRoot, key)
	if path == "" {
		return fmt.Errorf("update activity state: %w: %q", ErrInvalidAgentID, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("update activity state: %w", err)
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		s := LoadState(projectRoot, key)
		if !update(&s) {
			return nil
		}
		return SaveState(projectRoot, key, s)
	})
}

// SaveState atomically writes the activity state for key (see StateKey),
// creating the cache directory if needed. State files of sessions idle for
// more than StateRetention are removed at the same time, so per-session
// files never pile up.
func SaveState(projectRoot, key string, s State) error {
	path := StatePath(projectRoot, key)
	if path == "" {
		return fmt.Errorf("save activity state: %w: %q", ErrInvalidAgentID, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(path, s, 0o600); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	pruneStale(filepath.Dir(path), path, time.Now())
	return nil
}

// StateRetention is how long an untouched session state file is kept.
const StateRetention = 24 * time.Hour

// pruneStale removes per-session state files ("<agentID>.<sessionID>.json")
// not modified within StateRetention. Other files in the directory, such as
// agent-wide state or the murmur cache, are left alone. It is best-effort: the
// state just written is never removed and errors are ignored, since this runs
// inside a harness hook.
func pruneStale(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() || p == keep || !isSessionStateFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= StateRetention {
			continue
		}
		_ = os.Remove(p)
	}
}

// isSessionStateFile reports whether name is "<agentID>.<sessionID>.json".
func isSessionStateFile(name string) bool {
	key, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return false
	}
	agentID, sessionID, dotted := strings.Cut(key, ".")
	return dotted && agentinstance.IsValidAgentID(agentID) && sessionIDPattern.MatchString(sessionID)
}
