package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TerminalErrorHandler is the daemon-side surface for the adapter
// `terminal_error` push event. The supervisor decodes the wire event,
// looks up the per-agent queue, and hands the decoded event to the
// configured handler.
//
// Implementations MUST be safe to call from multiple goroutines and
// MUST NOT block — the per-agent worker pool already serializes events
// for a single agent, so the handler can assume that two simultaneous
// HandleTerminalError calls always carry different agent IDs.
type TerminalErrorHandler interface {
	HandleTerminalError(ctx context.Context, adapterType string, evt *adapterprotocol.Event)
}

// SetTerminalErrorHandler wires the daemon-side handler that fires for
// terminal_error push events. Calling with nil disables routing for the
// event (events are decoded but dropped); the supervisor never panics
// regardless. Safe to call before or after Start.
func (s *AdapterSupervisor) SetTerminalErrorHandler(h TerminalErrorHandler) {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	s.terminalHandler = h
}

// dispatchPushEvent routes an adapter push event to the right downstream
// handler. Always non-blocking on the read loop: terminal events fan out
// through a per-agent queue + drain goroutine; subagent events go to the
// existing synchronous HandleWorkerEvent (already cheap); unknown event
// names are logged.
func (s *AdapterSupervisor) dispatchPushEvent(adapterType string, evt *adapterprotocol.Event) {
	if evt == nil {
		return
	}
	switch evt.Event {
	case adapterprotocol.EventEntries:
		// Entries events are consumed via the session reader path elsewhere.
		return
	case adapterprotocol.EventTerminalError:
		s.submitTerminalEvent(adapterType, evt)
	case adapterprotocol.EventSubagentProgress,
		adapterprotocol.EventSubagentCompleted,
		adapterprotocol.EventSubagentFailed:
		// Existing subagent dispatch is cheap and lock-free w.r.t. the read loop.
		s.HandleWorkerEvent(evt)
	default:
		s.logger.Debug("ignoring unknown push event", "type", adapterType, "event", evt.Event)
	}
}

// submitTerminalEvent enqueues the event on a per-agent buffered channel
// and starts a drain goroutine on first use. Per-agent keying ensures
// finalize for agent A cannot block finalize for agent B and cannot
// deadlock the read loop by RPC-ing back into the same adapter.
func (s *AdapterSupervisor) submitTerminalEvent(adapterType string, evt *adapterprotocol.Event) {
	s.terminalMu.Lock()
	if s.terminalQueues == nil {
		s.terminalQueues = make(map[string]*terminalAgentQueue)
	}
	q, ok := s.terminalQueues[evt.AgentID]
	if !ok {
		q = &terminalAgentQueue{
			ch:          make(chan terminalDispatch, 16),
			adapterType: adapterType,
		}
		s.terminalQueues[evt.AgentID] = q
		go s.drainTerminalQueue(evt.AgentID, q)
	}
	s.terminalMu.Unlock()
	select {
	case q.ch <- terminalDispatch{adapterType: adapterType, evt: evt}:
	default:
		// Queue full — extremely unlikely (16 buffered + slow drain).
		// Drop with a warn rather than blocking the read loop.
		s.logger.Warn("terminal_event queue full, dropping",
			"type", adapterType, "agent_id", evt.AgentID)
	}
}

// drainTerminalQueue runs one goroutine per agent. It exits after the
// queue has been idle for terminalQueueIdleTimeout, releasing the map
// slot. New events for the same agent later spawn a fresh drainer.
func (s *AdapterSupervisor) drainTerminalQueue(agentID string, q *terminalAgentQueue) {
	idle := time.NewTimer(terminalQueueIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case d := <-q.ch:
			s.terminalMu.RLock()
			h := s.terminalHandler
			s.terminalMu.RUnlock()
			if h == nil {
				s.logger.Debug("terminal_error received but no handler configured",
					"type", d.adapterType, "agent_id", d.evt.AgentID)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), terminalHandlerTimeout)
				h.HandleTerminalError(ctx, d.adapterType, d.evt)
				cancel()
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(terminalQueueIdleTimeout)
		case <-idle.C:
			// idle timeout — drop the queue and exit
			s.terminalMu.Lock()
			if cur, ok := s.terminalQueues[agentID]; ok && cur == q {
				delete(s.terminalQueues, agentID)
			}
			s.terminalMu.Unlock()
			return
		}
	}
}

type terminalAgentQueue struct {
	ch          chan terminalDispatch
	adapterType string
}

type terminalDispatch struct {
	adapterType string
	evt         *adapterprotocol.Event
}

const (
	// terminalQueueIdleTimeout drops the per-agent drain goroutine
	// after this much idle time. Keeps the supervisor footprint
	// proportional to active terminal-error traffic, not total agents.
	terminalQueueIdleTimeout = 30 * time.Second

	// terminalHandlerTimeout caps how long the handler may take per
	// event. Finalize involves meta.json mutation + best-effort
	// EndSession RPC; both are fast in practice (well under a second),
	// so 60s is a generous upper bound that still avoids hung daemons
	// when the underlying disk or adapter pipe stalls.
	terminalHandlerTimeout = 60 * time.Second
)

// terminalSupervisorState holds the fields added to AdapterSupervisor
// for terminal_error dispatch. Lives in this file rather than the
// supervisor struct definition so the existing struct stays focused.
// The actual fields are declared on AdapterSupervisor in
// adapter_supervisor.go; this comment documents the contract.
//
// terminalMu       sync.RWMutex
// terminalHandler  TerminalErrorHandler
// terminalQueues   map[string]*terminalAgentQueue
var _ sync.RWMutex
