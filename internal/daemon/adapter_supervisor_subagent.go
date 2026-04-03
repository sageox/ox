package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

var (
	// ErrNoSubagentCapability is returned when the adapter does not support subagent control.
	ErrNoSubagentCapability = errors.New("adapter does not have subagent_controller capability")

	// ErrWorkerNotFound is returned when the requested worker_id is not tracked.
	ErrWorkerNotFound = errors.New("worker not found")
)

// SpawnSubagent routes a spawn-subagent request to the appropriate adapter.
// It validates capacity, registers the worker in the tracker, then sends to the adapter.
func (s *AdapterSupervisor) SpawnSubagent(ctx context.Context, adapterType string, params *adapterprotocol.SpawnSubagentParams) (*adapterprotocol.SpawnSubagentResult, error) {
	if s.workers == nil {
		return nil, errors.New("worker tracker not initialized")
	}

	// check adapter has subagent_controller capability
	if !s.hasCapability(adapterType, adapterprotocol.CapSubagentController) {
		return nil, fmt.Errorf("%w: %s", ErrNoSubagentCapability, adapterType)
	}

	// register worker in tracker (checks capacity)
	ws := &WorkerState{
		WorkerID:    params.WorkerID,
		AgentID:     params.AgentID,
		AdapterType: adapterType,
		Status:      adapterprotocol.WorkerStatusStarting,
		StartedAt:   time.Now(),
		Task:        params.Task,
		Model:       params.Model,
	}
	if err := s.workers.Register(ws); err != nil {
		return nil, fmt.Errorf("register worker: %w", err)
	}

	// send to adapter
	resp, err := s.SendRequest(ctx, adapterType, params.AgentID, adapterprotocol.MethodSpawnSubagent, params)
	if err != nil {
		// rollback tracker registration on send failure
		s.workers.Remove(params.WorkerID)
		return nil, fmt.Errorf("spawn-subagent request: %w", err)
	}

	if resp.Error != nil {
		s.workers.Remove(params.WorkerID)
		return nil, fmt.Errorf("adapter error: %s: %s", resp.Error.Code, resp.Error.Message)
	}

	// decode result
	var result adapterprotocol.SpawnSubagentResult
	if err := decodeResult(resp.Result, &result); err != nil {
		s.workers.Remove(params.WorkerID)
		return nil, fmt.Errorf("decode spawn result: %w", err)
	}

	s.logger.Info("subagent spawned", "worker_id", params.WorkerID, "agent_id", params.AgentID, "adapter", adapterType, "task", params.Task)
	return &result, nil
}

// SubagentStatus routes a subagent-status request to the adapter.
// If the worker is tracked locally, supplements with tracker data.
func (s *AdapterSupervisor) SubagentStatus(ctx context.Context, adapterType string, params *adapterprotocol.SubagentStatusParams) (*adapterprotocol.SubagentStatusResult, error) {
	if s.workers == nil {
		return nil, errors.New("worker tracker not initialized")
	}

	// check worker exists in tracker
	ws, ok := s.workers.Get(params.WorkerID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrWorkerNotFound, params.WorkerID)
	}

	resp, err := s.SendRequest(ctx, adapterType, ws.AgentID, adapterprotocol.MethodSubagentStatus, params)
	if err != nil {
		return nil, fmt.Errorf("subagent-status request: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("adapter error: %s: %s", resp.Error.Code, resp.Error.Message)
	}

	var result adapterprotocol.SubagentStatusResult
	if err := decodeResult(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode status result: %w", err)
	}

	return &result, nil
}

// CancelSubagent routes a cancel-subagent request to the adapter.
func (s *AdapterSupervisor) CancelSubagent(ctx context.Context, adapterType string, params *adapterprotocol.CancelSubagentParams) (*adapterprotocol.CancelSubagentResult, error) {
	if s.workers == nil {
		return nil, errors.New("worker tracker not initialized")
	}

	// check worker exists
	ws, ok := s.workers.Get(params.WorkerID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrWorkerNotFound, params.WorkerID)
	}

	resp, err := s.SendRequest(ctx, adapterType, ws.AgentID, adapterprotocol.MethodCancelSubagent, params)
	if err != nil {
		return nil, fmt.Errorf("cancel-subagent request: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("adapter error: %s: %s", resp.Error.Code, resp.Error.Message)
	}

	var result adapterprotocol.CancelSubagentResult
	if err := decodeResult(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode cancel result: %w", err)
	}

	// update tracker to canceling
	s.workers.UpdateStatus(params.WorkerID, adapterprotocol.WorkerStatusCanceling)

	s.logger.Info("subagent cancel requested", "worker_id", params.WorkerID, "reason", params.Reason)
	return &result, nil
}

// HandleWorkerEvent processes push events from adapters related to subagent workers.
// Called when the adapter sends subagent.progress, subagent.completed, or subagent.failed events.
func (s *AdapterSupervisor) HandleWorkerEvent(evt *adapterprotocol.Event) {
	if s.workers == nil {
		return
	}

	switch evt.Event {
	case adapterprotocol.EventSubagentProgress:
		var data adapterprotocol.SubagentProgressData
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			s.logger.Warn("failed to decode subagent.progress event", "error", err)
			return
		}
		// update status to running if still starting
		ws, ok := s.workers.Get(data.WorkerID)
		if ok && ws.Status == adapterprotocol.WorkerStatusStarting {
			s.workers.UpdateStatus(data.WorkerID, adapterprotocol.WorkerStatusRunning)
		}
		s.logger.Debug("subagent progress", "worker_id", data.WorkerID, "output_type", data.OutputType, "description", data.Description)

	case adapterprotocol.EventSubagentCompleted:
		var data adapterprotocol.SubagentCompletedData
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			s.logger.Warn("failed to decode subagent.completed event", "error", err)
			return
		}
		s.workers.UpdateStatus(data.WorkerID, adapterprotocol.WorkerStatusCompleted)
		s.logger.Info("subagent completed", "worker_id", data.WorkerID, "duration_sec", data.DurationSec, "summary", data.Summary)

	case adapterprotocol.EventSubagentFailed:
		var data adapterprotocol.SubagentFailedData
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			s.logger.Warn("failed to decode subagent.failed event", "error", err)
			return
		}
		// map exit_reason to appropriate terminal status
		status := adapterprotocol.WorkerStatusFailed
		switch data.ExitReason {
		case "canceled":
			status = adapterprotocol.WorkerStatusCanceled
		case "timed_out":
			status = adapterprotocol.WorkerStatusTimedOut
		}
		s.workers.UpdateStatus(data.WorkerID, status)
		s.logger.Info("subagent failed", "worker_id", data.WorkerID, "exit_reason", data.ExitReason, "error", data.Error, "duration_sec", data.DurationSec)

	default:
		s.logger.Warn("unknown subagent event", "event", evt.Event)
	}
}

// SetWorkerTracker sets the worker tracker on the supervisor.
// Must be called before any subagent operations.
func (s *AdapterSupervisor) SetWorkerTracker(wt *WorkerTracker) {
	s.workers = wt
}

// Workers returns the worker tracker for external status queries.
func (s *AdapterSupervisor) Workers() *WorkerTracker {
	return s.workers
}

// hasCapability checks whether the adapter type has the given capability.
func (s *AdapterSupervisor) hasCapability(adapterType, capability string) bool {
	s.mu.RLock()
	caps, exists := s.adapterCaps[adapterType]
	s.mu.RUnlock()
	if !exists {
		return false
	}
	return slices.Contains(caps, capability)
}

// SetAdapterCapabilities records the capabilities for an adapter type.
// Called when the adapter's info response is first received.
func (s *AdapterSupervisor) SetAdapterCapabilities(adapterType string, capabilities []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapterCaps == nil {
		s.adapterCaps = make(map[string][]string)
	}
	s.adapterCaps[adapterType] = capabilities
}

// decodeResult marshals resp.Result (which is any) back to JSON and decodes into target.
func decodeResult(result any, target any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
