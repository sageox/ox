package hooks

import (
	"encoding/json"
	"sort"
	"time"
)

// Event name constants. Dot-separated: category.action.
const (
	EventDaemonStarted    = "daemon.started"
	EventDaemonStopped    = "daemon.stopped"
	EventSessionStarted   = "session.started"
	EventSessionStopped   = "session.stopped"
	EventSessionUploaded  = "session.uploaded"
	EventSessionAvailable = "session.available"
	EventMurmurReceived   = "murmur.received"
	EventMurmurCritical   = "murmur.critical"
	EventSyncCompleted    = "sync.completed"
	EventSyncFailed       = "sync.failed"
	EventAgentRegistered  = "agent.registered"
	EventAgentIdle        = "agent.idle"
)

var allEvents = map[string]bool{
	EventDaemonStarted: true, EventDaemonStopped: true,
	EventSessionStarted: true, EventSessionStopped: true,
	EventSessionUploaded: true, EventSessionAvailable: true,
	EventMurmurReceived: true, EventMurmurCritical: true,
	EventSyncCompleted: true, EventSyncFailed: true,
	EventAgentRegistered: true, EventAgentIdle: true,
}

// ValidEvent returns true if name is a known event name.
func ValidEvent(name string) bool { return allEvents[name] }

// AllEventNames returns all valid event names sorted alphabetically.
func AllEventNames() []string {
	names := make([]string, 0, len(allEvents))
	for name := range allEvents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Event represents a daemon event that can trigger notifications and hooks.
type Event struct {
	Name      string         `json:"event"`
	Timestamp time.Time      `json:"timestamp"`
	Project   string         `json:"project_root"`
	RepoID    string         `json:"repo_id"`
	Payload   map[string]any `json:"-"` // merged into top-level JSON, not nested
}

// Marshal serializes the event into JSON, merging the common envelope
// with the Payload fields at the top level.
func (e Event) Marshal() ([]byte, error) {
	merged := map[string]any{
		"event":        e.Name,
		"timestamp":    e.Timestamp.UTC().Format(time.RFC3339),
		"project_root": e.Project,
		"repo_id":      e.RepoID,
	}
	for k, v := range e.Payload {
		// don't let payload overwrite envelope fields
		if k == "event" || k == "timestamp" || k == "project_root" || k == "repo_id" {
			continue
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}
