package session

import (
	"log/slog"
	"reflect"
	"sort"
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
	pruneLocalTraceAuthorizations(time.Now())
	// Only a new local recording can establish trace authority. Never upgrade
	// a raw carrier or a recovered .recording.json into trusted capture state.
	if err := writeLocalTraceCapture(r.SessionPath, r.Trace); err != nil {
		slog.Debug("local trace capture unavailable", "error", err)
		r.Trace = nil
		return
	}
	ids := []string{r.AgentSessionID}
	for _, native := range r.NativeSessions {
		ids = append(ids, native.ID)
	}
	r.recordTraceBoundary("start", r.StartedAt, ids...)
}

// RecordTraceBoundary takes a best-effort, immutable byte boundary. A failed
// snapshot is still appended, with unknown offsets, so it cannot expose paused
// or pre-recording data. Stop is idempotent for delayed finalization and retries.
func (r *RecordingState) RecordTraceBoundary(action string, at time.Time) {
	r.recordTraceBoundary(action, at)
}

func (r *RecordingState) recordTraceBoundary(action string, at time.Time, nativeIDs ...string) {
	if r == nil || r.Trace == nil {
		return
	}
	auth, err := readLocalTraceAuthorization(r.SessionPath)
	if err != nil || !reflect.DeepEqual(auth.Capture, r.Trace) {
		slog.Debug("trace boundary lacks matching local capture", "action", action, "error", err)
		r.Trace = nil
		return
	}
	capture := auth.Capture
	seen := map[string]bool{}
	for _, id := range auth.NativeIDs {
		seen[id] = true
	}
	for _, b := range capture.Boundaries {
		if b.Action == "stop" {
			return
		}
		for id := range b.Offsets {
			seen[id] = true
		}
	}
	// New IDs enter only through local recording start / native-session hooks,
	// never through mutable NativeSessions fields in recovered shared files.
	for _, nativeID := range nativeIDs {
		id := strings.ToLower(nativeID)
		if receiver.ValidSessionID(id) {
			seen[id] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	auth.NativeIDs = ids
	offsets, err := receiver.SnapshotOffsets(paths.TraceSpoolDir(), ids)
	if err != nil {
		slog.Debug("trace boundary unavailable", "action", action, "error", err)
		// Stable diagnostic only: raw filesystem paths and errors are local logs.
		capture.Errors = append(capture.Errors, "boundary-unavailable:"+action)
	}
	capture.Boundaries = append(capture.Boundaries, model.Boundary{Action: action, At: at.UTC(), Offsets: offsets})
	if err := writeLocalTraceAuthorization(r.SessionPath, auth); err != nil {
		slog.Debug("persist local trace boundary failed", "action", action, "error", err)
		r.Trace = nil
		return
	}
	r.Trace = capture
}
