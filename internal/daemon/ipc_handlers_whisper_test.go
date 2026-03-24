package daemon

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func TestHandleWhispers_ValidPayload(t *testing.T) {
	s := NewServer(nil)

	want := []whisperstore.WhisperEntry{
		{
			ID:        "w-001",
			Scope:     "ledger",
			Topic:     "lint",
			Content:   "fix warnings in main.go",
			CreatedAt: time.Now().UTC(),
		},
		{
			ID:        "w-002",
			Scope:     "team",
			Topic:     "build",
			Content:   "CI is green",
			CreatedAt: time.Now().UTC(),
		},
	}

	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		if agentID != "agent-abc" {
			t.Errorf("expected agentID agent-abc, got %s", agentID)
		}
		return want, nil
	})

	payload, _ := json.Marshal(WhispersPayload{
		AgentID:   "agent-abc",
		Attention: "all",
	})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}

	var resp WhispersResponse
	if err := json.Unmarshal(result.Response.Data, &resp); err != nil {
		t.Fatalf("unmarshal response data: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Entries))
	}
	if resp.Entries[0].ID != "w-001" {
		t.Errorf("expected first entry ID w-001, got %s", resp.Entries[0].ID)
	}
	if resp.Entries[1].ID != "w-002" {
		t.Errorf("expected second entry ID w-002, got %s", resp.Entries[1].ID)
	}
}

func TestHandleWhispers_EmptyAgentID(t *testing.T) {
	s := NewServer(nil)

	payload, _ := json.Marshal(WhispersPayload{AgentID: ""})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if result.Response.Success {
		t.Fatal("expected failure for empty agent_id")
	}
	if result.Response.Error != "agent_id required" {
		t.Errorf("expected 'agent_id required', got %q", result.Response.Error)
	}
}

func TestHandleWhispers_DefaultAttention(t *testing.T) {
	s := NewServer(nil)

	var gotAttention whisperstore.Attention
	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		gotAttention = attention
		return nil, nil
	})

	// send with empty attention field
	payload, _ := json.Marshal(WhispersPayload{AgentID: "agent-xyz"})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}
	if gotAttention != whisperstore.AttentionNormal {
		t.Errorf("expected default attention %q, got %q", whisperstore.AttentionNormal, gotAttention)
	}
}

func TestHandleWhispers_TopicFiltering(t *testing.T) {
	s := NewServer(nil)

	var gotTopics []string
	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		gotTopics = topics
		return []whisperstore.WhisperEntry{}, nil
	})

	topics := []string{"lint", "build", "deploy"}
	payload, _ := json.Marshal(WhispersPayload{
		AgentID:   "agent-filter",
		Attention: "all",
		Topics:    topics,
	})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}
	if len(gotTopics) != len(topics) {
		t.Fatalf("expected %d topics, got %d", len(topics), len(gotTopics))
	}
	for i, topic := range topics {
		if gotTopics[i] != topic {
			t.Errorf("topic[%d]: expected %q, got %q", i, topic, gotTopics[i])
		}
	}
}

func TestHandleWhispers_ServiceError(t *testing.T) {
	s := NewServer(nil)

	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		return nil, fmt.Errorf("store unavailable")
	})

	payload, _ := json.Marshal(WhispersPayload{AgentID: "agent-err"})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if result.Response.Success {
		t.Fatal("expected failure when service returns error")
	}
	if result.Response.Error != "get whispers: store unavailable" {
		t.Errorf("expected 'get whispers: store unavailable', got %q", result.Response.Error)
	}
}

func TestHandleWhispers_NilEntries(t *testing.T) {
	s := NewServer(nil)

	// service returns nil slice
	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		return nil, nil
	})

	payload, _ := json.Marshal(WhispersPayload{AgentID: "agent-nil"})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if !result.Response.Success {
		t.Fatalf("expected success, got error: %s", result.Response.Error)
	}

	var resp WhispersResponse
	if err := json.Unmarshal(result.Response.Data, &resp); err != nil {
		t.Fatalf("unmarshal response data: %v", err)
	}
	// handler normalizes nil to empty slice so JSON serializes as [] not null
	if resp.Entries == nil {
		t.Fatal("expected non-nil entries slice (empty array), got nil")
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(resp.Entries))
	}

	// verify raw JSON is [] not null
	raw := string(result.Response.Data)
	if raw == "" {
		t.Fatal("expected non-empty data")
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(result.Response.Data, &rawMap); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if string(rawMap["entries"]) != "[]" {
		t.Errorf("expected entries JSON to be [], got %s", string(rawMap["entries"]))
	}
}

func TestHandleWhispers_InvalidPayload(t *testing.T) {
	s := NewServer(nil)

	msg := Message{
		Type:    MsgTypeWhispers,
		Payload: json.RawMessage(`{not valid json`),
	}

	result := handleWhispers(s, msg, nil)
	if result.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if result.Response.Success {
		t.Fatal("expected failure for malformed JSON payload")
	}
	if result.Response.Error == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestHandleWhispers_JSONRoundTrip(t *testing.T) {
	s := NewServer(nil)

	now := time.Now().UTC().Truncate(time.Second)
	want := []whisperstore.WhisperEntry{
		{
			ID:            "w-rt-001",
			Scope:         "ledger",
			Type:          whisperstore.WhisperTrigger,
			Source:        "murmur",
			Topic:         "build",
			Content:       "special chars: <>&\"'",
			Importance:    whisperstore.ImportanceCritical,
			CreatedAt:     now,
			AgentID:       "remote-agent",
			PrincipalID:   "user-1",
			PrincipalType: "human",
		},
	}

	s.SetWhispersHandler(func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
		return want, nil
	})

	payload, _ := json.Marshal(WhispersPayload{
		AgentID:   "test-agent",
		Attention: "all",
		Topics:    []string{"build", "lint"},
	})
	msg := Message{Type: MsgTypeWhispers, Payload: payload}

	result := handleWhispers(s, msg, nil)
	if !result.Response.Success {
		t.Fatalf("handler failed: %s", result.Response.Error)
	}

	var resp WhispersResponse
	if err := json.Unmarshal(result.Response.Data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resp.Entries))
	}

	got := resp.Entries[0]
	if got.ID != want[0].ID {
		t.Errorf("ID: got %q, want %q", got.ID, want[0].ID)
	}
	if got.Content != want[0].Content {
		t.Errorf("Content: got %q, want %q", got.Content, want[0].Content)
	}
	if got.Source != want[0].Source {
		t.Errorf("Source: got %q, want %q", got.Source, want[0].Source)
	}
	if string(got.Importance) != string(want[0].Importance) {
		t.Errorf("Importance: got %q, want %q", got.Importance, want[0].Importance)
	}
	if got.AgentID != want[0].AgentID {
		t.Errorf("AgentID: got %q, want %q", got.AgentID, want[0].AgentID)
	}
}
