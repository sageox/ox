package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// recordingTerminalHandler captures HandleTerminalError calls for assertion.
type recordingTerminalHandler struct {
	mu     sync.Mutex
	events []*adapterprotocol.Event
	types  []string
}

func (h *recordingTerminalHandler) HandleTerminalError(_ context.Context, adapterType string, evt *adapterprotocol.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, evt)
	h.types = append(h.types, adapterType)
}

func (h *recordingTerminalHandler) seen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func newSilentSupervisor() *AdapterSupervisor {
	return NewAdapterSupervisor(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestDispatch_RoutesTerminalErrorToHandler(t *testing.T) {
	s := newSilentSupervisor()
	h := &recordingTerminalHandler{}
	s.SetTerminalErrorHandler(h)

	data, _ := json.Marshal(adapterprotocol.TerminalErrorData{
		Reason: "rate_limited", Source: "regex", PatternID: "p1",
	})
	evt := &adapterprotocol.Event{
		Event:   adapterprotocol.EventTerminalError,
		AgentID: "agent-1",
		Data:    data,
	}
	s.dispatchPushEvent("claude-code", evt)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.seen() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if h.seen() != 1 {
		t.Fatalf("expected 1 terminal event, got %d", h.seen())
	}
	if h.types[0] != "claude-code" {
		t.Errorf("adapter type=%q want %q", h.types[0], "claude-code")
	}
}

func TestDispatch_TerminalErrorPerAgentIsolation(t *testing.T) {
	s := newSilentSupervisor()
	h := &recordingTerminalHandler{}
	s.SetTerminalErrorHandler(h)

	for _, agent := range []string{"a", "b", "c"} {
		data, _ := json.Marshal(adapterprotocol.TerminalErrorData{Reason: "rate_limited"})
		s.dispatchPushEvent("claude-code", &adapterprotocol.Event{
			Event:   adapterprotocol.EventTerminalError,
			AgentID: agent,
			Data:    data,
		})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.seen() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if h.seen() != 3 {
		t.Fatalf("expected 3 events across 3 agents, got %d", h.seen())
	}
}

func TestDispatch_UnknownEventNoOp(t *testing.T) {
	s := newSilentSupervisor()
	h := &recordingTerminalHandler{}
	s.SetTerminalErrorHandler(h)
	// Future-event-name passes through without crash / without invoking handler.
	s.dispatchPushEvent("claude-code", &adapterprotocol.Event{
		Event:   "future_event_v2",
		AgentID: "agent-1",
		Data:    json.RawMessage(`{}`),
	})
	// Settle: drain has 30s idle timer but handler should NEVER be called.
	time.Sleep(50 * time.Millisecond)
	if h.seen() != 0 {
		t.Errorf("unknown event should not invoke handler, got %d", h.seen())
	}
}

func TestDispatch_EntriesEventIgnored(t *testing.T) {
	s := newSilentSupervisor()
	h := &recordingTerminalHandler{}
	s.SetTerminalErrorHandler(h)
	s.dispatchPushEvent("claude-code", &adapterprotocol.Event{
		Event:   adapterprotocol.EventEntries,
		AgentID: "agent-1",
		Data:    json.RawMessage(`{}`),
	})
	time.Sleep(50 * time.Millisecond)
	if h.seen() != 0 {
		t.Errorf("entries event should not invoke terminal handler, got %d", h.seen())
	}
}

func TestDispatch_NoHandlerSilentlyDrops(t *testing.T) {
	s := newSilentSupervisor()
	// No handler set — must not panic.
	data, _ := json.Marshal(adapterprotocol.TerminalErrorData{Reason: "rate_limited"})
	s.dispatchPushEvent("claude-code", &adapterprotocol.Event{
		Event:   adapterprotocol.EventTerminalError,
		AgentID: "agent-1",
		Data:    data,
	})
	// no assertion needed — we just must not crash. Settle for goroutine.
	time.Sleep(50 * time.Millisecond)
}
