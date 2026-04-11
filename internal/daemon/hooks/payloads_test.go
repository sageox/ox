package hooks_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/hooks"
)

func TestEventMarshal(t *testing.T) {
	t.Parallel()

	event := hooks.Event{
		Name:      hooks.EventSessionUploaded,
		Timestamp: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		Project:   "/tmp/test-project",
		RepoID:    "repo_test123",
		Payload:   hooks.SessionUploadedPayload("my-session", "https://example.com", "agent-1", 60*time.Second),
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// envelope fields at top level
	if parsed["event"] != "session.uploaded" {
		t.Errorf("event = %v, want session.uploaded", parsed["event"])
	}
	if parsed["project_root"] != "/tmp/test-project" {
		t.Errorf("project_root = %v", parsed["project_root"])
	}
	if parsed["repo_id"] != "repo_test123" {
		t.Errorf("repo_id = %v", parsed["repo_id"])
	}

	// payload merged at top level (no nested "payload" key)
	if _, hasPayload := parsed["payload"]; hasPayload {
		t.Error("payload should be merged at top level, not nested")
	}

	// session data present at top level
	sess, ok := parsed["session"].(map[string]any)
	if !ok {
		t.Fatal("session key missing or wrong type")
	}
	if sess["name"] != "my-session" {
		t.Errorf("session.name = %v, want my-session", sess["name"])
	}
}

func TestMurmurPayload(t *testing.T) {
	t.Parallel()

	p := hooks.MurmurPayload("m-1", "agent-1", "Person A", "wip", "normal", "working on things")
	murmur, ok := p["murmur"].(map[string]any)
	if !ok {
		t.Fatal("expected murmur key")
	}
	if murmur["id"] != "m-1" {
		t.Errorf("id = %v", murmur["id"])
	}
	if murmur["topic"] != "wip" {
		t.Errorf("topic = %v", murmur["topic"])
	}
}

func TestSyncPayload(t *testing.T) {
	t.Parallel()

	p := hooks.SyncPayload("ledger", "pull", 1500*time.Millisecond)
	sync, ok := p["sync"].(map[string]any)
	if !ok {
		t.Fatal("expected sync key")
	}
	if sync["duration_ms"] != int64(1500) {
		t.Errorf("duration_ms = %v, want 1500", sync["duration_ms"])
	}
}

func TestAllEventNamesComplete(t *testing.T) {
	t.Parallel()

	names := hooks.AllEventNames()

	// verify every name is a valid event and there are no duplicates
	seen := make(map[string]bool)
	for _, n := range names {
		if !hooks.ValidEvent(n) {
			t.Errorf("AllEventNames() returned invalid event %q", n)
		}
		if seen[n] {
			t.Errorf("AllEventNames() returned duplicate %q", n)
		}
		seen[n] = true
	}

	if len(names) == 0 {
		t.Fatal("AllEventNames() returned empty slice")
	}
}

func TestValidEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid bool
	}{
		{"daemon.started", true},
		{"session.uploaded", true},
		{"murmur.received", true},
		{"sync.completed", true},
		{"unknown.event", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := hooks.ValidEvent(tt.name); got != tt.valid {
			t.Errorf("ValidEvent(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

// --- Marshal edge cases ---

// TestEventMarshalEmptyPayload verifies marshaling with nil/empty payload
// produces valid JSON with just the envelope fields.
// Failure prevented: nil map iteration causes panic during marshal.
func TestEventMarshalEmptyPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]any
	}{
		{"nil_payload", nil},
		{"empty_payload", map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := hooks.Event{
				Name:      hooks.EventDaemonStarted,
				Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Project:   "/tmp/test",
				RepoID:    "repo-1",
				Payload:   tt.payload,
			}

			data, err := event.Marshal()
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}

			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("invalid JSON: %v\ncontent: %s", err, data)
			}

			// envelope fields must always be present
			if parsed["event"] != "daemon.started" {
				t.Errorf("event = %v", parsed["event"])
			}
			if parsed["project_root"] != "/tmp/test" {
				t.Errorf("project_root = %v", parsed["project_root"])
			}
		})
	}
}

// TestEventMarshalEnvelopeProtectedFromPayload verifies that envelope keys
// (event, timestamp, project_root, repo_id) cannot be overwritten by payload.
// Failure prevented: payload data corrupts event metadata delivered to hooks.
func TestEventMarshalEnvelopeProtectedFromPayload(t *testing.T) {
	t.Parallel()

	event := hooks.Event{
		Name:      hooks.EventDaemonStarted,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Project:   "/tmp/test",
		RepoID:    "repo-123",
		Payload: map[string]any{
			"event":        "overridden.value",
			"timestamp":    "bogus",
			"project_root": "/evil",
			"repo_id":      "fake",
			"extra":        "kept",
		},
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// envelope fields must not be overridden by payload
	if parsed["event"] != hooks.EventDaemonStarted {
		t.Errorf("envelope 'event' was overridden: got %v", parsed["event"])
	}
	if parsed["project_root"] != "/tmp/test" {
		t.Errorf("envelope 'project_root' was overridden: got %v", parsed["project_root"])
	}
	if parsed["repo_id"] != "repo-123" {
		t.Errorf("envelope 'repo_id' was overridden: got %v", parsed["repo_id"])
	}
	// non-reserved payload keys are preserved
	if parsed["extra"] != "kept" {
		t.Errorf("non-reserved payload key 'extra' was dropped: got %v", parsed["extra"])
	}
}

// TestEventMarshalLargePayload verifies marshaling a payload with many keys
// doesn't truncate.
// Failure prevented: buffer size limit silently drops payload data.
func TestEventMarshalLargePayload(t *testing.T) {
	t.Parallel()

	payload := make(map[string]any)
	for i := 0; i < 100; i++ {
		payload[fmt.Sprintf("key_%d", i)] = fmt.Sprintf("value_%d", i)
	}

	event := hooks.Event{
		Name:      hooks.EventDaemonStarted,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Payload:   payload,
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// 100 payload keys + 4 envelope keys
	if len(parsed) != 104 {
		t.Errorf("expected 104 keys, got %d", len(parsed))
	}
}

// TestEventMarshalNilValues verifies payload with nil values marshals as null.
// Failure prevented: nil interface{} causes marshal error or omission.
func TestEventMarshalNilValues(t *testing.T) {
	t.Parallel()

	event := hooks.Event{
		Name:      hooks.EventDaemonStarted,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Payload: map[string]any{
			"nullable_field": nil,
			"nested":         map[string]any{"inner": nil},
		},
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["nullable_field"] != nil {
		t.Errorf("nullable_field should be null, got %v", parsed["nullable_field"])
	}
}

// --- Payload builder coverage ---

// TestAllPayloadBuilders verifies every payload builder returns well-formed data.
// Failure prevented: payload builder returns wrong keys or types.
func TestAllPayloadBuilders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   map[string]any
		topKey    string
		innerKeys []string
	}{
		{
			"murmur",
			hooks.MurmurPayload("m-1", "a-1", "Person A", "wip", "normal", "content"),
			"murmur",
			[]string{"id", "agent_id", "principal", "topic", "importance", "content"},
		},
		{
			"session_uploaded",
			hooks.SessionUploadedPayload("s-1", "https://example.com", "a-1", 60*time.Second),
			"session",
			[]string{"name", "url", "agent_id", "duration_seconds"},
		},
		{
			"session_available",
			hooks.SessionAvailablePayload("s-1", "https://example.com", "Person A", "a-1"),
			"session",
			[]string{"name", "url", "principal", "agent_id"},
		},
		{
			"session_started",
			hooks.SessionPayload("s-1", "a-1"),
			"session",
			[]string{"name", "agent_id"},
		},
		{
			"session_stopped",
			hooks.SessionStoppedPayload("s-1", "a-1", 120*time.Second),
			"session",
			[]string{"name", "agent_id", "duration_seconds"},
		},
		{
			"sync_completed",
			hooks.SyncPayload("ledger", "pull", 1500*time.Millisecond),
			"sync",
			[]string{"workspace", "type", "duration_ms"},
		},
		{
			"sync_failed",
			hooks.SyncFailedPayload("ledger", "pull", "connection refused"),
			"sync",
			[]string{"workspace", "type", "error"},
		},
		{
			"agent_registered",
			hooks.AgentPayload("a-1", "claude-code", "Person A"),
			"agent",
			[]string{"id", "type", "principal"},
		},
		{
			"agent_idle",
			hooks.AgentIdlePayload("a-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			"agent",
			[]string{"id", "idle_since"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner, ok := tt.payload[tt.topKey].(map[string]any)
			if !ok {
				t.Fatalf("expected top-level key %q with map value", tt.topKey)
			}

			for _, key := range tt.innerKeys {
				if _, exists := inner[key]; !exists {
					t.Errorf("missing expected key %q in %s payload", key, tt.name)
				}
			}
		})
	}
}

// TestSessionStoppedPayloadDuration verifies duration is correctly converted to seconds.
// Failure prevented: wrong unit conversion (ms vs s) in duration field.
func TestSessionStoppedPayloadDuration(t *testing.T) {
	t.Parallel()

	p := hooks.SessionStoppedPayload("s-1", "a-1", 90*time.Second)
	sess := p["session"].(map[string]any)
	if sess["duration_seconds"] != 90 {
		t.Errorf("duration_seconds = %v, want 90", sess["duration_seconds"])
	}
}

// TestAgentIdlePayloadTimestamp verifies idle_since is RFC3339 UTC.
// Failure prevented: timezone-dependent timestamp breaks hook parsing.
func TestAgentIdlePayloadTimestamp(t *testing.T) {
	t.Parallel()

	eastern, _ := time.LoadLocation("America/New_York")
	localTime := time.Date(2026, 6, 15, 14, 30, 0, 0, eastern)

	p := hooks.AgentIdlePayload("a-1", localTime)
	agent := p["agent"].(map[string]any)
	idle := agent["idle_since"].(string)

	if idle != "2026-06-15T18:30:00Z" {
		t.Errorf("idle_since = %q, want UTC RFC3339", idle)
	}
}

// TestAllEventNamesSorted verifies AllEventNames returns sorted list.
// Failure prevented: unsorted list breaks binary search or display assumptions.
func TestAllEventNamesSorted(t *testing.T) {
	t.Parallel()

	names := hooks.AllEventNames()
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Fatalf("event names not sorted: %q comes after %q", names[i], names[i-1])
		}
	}
}

// TestValidEventAllKnownEvents verifies every event from AllEventNames() is valid.
// Failure prevented: AllEventNames and ValidEvent disagree on the event set.
func TestValidEventAllKnownEvents(t *testing.T) {
	t.Parallel()

	for _, name := range hooks.AllEventNames() {
		if !hooks.ValidEvent(name) {
			t.Errorf("AllEventNames includes %q but ValidEvent returns false", name)
		}
	}
}
