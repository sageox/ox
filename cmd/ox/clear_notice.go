package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/session"
)

// envClearNotice carries information about a session finalized by /clear from
// the hook process to the prime subprocess so prime can include a user-facing
// notice describing the boundary transition.
//
// See ADR-019 (session entity lifecycle) for why /clear is a session boundary
// and why the user is told about the transition.
const envClearNotice = "OX_CLEAR_PRIOR_SESSION"

// ClearNoticeInfo describes the session that was finalized at a /clear boundary.
type ClearNoticeInfo struct {
	SessionName  string `json:"session_name"`            // e.g. "2026-05-27T07-12-ryan-Oxa7b3"
	AgentID      string `json:"agent_id"`                // e.g. "Oxa7b3"
	Status       string `json:"status"`                  // "recording" | "suspended"
	DurationSecs int64  `json:"duration_secs,omitempty"` // session lifetime up to /clear
	WasSuspended bool   `json:"was_suspended,omitempty"` // true if SuspendedAt was set on finalize
}

// buildClearNoticeFromState builds a ClearNoticeInfo from the recording state
// being finalized at a /clear boundary. Returns nil if state is nil.
func buildClearNoticeFromState(state *session.RecordingState) *ClearNoticeInfo {
	if state == nil {
		return nil
	}
	info := &ClearNoticeInfo{
		AgentID: state.AgentID,
	}
	if state.SessionPath != "" {
		info.SessionName = filepath.Base(state.SessionPath)
	}
	if !state.StartedAt.IsZero() {
		info.DurationSecs = int64(time.Since(state.StartedAt).Seconds())
	}
	// ADR-020: when SuspendedAt is set, the prior session was actively paused.
	// New post-/clear session inherits the suspended state via the pause marker.
	if state.SuspendedAt != nil {
		info.WasSuspended = true
		info.Status = "suspended"
	} else {
		info.Status = "recording"
	}
	return info
}

// serializeClearNoticeEnv returns a "KEY=VAL" pair suitable for cmd.Env.
// Returns empty string if info is nil.
func serializeClearNoticeEnv(info *ClearNoticeInfo) string {
	if info == nil {
		return ""
	}
	b, err := json.Marshal(info)
	if err != nil {
		return ""
	}
	return envClearNotice + "=" + string(b)
}

// parseClearNoticeEnv reads OX_CLEAR_PRIOR_SESSION from the environment.
// Returns nil if unset or malformed.
func parseClearNoticeEnv() *ClearNoticeInfo {
	raw := os.Getenv(envClearNotice)
	if raw == "" {
		return nil
	}
	var info ClearNoticeInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return nil
	}
	if info.SessionName == "" {
		return nil
	}
	return &info
}

// renderClearNotice formats a user-facing notice describing the /clear boundary.
//
// Format:
//
//	[ox] /clear → previous session <name> (<status>, <duration>) finalized.
//	     New session <newAgentID> started. Recording: <on|off>.
//
// When the prior session was suspended, the notice indicates the paused range
// is excluded from upload (ADR-020 semantics; for ADR-019-only deploys the
// extra line is still accurate since suspended-then-stopped sessions in this
// codebase upload empty content for the suspended range).
func renderClearNotice(info *ClearNoticeInfo, newAgentID string, recordingOn bool) string {
	if info == nil {
		return ""
	}
	dur := formatClearDuration(time.Duration(info.DurationSecs) * time.Second)
	recState := "off"
	if recordingOn {
		recState = "on"
	}
	// newAgentID already carries the agent's "Ox" prefix from
	// agentinstance.NewID — re-prefixing it produces "OxOxa7b3" in user-
	// visible notices. Render the ID as-is.
	if info.WasSuspended {
		return fmt.Sprintf(
			"[ox] /clear → previous session %s (suspended, %s) finalized. "+
				"Paused range excluded from upload. "+
				"New session %s started — RECORDING SUSPENDED (carried from previous). "+
				"Resume: /ox-session-resume · Stop: /ox-session-stop",
			info.SessionName, dur, newAgentID,
		)
	}
	return fmt.Sprintf(
		"[ox] /clear → previous session %s (%s, %s) finalized. "+
			"New session %s started. Recording: %s.",
		info.SessionName, info.Status, dur, newAgentID, recState,
	)
}

// formatClearDuration returns a compact human-readable duration like "3m 24s",
// "1h 12m", or "47s". Used for the /clear notice.
func formatClearDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
