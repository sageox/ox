package daemon

import (
	"encoding/json"
	"fmt"
	"net"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// marshalResponse creates a success response with marshaled data.
// If marshaling fails, returns an error response instead of Success: true with nil data.
func marshalResponse(v any) *Response {
	data, err := json.Marshal(v)
	if err != nil {
		// this should never happen for our well-typed structs, but handle defensively
		return &Response{Success: false, Error: fmt.Sprintf("marshal error: %v", err)}
	}
	return &Response{Success: true, Data: data}
}

func handlePing(_ *Server, _ Message, _ net.Conn) HandlerResult {
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`"pong"`)},
	}
}

func handleVersion(_ *Server, _ Message, _ net.Conn) HandlerResult {
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`"1.0.0"`)},
	}
}

func handleStatus(s *Server, _ Message, _ net.Conn) HandlerResult {
	status := s.service.Status()
	if status != nil {
		return HandlerResult{Response: marshalResponse(status)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{}`)},
	}
}

func handleSyncHistory(s *Server, _ Message, _ net.Conn) HandlerResult {
	history := s.service.SyncHistory()
	if history != nil {
		return HandlerResult{Response: marshalResponse(history)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`[]`)},
	}
}

func handleSync(s *Server, _ Message, conn net.Conn) HandlerResult {
	// prefer the progress-aware path; fall back to the simple sync for backward compat
	// (CallbackService.SyncWithProgress returns nil error when onSyncWithProgress is unset)
	var resp Response

	// check if progress handler is wired by attempting it; CallbackService.SyncWithProgress
	// returns nil when callback is nil, which is indistinguishable from success.
	// We distinguish by checking the svc field directly for CallbackService.
	if cs, ok := s.service.(*CallbackService); ok {
		cs.mu.Lock()
		hasProgress := cs.onSyncWithProgress != nil
		hasLegacy := cs.onSync != nil
		cs.mu.Unlock()

		if hasProgress {
			pw := &ProgressWriter{conn: conn}
			if err := s.service.SyncWithProgress(pw); err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		} else if hasLegacy {
			if err := s.service.Sync(); err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		} else {
			resp = Response{Success: false, Error: "sync handler not set"}
		}
	} else {
		// non-callback service: always use SyncWithProgress
		pw := &ProgressWriter{conn: conn}
		if err := s.service.SyncWithProgress(pw); err != nil {
			resp = Response{Success: false, Error: err.Error()}
		} else {
			resp = Response{Success: true}
		}
	}

	return HandlerResult{Response: &resp}
}

func handleTeamSync(s *Server, _ Message, conn net.Conn) HandlerResult {
	// check if handler is wired before invoking, so we can return a clear error
	if cs, ok := s.service.(*CallbackService); ok {
		cs.mu.Lock()
		has := cs.onTeamSync != nil
		cs.mu.Unlock()
		if !has {
			return HandlerResult{
				Response: &Response{Success: false, Error: "team sync handler not set"},
			}
		}
	}

	pw := &ProgressWriter{conn: conn}
	var resp Response
	if err := s.service.TeamSync(pw); err != nil {
		resp = Response{Success: false, Error: err.Error()}
	} else {
		resp = Response{Success: true}
	}
	return HandlerResult{Response: &resp}
}

func handleStop(s *Server, _ Message, conn net.Conn) HandlerResult {
	// send response before stopping
	resp := Response{Success: true}
	s.sendResponse(conn, resp)
	s.service.Stop()
	return HandlerResult{SkipDefault: true}
}

// handleHeartbeat processes CLI heartbeat messages (fire-and-forget).
//
// Credential handling note: Heartbeat payloads may include credentials as an optimization
// for early refresh, but this is NOT a critical path. The daemon has its own credential
// management via refreshCredentialsIfNeeded() which uses gitserver.LoadCredentialsForEndpoint()
// and auth.GetTokenForEndpoint() to load fresh credentials from disk or refresh via API.
// If heartbeat credentials are lost (network issue, daemon busy), the daemon will refresh
// credentials normally on the next sync operation.
func handleHeartbeat(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.service.Heartbeat(msg.CallerID, msg.Payload)
	// fire-and-forget: no response needed, credential loss is acceptable
	return HandlerResult{SkipDefault: true}
}

func handleTelemetry(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.service.Telemetry(msg.Payload)
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleFriction(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload FrictionPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		s.logger.Debug("failed to parse friction payload", "error", err)
	} else {
		s.service.Friction(payload)
	}
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleSessionFinalize(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload SessionFinalizeIPCPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		s.logger.Debug("failed to parse session_finalize payload", "error", err)
	} else if payload.SessionName == "" || payload.LedgerPath == "" {
		s.logger.Debug("session_finalize payload missing required fields", "session_name", payload.SessionName, "ledger_path", payload.LedgerPath)
	} else {
		s.service.SessionFinalize(payload)
	}
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleGetErrors(s *Server, _ Message, _ net.Conn) HandlerResult {
	errs := s.service.GetErrors()
	if errs != nil {
		return HandlerResult{Response: marshalResponse(errs)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`[]`)},
	}
}

func handleMarkErrors(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload MarkErrorsPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("invalid payload: %v", err)},
		}
	}
	s.service.MarkErrors(payload.IDs)
	return HandlerResult{
		Response: &Response{Success: true},
	}
}

func handleCheckout(s *Server, msg Message, conn net.Conn) HandlerResult {
	var payload CheckoutPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("invalid checkout payload: %v", err)},
		}
	}

	pw := &ProgressWriter{conn: conn}
	result, err := s.service.Checkout(payload, pw)
	if err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: err.Error()},
		}
	}
	if result == nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: "checkout handler not set"},
		}
	}

	return HandlerResult{Response: marshalResponse(result)}
}

func handleSessions(s *Server, _ Message, _ net.Conn) HandlerResult {
	sessions := s.service.Sessions()
	if sessions != nil {
		resp := SessionsResponse{Sessions: sessions}
		return HandlerResult{Response: marshalResponse(resp)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{"sessions":[]}`)},
	}
}

func handleInstances(s *Server, _ Message, _ net.Conn) HandlerResult {
	instances := s.service.Instances()
	if instances != nil {
		resp := InstancesResponse{Instances: instances}
		return HandlerResult{Response: marshalResponse(resp)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{"instances":[]}`)},
	}
}

func handleDoctor(s *Server, _ Message, _ net.Conn) HandlerResult {
	resp := s.service.Doctor()
	if resp == nil {
		resp = &DoctorResponse{}
	}
	return HandlerResult{Response: marshalResponse(resp)}
}

func handleTriggerGC(s *Server, _ Message, _ net.Conn) HandlerResult {
	resp := s.service.TriggerGC()
	if resp == nil {
		resp = &TriggerGCResponse{}
	}
	return HandlerResult{Response: marshalResponse(resp)}
}

func handleCodeIndex(s *Server, msg Message, conn net.Conn) HandlerResult {
	var payload CodeIndexPayload
	if len(msg.Payload) > 0 {
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return HandlerResult{
				Response: &Response{Success: false, Error: fmt.Sprintf("invalid code_index payload: %v", err)},
			}
		}
	}

	pw := &ProgressWriter{conn: conn}
	result, err := s.service.CodeIndex(payload, pw)
	if err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: err.Error()},
		}
	}
	if result == nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: "code index handler not set"},
		}
	}

	return HandlerResult{Response: marshalResponse(result)}
}

func handleCodeStatus(s *Server, _ Message, _ net.Conn) HandlerResult {
	stats := s.service.CodeStatus()
	if stats != nil {
		return HandlerResult{Response: marshalResponse(stats)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{}`)},
	}
}

func handleMurmur(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload MurmurPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		s.logger.Debug("failed to parse murmur payload", "error", err)
	} else if payload.TargetDir == "" || payload.RelPath == "" {
		s.logger.Debug("murmur payload missing required fields", "target_dir", payload.TargetDir, "rel_path", payload.RelPath)
	} else {
		s.service.PublishMurmur(payload)
	}
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleWhispers(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload WhispersPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("invalid payload: %v", err)},
		}
	}

	if payload.AgentID == "" {
		return HandlerResult{
			Response: &Response{Success: false, Error: "agent_id required"},
		}
	}

	attention := whisperstore.Attention(payload.Attention)
	if attention == "" {
		attention = whisperstore.AttentionNormal
	}

	entries, err := s.service.Whispers(payload.AgentID, attention, payload.Topics)
	if err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("get whispers: %v", err)},
		}
	}
	if entries == nil {
		entries = []whisperstore.WhisperEntry{}
	}
	resp := WhispersResponse{Entries: entries}
	return HandlerResult{Response: marshalResponse(resp)}
}

func handleMurmurPause(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload MurmurPausePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		s.logger.Debug("failed to parse murmur_pause payload", "error", err)
	} else if payload.AgentID == "" {
		s.logger.Debug("murmur_pause payload missing agent_id")
	} else {
		s.service.PauseMurmuring(payload.AgentID)
	}
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleMurmurResume(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload MurmurPausePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		s.logger.Debug("failed to parse murmur_resume payload", "error", err)
	} else if payload.AgentID == "" {
		s.logger.Debug("murmur_resume payload missing agent_id")
	} else {
		s.service.ResumeMurmuring(payload.AgentID)
	}
	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleWhisperHistory(s *Server, msg Message, _ net.Conn) HandlerResult {
	var payload WhisperHistoryPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("invalid payload: %v", err)},
		}
	}
	// agent_id must be non-empty: per-agent cursor tracking is undefined for the all-agents query
	if payload.AgentID == "" {
		return HandlerResult{
			Response: &Response{Success: false, Error: "agent_id is required for whisper history"},
		}
	}

	result, err := s.service.WhisperHistory(payload.AgentID, payload.Before, payload.Limit)
	if err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("whisper history: %v", err)},
		}
	}
	return HandlerResult{Response: marshalResponse(result)}
}
