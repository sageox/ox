package hooks

import "time"

// MurmurPayload builds the payload for murmur.received and murmur.critical events.
func MurmurPayload(id, agentID, principal, topic, importance, content string) map[string]any {
	return map[string]any{
		"murmur": map[string]any{
			"id": id, "agent_id": agentID, "principal": principal,
			"topic": topic, "importance": importance, "content": content,
		},
	}
}

// SessionUploadedPayload builds the payload for session.uploaded events.
func SessionUploadedPayload(name, url, agentID string, dur time.Duration) map[string]any {
	return map[string]any{
		"session": map[string]any{
			"name": name, "url": url, "agent_id": agentID,
			"duration_seconds": int(dur.Seconds()),
		},
	}
}

// SessionAvailablePayload builds the payload for session.available events.
func SessionAvailablePayload(name, url, principal, agentID string) map[string]any {
	return map[string]any{
		"session": map[string]any{
			"name": name, "url": url, "principal": principal, "agent_id": agentID,
		},
	}
}

// SessionPayload builds the payload for session.started events.
func SessionPayload(name, agentID string) map[string]any {
	return map[string]any{
		"session": map[string]any{"name": name, "agent_id": agentID},
	}
}

// SessionStoppedPayload builds the payload for session.stopped with duration.
func SessionStoppedPayload(name, agentID string, dur time.Duration) map[string]any {
	return map[string]any{
		"session": map[string]any{
			"name": name, "agent_id": agentID,
			"duration_seconds": int(dur.Seconds()),
		},
	}
}

// SyncPayload builds the payload for sync.completed events.
func SyncPayload(workspace, syncType string, dur time.Duration) map[string]any {
	return map[string]any{
		"sync": map[string]any{
			"workspace": workspace, "type": syncType, "duration_ms": dur.Milliseconds(),
		},
	}
}

// SyncFailedPayload builds the payload for sync.failed events.
func SyncFailedPayload(workspace, syncType, errMsg string) map[string]any {
	return map[string]any{
		"sync": map[string]any{
			"workspace": workspace, "type": syncType, "error": errMsg,
		},
	}
}

// AgentPayload builds the payload for agent.registered events.
func AgentPayload(id, agentType, principal string) map[string]any {
	return map[string]any{
		"agent": map[string]any{"id": id, "type": agentType, "principal": principal},
	}
}

// AgentIdlePayload builds the payload for agent.idle events.
func AgentIdlePayload(id string, idleSince time.Time) map[string]any {
	return map[string]any{
		"agent": map[string]any{
			"id": id, "idle_since": idleSince.UTC().Format(time.RFC3339),
		},
	}
}
