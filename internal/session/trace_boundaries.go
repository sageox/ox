package session

import (
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/sageox/ox/internal/trace/receiver"
)

// initializeTraceCapture freezes opt-in for this recording. Existing recordings
// and non-Claude sessions never acquire trace capture merely because config changes.
func (r *RecordingState) initializeTraceCapture() {
	agentType := r.AgentType
	if agentType == "" {
		agentType = r.AdapterName
	}
	// Auto-prime uses the detection alias "claude". Apply the same alias
	// resolution as adapter lookup before deciding whether capture is supported.
	if adapters.CanonicalAdapterName(agentType) != "claude-code" {
		return
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		slog.Debug("trace capture config unavailable", "error", err)
		return
	}
	if cfg.Trace == nil || !cfg.Trace.Enabled {
		return
	}
	r.Trace = &model.Capture{}
	r.RecordTraceBoundary("start", r.StartedAt)
}

// RecordTraceBoundary takes a best-effort, immutable byte boundary. A failed
// snapshot is still appended, with unknown offsets, so it cannot expose paused
// or pre-recording data. Stop is idempotent for delayed finalization and retries.
func (r *RecordingState) RecordTraceBoundary(action string, at time.Time) {
	if r == nil || r.Trace == nil {
		return
	}
	for _, b := range r.Trace.Boundaries {
		if b.Action == "stop" {
			return
		}
	}
	ids := make([]string, 0, len(r.NativeSessions)+1)
	seen := map[string]bool{}
	for _, native := range r.NativeSessions {
		id := strings.ToLower(native.ID)
		if receiver.ValidSessionID(id) && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	id := strings.ToLower(r.AgentSessionID)
	if receiver.ValidSessionID(id) && !seen[id] {
		ids = append(ids, id)
	}
	offsets, err := receiver.SnapshotOffsets(paths.TraceSpoolDir(), ids)
	if err != nil {
		slog.Debug("trace boundary unavailable", "action", action, "error", err)
		// Stable diagnostic only: raw filesystem paths and errors are local logs.
		r.Trace.Errors = append(r.Trace.Errors, "boundary-unavailable:"+action)
	}
	r.Trace.Boundaries = append(r.Trace.Boundaries, model.Boundary{Action: action, At: at.UTC(), Offsets: offsets})
}
